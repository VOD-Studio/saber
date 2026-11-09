package model

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

type oauthToken struct {
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token,omitempty"`
	TokenType      string    `json:"token_type,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Scope          string    `json:"scope,omitempty"`
	Dead           bool      `json:"dead,omitempty"`
	UnusableReason string    `json:"unusable_reason,omitempty"`
}

type oauthPending struct {
	Provider   string
	Verifier   string
	State      string
	Redirect   string
	DeviceCode string
	Expires    time.Time
	Interval   time.Duration
}

type authManager struct {
	dir       string
	providers map[string]config.ProviderConfig
	mu        sync.Mutex
	pending   map[string]oauthPending
}

func readPrivateJSON(path string, dst any) error {
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("credential file must be a private regular file (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return errors.New("credential file changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err = decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid private credential JSON: %w", err)
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("credential file contains trailing data")
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("credential directory must be private (0700)")
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("refusing unsafe credential destination")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

func randomSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func safeProviderFile(dir, provider string) string {
	hash := sha256.Sum256([]byte(provider))
	return filepath.Join(dir, fmt.Sprintf("%x.oauth.json", hash[:16]))
}

func (a *authManager) tokenPath(provider string) string {
	if raw := a.providers[provider].OAuth.TokenFile; raw != "" {
		return raw
	}
	return safeProviderFile(a.dir, provider)
}

func (a *authManager) postToken(ctx context.Context, cfg config.OAuthConfig, values url.Values) (oauthToken, error) {
	values.Set("client_id", cfg.ClientID)
	if cfg.ClientSecret != "" {
		values.Set("client_secret", cfg.ClientSecret)
	}
	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
		Key          string `json:"key"`
		Status       string `json:"status"`
		ExpiredIn    int64  `json:"expired_in"`
	}
	if err := oauthRequest(ctx, cfg, values, &raw); err != nil {
		return oauthToken{}, err
	}
	if raw.Error != "" {
		return oauthToken{}, &oauthError{Code: raw.Error}
	}
	if cfg.Flow == "openrouter" {
		raw.AccessToken = raw.Key
	}
	if cfg.Flow == "minimax" {
		if raw.Status == "error" {
			return oauthToken{}, &oauthError{Code: "access_denied"}
		}
		if raw.AccessToken == "" && raw.Status != "success" {
			return oauthToken{}, &oauthError{Code: "authorization_pending"}
		}
	}
	if cfg.Flow == "qwen" && raw.Status == "pending" {
		return oauthToken{}, &oauthError{Code: "authorization_pending"}
	}
	if raw.AccessToken == "" {
		return oauthToken{}, errors.New("OAuth token endpoint returned no access_token")
	}
	token := oauthToken{AccessToken: raw.AccessToken, RefreshToken: raw.RefreshToken, TokenType: raw.TokenType, Scope: raw.Scope}
	if raw.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	if raw.ExpiredIn > 0 {
		token.ExpiresAt = miniMaxExpiry(raw.ExpiredIn)
	}
	return token, nil
}

type oauthError struct{ Code string }

func (e *oauthError) Error() string { return "OAuth authorization failed: " + e.Code }

func (a *authManager) token(ctx context.Context, provider, path string, force bool) (oauthToken, error) {
	unlock, err := lockCredential(ctx, path)
	if err != nil {
		return oauthToken{}, err
	}
	defer unlock()
	var old oauthToken
	err = readPrivateJSON(path, &old)
	cfg := a.providers[provider].OAuth
	if err != nil && (!errors.Is(err, os.ErrNotExist) || cfg.Flow != "client_credentials") {
		return old, fmt.Errorf("OAuth credential unavailable; authenticate provider %q: %w", provider, err)
	}
	if old.Dead {
		return old, fmt.Errorf("OAuth credential is unusable (%s); a new login is required", old.UnusableReason)
	}
	if !force && old.AccessToken != "" && (old.ExpiresAt.IsZero() || time.Until(old.ExpiresAt) > 2*time.Minute) {
		return old, nil
	}
	if !cfg.Enabled {
		return old, errors.New("OAuth refresh is not configured")
	}
	values := url.Values{}
	if cfg.Flow == "client_credentials" {
		values.Set("grant_type", "client_credentials")
		values.Set("scope", strings.Join(cfg.Scopes, " "))
	} else {
		if old.RefreshToken == "" {
			return old, errors.New("OAuth access token expired and has no refresh token; login required")
		}
		values.Set("grant_type", "refresh_token")
		values.Set("refresh_token", old.RefreshToken)
		// 先把单次 refresh 标记为不可重放；进程在网络请求或落盘中断时必须重新登录。
		quarantined := old
		quarantined.Dead = true
		quarantined.UnusableReason = "refresh_interrupted_or_in_flight"
		if err = writePrivateJSON(path, quarantined); err != nil {
			return old, err
		}
	}
	fresh, err := a.postToken(ctx, cfg, values)
	if err != nil {
		var authErr *oauthError
		if errors.As(err, &authErr) && (authErr.Code == "invalid_grant" || authErr.Code == "invalid_token" || authErr.Code == "token_revoked" || authErr.Code == "refresh_token_reused") {
			old.Dead = true
			old.UnusableReason = authErr.Code
			if persistErr := writePrivateJSON(path, old); persistErr != nil {
				return old, errors.Join(err, persistErr)
			}
		} else if cfg.Flow != "client_credentials" {
			var protocol *ProtocolError
			if errors.As(err, &protocol) && protocol.Status < 500 {
				if persistErr := writePrivateJSON(path, old); persistErr != nil {
					return old, errors.Join(err, persistErr)
				}
			}
		}
		return old, err
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = old.RefreshToken
	}
	if err = writePrivateJSON(path, fresh); err != nil {
		// 已旋转的 refresh token 不允许在落盘失败后再次使用。
		old.Dead = true
		old.UnusableReason = "credential_persist_failed"
		markErr := writePrivateJSON(path, old)
		return oauthToken{}, errors.Join(fmt.Errorf("rotated OAuth credentials were not durably saved: %w", err), markErr)
	}
	return fresh, nil
}

// AuthCommand 仅供宿主管理员命令使用；返回状态与授权 URL，永不返回令牌材料。
func (c *Core) AuthCommand(ctx context.Context, args []string) (any, error) {
	c.clientsMu.RLock()
	rt := c.runtime
	c.clientsMu.RUnlock()
	if rt == nil || rt.auth == nil {
		return nil, errors.New("model runtime authentication is not configured")
	}
	if len(args) < 2 {
		return nil, errors.New("usage: model-auth status|login|finish|logout|refresh <provider> [state callback_url]")
	}
	a := rt.auth
	provider := args[1]
	p, ok := a.providers[provider]
	if !ok {
		return nil, errors.New("provider is not configured")
	}
	if args[0] == "pool-status" || args[0] == "pool-reset" {
		return a.poolCommand(ctx, provider, p, args)
	}
	cfg := p.OAuth
	if !cfg.Enabled {
		return nil, errors.New("OAuth is disabled for this provider")
	}
	path := a.tokenPath(provider)
	switch args[0] {
	case "status":
		var token oauthToken
		if err := readPrivateJSON(path, &token); errors.Is(err, os.ErrNotExist) {
			return map[string]any{"provider": provider, "authenticated": false}, nil
		} else if err != nil {
			return nil, err
		}
		return map[string]any{"provider": provider, "authenticated": token.AccessToken != "" && !token.Dead && (token.ExpiresAt.IsZero() || time.Now().Before(token.ExpiresAt)), "expires_at": token.ExpiresAt, "unusable": token.Dead, "unusable_reason": token.UnusableReason, "refresh_available": token.RefreshToken != ""}, nil
	case "logout":
		unlock, err := lockCredential(ctx, path)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return map[string]any{"provider": provider, "authenticated": false}, nil
	case "refresh":
		if _, err := a.token(ctx, provider, path, true); err != nil {
			return nil, err
		}
		return map[string]any{"provider": provider, "authenticated": true}, nil
	case "login":
		if cfg.Flow == "client_credentials" {
			if _, err := a.token(ctx, provider, path, true); err != nil {
				return nil, err
			}
			return map[string]any{"authenticated": true}, nil
		}
		state, err := randomSecret()
		if err != nil {
			return nil, err
		}
		verifier, err := randomSecret()
		if err != nil {
			return nil, err
		}
		pending := oauthPending{Provider: provider, State: state, Verifier: verifier, Expires: time.Now().Add(10 * time.Minute)}
		if cfg.Flow == "minimax" {
			return a.miniMaxLogin(ctx, cfg, pending)
		}
		if cfg.Flow == "device" || cfg.Flow == "qwen" {
			var device struct {
				DeviceCode           string `json:"device_code"`
				UserCode             string `json:"user_code"`
				VerificationURI      string `json:"verification_uri"`
				VerificationComplete string `json:"verification_uri_complete"`
				ExpiresIn            int    `json:"expires_in"`
				Interval             int    `json:"interval"`
			}
			values := url.Values{"client_id": {cfg.ClientID}, "scope": {strings.Join(cfg.Scopes, " ")}}
			if cfg.Flow == "qwen" {
				challenge := sha256.Sum256([]byte(verifier))
				values.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
				values.Set("code_challenge_method", "S256")
			}
			if err := formJSON(ctx, cfg.DeviceURL, values, &device); err != nil {
				return nil, err
			}
			if device.DeviceCode == "" || device.VerificationURI == "" || device.ExpiresIn < 1 {
				return nil, errors.New("invalid OAuth device authorization response")
			}
			pending.DeviceCode = device.DeviceCode
			pending.Expires = time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
			pending.Interval = time.Duration(max(device.Interval, 5)) * time.Second
			a.mu.Lock()
			a.pending[state] = pending
			a.mu.Unlock()
			return map[string]any{"provider": provider, "state": state, "user_code": device.UserCode, "verification_uri": device.VerificationURI, "verification_uri_complete": device.VerificationComplete, "expires_at": pending.Expires, "next": "finish"}, nil
		}
		pending.Redirect = cfg.RedirectURL
		if pending.Redirect == "" {
			pending.Redirect = "http://localhost:1455/auth/callback"
		}
		if cfg.Flow == "openrouter" {
			pending.Redirect = strings.TrimRight(pending.Redirect, "/") + "/" + state
		}
		u, err := url.Parse(cfg.AuthorizationURL)
		if err != nil {
			return nil, err
		}
		query := u.Query()
		for k, v := range cfg.AuthParams {
			query.Set(k, v)
		}
		challenge := sha256.Sum256([]byte(verifier))
		query.Set("response_type", "code")
		query.Set("client_id", cfg.ClientID)
		query.Set("redirect_uri", pending.Redirect)
		query.Set("scope", strings.Join(cfg.Scopes, " "))
		query.Set("state", state)
		query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
		query.Set("code_challenge_method", "S256")
		if cfg.Flow == "anthropic" {
			query.Set("code", "true")
		}
		if cfg.Flow == "openrouter" {
			query.Del("response_type")
			query.Del("client_id")
			query.Del("redirect_uri")
			query.Del("scope")
			query.Del("state")
			query.Set("callback_url", pending.Redirect)
		}
		u.RawQuery = query.Encode()
		a.mu.Lock()
		for k, v := range a.pending {
			if time.Now().After(v.Expires) {
				delete(a.pending, k)
			}
		}
		a.pending[state] = pending
		a.mu.Unlock()
		return map[string]any{"provider": provider, "state": state, "authorization_url": u.String(), "redirect_uri": pending.Redirect, "expires_at": pending.Expires, "next": "finish"}, nil
	case "finish":
		if len(args) < 3 {
			return nil, errors.New("finish requires login state and PKCE callback URL (device flow needs state only)")
		}
		a.mu.Lock()
		pending, ok := a.pending[args[2]]
		a.mu.Unlock()
		if !ok || pending.Provider != provider || time.Now().After(pending.Expires) {
			return nil, errors.New("unknown or expired OAuth login state; restart login")
		}
		values := url.Values{}
		if pending.DeviceCode != "" {
			if cfg.Flow == "minimax" {
				values.Set("grant_type", "urn:ietf:params:oauth:grant-type:user_code")
				values.Set("user_code", pending.DeviceCode)
				values.Set("code_verifier", pending.Verifier)
			} else {
				values.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
				values.Set("device_code", pending.DeviceCode)
			}
			if cfg.Flow == "qwen" {
				values.Set("code_verifier", pending.Verifier)
			}
		} else {
			if len(args) != 4 {
				return nil, errors.New("PKCE finish requires callback URL or Anthropic code#state")
			}
			if cfg.Flow == "anthropic" && !strings.Contains(args[3], "://") {
				code, state, ok := strings.Cut(args[3], "#")
				if !ok || code == "" || subtle.ConstantTimeCompare([]byte(state), []byte(pending.State)) != 1 {
					return nil, errors.New("anthropic authorization code state mismatch")
				}
				values.Set("code", code)
				values.Set("state", state)
			} else {
				callback, err := url.Parse(args[3])
				if err != nil {
					return nil, err
				}
				redirect, err := url.Parse(pending.Redirect)
				if err != nil {
					return nil, err
				}
				if callback.Scheme != redirect.Scheme || callback.Host != redirect.Host || callback.Path != redirect.Path || (cfg.Flow != "openrouter" && subtle.ConstantTimeCompare([]byte(callback.Query().Get("state")), []byte(pending.State)) != 1) {
					return nil, errors.New("OAuth callback URI or state mismatch")
				}
				if code := callback.Query().Get("error"); code != "" {
					return nil, &oauthError{Code: code}
				}
				if callback.Query().Get("code") == "" {
					return nil, errors.New("OAuth callback has no code")
				}
				values.Set("code", callback.Query().Get("code"))
				values.Set("state", pending.State)
			}
			values.Set("grant_type", "authorization_code")
			values.Set("redirect_uri", pending.Redirect)
			values.Set("code_verifier", pending.Verifier)
		}
		unlock, err := lockCredential(ctx, path)
		if err != nil {
			return nil, err
		}
		defer unlock()
		for {
			token, err := a.postToken(ctx, cfg, values)
			var authErr *oauthError
			if err != nil && pending.DeviceCode != "" && errors.As(err, &authErr) && (authErr.Code == "authorization_pending" || authErr.Code == "slow_down") {
				if authErr.Code == "slow_down" {
					pending.Interval += 5 * time.Second
				}
				if time.Now().Add(pending.Interval).After(pending.Expires) {
					return nil, errors.New("OAuth device code expired")
				}
				timer := time.NewTimer(pending.Interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-timer.C:
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			if err = writePrivateJSON(path, token); err != nil {
				return nil, err
			}
			a.mu.Lock()
			delete(a.pending, args[2])
			a.mu.Unlock()
			return map[string]any{"provider": provider, "authenticated": true, "expires_at": token.ExpiresAt}, nil
		}
	default:
		return nil, errors.New("unknown model authentication operation")
	}
}
