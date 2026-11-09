package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

type credentialState struct {
	Fingerprint    string               `json:"fingerprint"`
	Dead           bool                 `json:"dead"`
	Until          time.Time            `json:"until"`
	ModelCooldowns map[string]time.Time `json:"model_cooldowns,omitempty"`
	Uses           uint64               `json:"uses"`
}

type credentialPool struct {
	provider string
	cfg      config.ProviderConfig
	auth     *authManager
	path     string
	mu       sync.Mutex
}

type providerTransport struct {
	base         http.RoundTripper
	pool         *credentialPool
	headers      map[string]string
	endpoint     *url.URL
	googleSource func(context.Context) (string, error)
}

func (p *credentialPool) entries() []config.ProviderCredential {
	result := append([]config.ProviderCredential{}, p.cfg.Credentials...)
	if p.cfg.APIKey != "" {
		result = append(result, config.ProviderCredential{ID: "configured", APIKey: p.cfg.APIKey})
	}
	if p.cfg.OAuth.Enabled {
		result = append(result, config.ProviderCredential{ID: "oauth", TokenFile: p.auth.tokenPath(p.provider)})
	}
	return result
}

func (p *credentialPool) acquire(ctx context.Context, model string, excluded map[string]bool) (config.ProviderCredential, string, error) {
	entries := p.entries()
	if len(entries) == 0 {
		return config.ProviderCredential{}, "", nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	unlock, err := lockCredential(ctx, p.path)
	if err != nil {
		return config.ProviderCredential{}, "", err
	}
	defer unlock()
	states := map[string]credentialState{}
	if err = readPrivateJSON(p.path, &states); err != nil && !errors.Is(err, os.ErrNotExist) {
		return config.ProviderCredential{}, "", err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Priority < entries[j].Priority })
	if p.cfg.PoolStrategy == "least_used" {
		sort.SliceStable(entries, func(i, j int) bool { return states[entries[i].ID].Uses < states[entries[j].ID].Uses })
	}
	if p.cfg.PoolStrategy == "random" {
		rand.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	}
	start := 0
	if p.cfg.PoolStrategy == "round_robin" && len(entries) > 0 {
		start = int(states["__cursor"].Uses % uint64(len(entries)))
	}
	var failures []error
	for i := range entries {
		entry := entries[(i+start)%len(entries)]
		if entry.Disabled || excluded[entry.ID] {
			continue
		}
		secret := entry.APIKey
		if entry.TokenFile != "" {
			token, tokenErr := p.auth.token(ctx, p.provider, entry.TokenFile, false)
			if tokenErr != nil {
				failures = append(failures, fmt.Errorf("credential %q: %w", entry.ID, tokenErr))
				continue
			}
			secret = token.AccessToken
		}
		if secret == "" {
			continue
		}
		hash := sha256.Sum256([]byte(secret))
		fingerprint := fmt.Sprintf("%x", hash[:])
		state := states[entry.ID]
		if state.Fingerprint != fingerprint {
			state = credentialState{Fingerprint: fingerprint}
		}
		if state.Dead || time.Now().Before(state.Until) || time.Now().Before(state.ModelCooldowns[model]) {
			continue
		}
		state.Uses++
		states[entry.ID] = state
		if p.cfg.PoolStrategy == "round_robin" {
			cursor := states["__cursor"]
			cursor.Uses++
			states["__cursor"] = cursor
		}
		if err = writePrivateJSON(p.path, states); err != nil {
			return entry, "", err
		}
		return entry, secret, nil
	}
	return config.ProviderCredential{}, "", errors.Join(append([]error{errCredentialsUnavailable}, failures...)...)
}

func (p *credentialPool) mark(ctx context.Context, entry config.ProviderCredential, secret, model string, failure *ProtocolError) error {
	unlock, err := lockCredential(ctx, p.path)
	if err != nil {
		return err
	}
	defer unlock()
	states := map[string]credentialState{}
	if err = readPrivateJSON(p.path, &states); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state := states[entry.ID]
	hash := sha256.Sum256([]byte(secret))
	if state.Fingerprint != fmt.Sprintf("%x", hash[:]) {
		return nil
	}
	code := strings.ToLower(failure.Code)
	state.Dead = code == "invalid_token" || code == "token_revoked" || code == "token_invalidated" || code == "invalid_grant"
	delay := failure.RetryAfter
	if delay <= 0 {
		delay = time.Minute
		if failure.Status == 401 {
			delay = 5 * time.Minute
		}
		if failure.Status == 402 || strings.Contains(code, "quota") || strings.Contains(code, "billing") {
			delay = time.Hour
		}
	}
	if failure.Status == 429 && !strings.Contains(code, "quota") {
		if state.ModelCooldowns == nil {
			state.ModelCooldowns = map[string]time.Time{}
		}
		state.ModelCooldowns[model] = time.Now().Add(delay)
	} else {
		state.Until = time.Now().Add(delay)
	}
	states[entry.ID] = state
	return writePrivateJSON(p.path, states)
}

func (t *providerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.endpoint != nil && (req.URL.Scheme != t.endpoint.Scheme || req.URL.Host != t.endpoint.Host) {
		return nil, errors.New("credential destination does not match configured provider")
	}
	if t.pool != nil && len(t.pool.cfg.Credentials) == 0 && t.pool.cfg.APIKey == "" && !t.pool.cfg.OAuth.Enabled && t.googleSource == nil && len(t.headers) == 0 {
		return t.base.RoundTrip(req)
	}
	getBody := req.GetBody
	if req.Body != nil {
		defer func() { _ = req.Body.Close() }()
	}
	if getBody == nil && req.Body != nil {
		body, err := readBoundedBody(req.Body)
		if err != nil {
			return nil, err
		}
		getBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	}
	excluded := map[string]bool{}
	refreshed := map[string]bool{}
	model, _ := req.Context().Value(modelHTTPKey{}).(string)
	if model == "" {
		model = req.URL.Path
	}
	var err error
	for {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		clone := req.Clone(req.Context())
		clone.Header = req.Header.Clone()
		if getBody != nil {
			clone.Body, err = getBody()
			if err != nil {
				return nil, err
			}
		}
		for k, v := range t.headers {
			clone.Header.Set(k, v)
		}
		var entry config.ProviderCredential
		var secret string
		if t.googleSource != nil {
			secret, err = t.googleSource(req.Context())
		} else if t.pool != nil {
			entry, secret, err = t.pool.acquire(req.Context(), model, excluded)
		}
		if err != nil {
			if clone.Body != nil {
				_ = clone.Body.Close()
			}
			return nil, err
		}
		if secret != "" {
			clone.Header.Del("Authorization")
			clone.Header.Del("api-key")
			clone.Header.Del("x-api-key")
			clone.Header.Del("x-goog-api-key")
			api := effectiveAPI(t.pool.cfg.Type, t.pool.cfg.API)
			switch {
			case api == "anthropic-messages" && !strings.HasPrefix(secret, "sk-ant-oat") && entry.TokenFile == "":
				clone.Header.Set("x-api-key", secret)
			case api == "google-gemini" && entry.TokenFile == "" && t.googleSource == nil:
				clone.Header.Set("x-goog-api-key", secret)
			case normalizeProvider(t.pool.cfg.Type) == "azure" && entry.TokenFile == "":
				clone.Header.Set("api-key", secret)
			default:
				clone.Header.Set("Authorization", "Bearer "+secret)
			}
			if api == "anthropic-messages" && clone.Header.Get("Authorization") != "" {
				clone.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14")
				clone.Header.Set("User-Agent", "claude-cli/2.1.74 (external, cli)")
			}
			if t.pool.cfg.Type == "openai-codex" {
				clone.Header.Set("originator", "saber")
				if account := codexAccountID(secret); account != "" {
					clone.Header.Set("ChatGPT-Account-ID", account)
				}
			}
		}
		resp, err := t.base.RoundTrip(clone)
		if err != nil {
			return nil, err
		}
		if secret == "" || t.googleSource != nil || t.pool == nil || (resp.StatusCode != 401 && resp.StatusCode != 402 && resp.StatusCode != 429 && resp.StatusCode != 403) {
			return resp, nil
		}
		data, readErr := readBoundedBody(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		failure := protocolError(resp, data).(*ProtocolError)
		if resp.StatusCode == 401 && entry.TokenFile != "" && t.pool.cfg.OAuth.Enabled && !refreshed[entry.ID] {
			refreshed[entry.ID] = true
			if _, refreshErr := t.pool.auth.token(req.Context(), t.pool.provider, entry.TokenFile, true); refreshErr == nil {
				continue
			}
		}
		// 403 permission_denied 不是账号轮换信号，不能通过其他账号绕过资源权限。
		if resp.StatusCode == 403 && !strings.Contains(strings.ToLower(failure.Code), "rate") && !strings.Contains(strings.ToLower(failure.Code), "quota") && !strings.Contains(strings.ToLower(failure.Code), "billing") {
			resp.Body = io.NopCloser(bytes.NewReader(data))
			return resp, nil
		}
		if err = t.pool.mark(req.Context(), entry, secret, model, failure); err != nil {
			return nil, errors.Join(failure, err)
		}
		excluded[entry.ID] = true
		if len(excluded) >= len(t.pool.entries()) {
			resp.Body = io.NopCloser(bytes.NewReader(data))
			return resp, nil
		}
	}
}

func newCredentialPool(provider string, cfg config.ProviderConfig, auth *authManager) *credentialPool {
	hash := sha256.Sum256([]byte(provider))
	return &credentialPool{provider: provider, cfg: cfg, auth: auth, path: filepath.Join(auth.dir, fmt.Sprintf("%x.pool.json", hash[:16]))}
}
