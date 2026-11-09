package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

type oauthTokens struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int64  `json:"expires_in"`
	ExpiresAt     int64  `json:"expires_at"`
	TokenURL      string `json:"token_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	RevocationURL string `json:"revocation_url,omitempty"`
}
type oauthFlow struct {
	mu                                                sync.Mutex
	ID, Name, State, AuthorizationURL, Status, Detail string
	CreatedAt                                         time.Time
	ctx                                               context.Context
	stop                                              context.CancelFunc
	server                                            *http.Server
	verifier                                          string
	committed, claimed                                bool
}

func (f *oauthFlow) snapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotLocked()
}
func (f *oauthFlow) snapshotLocked() map[string]any {
	return map[string]any{"id": f.ID, "name": f.Name, "status": f.Status, "detail": f.Detail, "authorization_url": f.AuthorizationURL, "created_at": f.CreatedAt, "committed": f.committed}
}
func (f *oauthFlow) cancel() {
	f.mu.Lock()
	if !f.committed {
		f.Status = "canceled"
		f.Detail = "authorization canceled; no credentials adopted"
	}
	f.stop()
	server := f.server
	f.verifier = ""
	f.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}
func (c *connectorManager) flowPath(ctx context.Context, name string) string {
	sum := sha256.Sum256([]byte(connectorKey(ctx, name)))
	return filepath.Join(c.host.StateDir, "connectors", fmt.Sprintf("%x.json", sum[:16]))
}
func (c *connectorManager) persistFlow(ctx context.Context, name string, f *oauthFlow) error {
	data, err := json.Marshal(f.snapshot())
	if err != nil {
		return err
	}
	return writePrivate(c.flowPath(ctx, name), data)
}
func (c *connectorManager) restoredFlow(ctx context.Context, name string) (map[string]any, error) {
	data, err := os.ReadFile(c.flowPath(ctx, name))
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		return nil, errors.New("connector operation state invalid")
	}
	status, _ := result["status"].(string)
	if status == "pending" || status == "exchanging" {
		result["status"] = "interrupted"
		result["detail"] = "restart interrupted authorization; begin a new attempt, no callback or side effect is replayed"
		delete(result, "authorization_url")
	}
	return result, nil
}
func safeOAuthEndpoint(value string) error {
	return (config.PluginConnectorConfig{Type: "mcp", URL: value}).Validate()
}
func (c *connectorManager) metadataGET(ctx context.Context, endpoint string, out any) error {
	if err := safeOAuthEndpoint(endpoint); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OAuth metadata returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out); err != nil {
		return errors.New("OAuth metadata JSON invalid")
	}
	return nil
}
func (c *connectorManager) metadata(ctx context.Context, cfg config.PluginConnectorConfig) (config.PluginConnectorConfig, error) {
	if cfg.OAuth.AuthorizationURL != "" && cfg.OAuth.TokenURL != "" {
		return cfg, nil
	}
	resource, _ := url.Parse(cfg.URL)
	issuer := resource.Scheme + "://" + resource.Host
	// RFC 9728 资源元数据可以指定独立授权服务器；失败时兼容同源 RFC 8414 服务。
	var protected struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	path := issuer + "/.well-known/oauth-protected-resource" + strings.TrimRight(resource.Path, "/")
	if err := c.metadataGET(ctx, path, &protected); err == nil {
		if protected.Resource != "" && strings.TrimRight(protected.Resource, "/") != strings.TrimRight(cfg.URL, "/") {
			return cfg, errors.New("OAuth protected resource identity mismatch")
		}
		if len(protected.Servers) > 0 {
			issuer = strings.TrimRight(protected.Servers[0], "/")
			if err := safeOAuthEndpoint(issuer); err != nil {
				return cfg, err
			}
		}
	}
	parsed, _ := url.Parse(issuer)
	metadataURL := parsed.Scheme + "://" + parsed.Host + "/.well-known/oauth-authorization-server" + strings.TrimRight(parsed.Path, "/")
	var metadata struct {
		Issuer           string   `json:"issuer"`
		Authorization    string   `json:"authorization_endpoint"`
		Token            string   `json:"token_endpoint"`
		Registration     string   `json:"registration_endpoint"`
		Revocation       string   `json:"revocation_endpoint"`
		ChallengeMethods []string `json:"code_challenge_methods_supported"`
	}
	if err := c.metadataGET(ctx, metadataURL, &metadata); err != nil {
		return cfg, fmt.Errorf("OAuth discovery failed; configure explicit authorization_url/token_url: %w", err)
	}
	if metadata.Issuer != "" && strings.TrimRight(metadata.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return cfg, errors.New("OAuth issuer metadata mismatch")
	}
	if len(metadata.ChallengeMethods) > 0 && !contains(metadata.ChallengeMethods, "S256") {
		return cfg, errors.New("OAuth server does not support S256 PKCE")
	}
	cfg.OAuth.AuthorizationURL = metadata.Authorization
	cfg.OAuth.TokenURL = metadata.Token
	if cfg.OAuth.RegistrationURL == "" {
		cfg.OAuth.RegistrationURL = metadata.Registration
	}
	if cfg.OAuth.RevocationURL == "" {
		cfg.OAuth.RevocationURL = metadata.Revocation
	}
	if cfg.OAuth.AuthorizationURL == "" || cfg.OAuth.TokenURL == "" {
		return cfg, errors.New("OAuth metadata lacks required endpoints")
	}
	return cfg, cfg.Validate()
}
func (c *connectorManager) registerClient(ctx context.Context, cfg config.PluginConnectorConfig, redirect string) (config.PluginConnectorConfig, string, error) {
	if cfg.OAuth.ClientID != "" {
		return cfg, "", nil
	}
	if cfg.OAuth.RegistrationURL == "" {
		return cfg, "", errors.New("OAuth client_id missing and server has no dynamic registration endpoint")
	}
	body, _ := json.Marshal(map[string]any{"client_name": "Saber", "redirect_uris": []string{redirect}, "grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none", "application_type": "native"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.OAuth.RegistrationURL, bytes.NewReader(body))
	if err != nil {
		return cfg, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return cfg, "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return cfg, "", fmt.Errorf("OAuth native-client registration failed HTTP %d", response.StatusCode)
	}
	var result struct {
		ClientID     string   `json:"client_id"`
		ClientSecret string   `json:"client_secret"`
		Redirects    []string `json:"redirect_uris"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result) != nil || result.ClientID == "" {
		return cfg, "", errors.New("OAuth registration returned no client_id")
	}
	if len(result.Redirects) > 0 && !contains(result.Redirects, redirect) {
		return cfg, "", errors.New("OAuth registration did not accept exact loopback redirect")
	}
	cfg.OAuth.ClientID = result.ClientID
	return cfg, result.ClientSecret, nil
}
func (c *connectorManager) oauthRequest(ctx context.Context, endpoint string, values url.Values) (map[string]any, error) {
	if err := safeOAuthEndpoint(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if len(raw) > 1<<20 {
		return nil, errors.New("OAuth response too large")
	}
	var result map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("OAuth server returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("OAuth request failed HTTP %d; authorization refused or expired", response.StatusCode)
	}
	return result, nil
}
func (c *connectorManager) exchange(ctx context.Context, cfg config.PluginConnectorConfig, values url.Values) (oauthTokens, error) {
	values.Set("client_id", cfg.OAuth.ClientID)
	if cfg.OAuth.ClientSecretEnv != "" {
		secret := os.Getenv(cfg.OAuth.ClientSecretEnv)
		if secret == "" {
			return oauthTokens{}, errors.New("OAuth client secret environment missing")
		}
		values.Set("client_secret", secret)
	}
	result, err := c.oauthRequest(ctx, cfg.OAuth.TokenURL, values)
	if err != nil {
		return oauthTokens{}, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return oauthTokens{}, err
	}
	defer clear(data)
	var tokens oauthTokens
	if json.Unmarshal(data, &tokens) != nil || tokens.AccessToken == "" || !strings.EqualFold(tokens.TokenType, "bearer") {
		return tokens, errors.New("OAuth token response missing bearer access_token")
	}
	if tokens.ExpiresIn > 0 {
		tokens.ExpiresAt = time.Now().Unix() + tokens.ExpiresIn
	}
	tokens.TokenURL = cfg.OAuth.TokenURL
	tokens.ClientID = cfg.OAuth.ClientID
	tokens.ClientSecret = values.Get("client_secret")
	tokens.RevocationURL = cfg.OAuth.RevocationURL
	return tokens, nil
}
func (c *connectorManager) refresh(ctx context.Context, cfg config.PluginConnectorConfig, previous oauthTokens) (oauthTokens, error) {
	if cfg.OAuth.TokenURL == "" {
		cfg.OAuth.TokenURL = previous.TokenURL
	}
	if cfg.OAuth.ClientID == "" {
		cfg.OAuth.ClientID = previous.ClientID
	}
	if err := cfg.Validate(); err != nil {
		return oauthTokens{}, err
	}
	if cfg.OAuth.TokenURL == "" {
		return oauthTokens{}, errors.New("OAuth refresh token endpoint unavailable")
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {previous.RefreshToken}}
	if previous.ClientSecret != "" {
		values.Set("client_secret", previous.ClientSecret)
	}
	tokens, err := c.exchange(ctx, cfg, values)
	if err != nil {
		return tokens, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = previous.RefreshToken
	}
	if tokens.RevocationURL == "" {
		tokens.RevocationURL = previous.RevocationURL
	}
	return tokens, nil
}
func (c *connectorManager) beginOAuth(ctx context.Context, name string, cfg config.PluginConnectorConfig) (any, error) {
	c.mu.Lock()
	store := c.vault
	c.mu.Unlock()
	if store == nil {
		return nil, errors.New("OAuth requires attached encrypted vault")
	}
	cfg, err := c.metadata(ctx, cfg)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.OAuth.RedirectPort))
	if err != nil {
		return nil, fmt.Errorf("OAuth loopback callback listener: %w", err)
	}
	redirect := "http://" + listener.Addr().String() + "/callback"
	cfg, clientSecret, err := c.registerClient(ctx, cfg, redirect)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		_ = listener.Close()
		return nil, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(random)
	challenge := sha256.Sum256([]byte(verifier))
	state := randomID()
	flowCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	f := &oauthFlow{ID: randomID(), Name: name, State: state, Status: "pending", CreatedAt: time.Now(), ctx: flowCtx, stop: stop, verifier: verifier}
	endpoint, _ := url.Parse(cfg.OAuth.AuthorizationURL)
	q := endpoint.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.OAuth.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", strings.Join(cfg.OAuth.Scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("resource", cfg.URL)
	endpoint.RawQuery = q.Encode()
	f.AuthorizationURL = endpoint.String()
	mux := http.NewServeMux()
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	key := connectorKey(ctx, name)
	c.mu.Lock()
	old := c.flows[key]
	c.flows[key] = f
	c.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	if err := c.persistFlow(ctx, name, f); err != nil {
		f.cancel()
		_ = listener.Close()
		return nil, err
	}
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(f.State)) != 1 {
			http.Error(w, "Invalid authorization state", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		if f.claimed || f.Status != "pending" {
			f.mu.Unlock()
			http.Error(w, "Authorization no longer pending", http.StatusConflict)
			return
		}
		f.claimed = true
		f.Status = "exchanging"
		localVerifier := f.verifier
		f.mu.Unlock()
		if r.URL.Query().Get("error") != "" {
			c.finishOAuth(ctx, name, f, "denied", "authorization server denied consent")
			http.Error(w, "Authorization denied", http.StatusForbidden)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			c.finishOAuth(ctx, name, f, "failed", "authorization code missing")
			http.Error(w, "Authorization code missing", http.StatusBadRequest)
			return
		}
		values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {localVerifier}, "resource": {cfg.URL}}
		if clientSecret != "" {
			values.Set("client_secret", clientSecret)
		}
		tokens, err := c.exchange(f.ctx, cfg, values)
		if err == nil {
			_, err = c.authorize(f.ctx, name, "")
		}
		if err == nil && cfg.Type == "mcp" {
			session, probeErr := c.connectMCP(f.ctx, name, cfg, tokens.AccessToken)
			if probeErr != nil {
				err = probeErr
			} else {
				err = session.Close()
			}
		}
		if err == nil && cfg.Type == "managed" {
			err = c.probeManaged(f.ctx, cfg, tokens.AccessToken)
		}
		if err != nil {
			c.finishOAuth(ctx, name, f, "failed", "authorization or connector initialization failed; prior credentials unchanged")
			http.Error(w, "Authorization/connection failed. Inspect manage_connections continue.", http.StatusBadGateway)
			return
		}
		data, err := json.Marshal(tokens)
		if err != nil {
			c.finishOAuth(ctx, name, f, "failed", "token encoding failed")
			http.Error(w, "Authorization failed", 500)
			return
		}
		defer clear(data)
		lock := c.credentialLock(ctx, name)
		lock.Lock()
		defer lock.Unlock()
		c.mu.Lock()
		f.mu.Lock()
		if c.flows[key] != f || f.ctx.Err() != nil || f.Status != "exchanging" {
			f.mu.Unlock()
			c.mu.Unlock()
			http.Error(w, "Authorization canceled", http.StatusConflict)
			return
		}
		err = store.EncodeSecret(f.ctx, "connector:"+name, data)
		if err == nil {
			f.committed = true
			f.Status = "connected"
			f.Detail = "authorization verified and encrypted credentials committed"
			f.verifier = ""
		}
		f.mu.Unlock()
		c.mu.Unlock()
		if err != nil {
			c.finishOAuth(ctx, name, f, "failed", "credential commit failed; prior credentials unchanged")
			http.Error(w, "Credential commit failed", 500)
			return
		}
		if err := c.persistFlow(ctx, name, f); err != nil {
			http.Error(w, "Credentials saved but operation status could not be persisted", 500)
			return
		}
		_, _ = io.WriteString(w, "Authorization complete. You can close this window.")
		go func() { _ = f.server.Close() }()
	})
	go func() {
		err := f.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.finishOAuth(ctx, name, f, "failed", "OAuth callback receiver stopped")
		}
	}()
	go func() {
		<-flowCtx.Done()
		f.mu.Lock()
		pending := !f.committed && f.Status != "canceled" && f.Status != "failed" && f.Status != "denied"
		f.mu.Unlock()
		if pending {
			c.finishOAuth(ctx, name, f, "expired", "authorization expired; begin a new attempt")
		}
		_ = f.server.Close()
	}()
	return f.snapshot(), nil
}
func (c *connectorManager) finishOAuth(ctx context.Context, name string, f *oauthFlow, status, detail string) {
	f.mu.Lock()
	if !f.committed && f.Status != "canceled" {
		f.Status = status
		f.Detail = detail
	}
	f.verifier = ""
	f.mu.Unlock()
	if err := c.persistFlow(ctx, name, f); err != nil {
		f.mu.Lock()
		f.Detail += "; failed to persist operation status"
		f.mu.Unlock()
	}
}
func (c *connectorManager) cancelOAuth(ctx context.Context, name string) (any, error) {
	key := connectorKey(ctx, name)
	c.mu.Lock()
	f := c.flows[key]
	if f == nil {
		c.mu.Unlock()
		if restored, err := c.restoredFlow(ctx, name); err == nil {
			return restored, nil
		}
		return map[string]any{"status": "not_pending"}, nil
	}
	f.mu.Lock()
	if f.committed {
		result := f.snapshotLocked()
		f.mu.Unlock()
		c.mu.Unlock()
		return result, nil
	}
	f.Status = "canceled"
	f.Detail = "authorization canceled; credentials not adopted"
	f.verifier = ""
	f.stop()
	server := f.server
	f.mu.Unlock()
	c.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	if err := c.persistFlow(ctx, name, f); err != nil {
		return nil, err
	}
	return f.snapshot(), nil
}
