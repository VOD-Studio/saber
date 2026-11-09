package model

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

func TestModelOAuth_PKCEStateAndPrivateTokenStorage(t *testing.T) {
	var exchanges atomic.Int32
	var challenge string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/token", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		require.Equal(t, "valid-code", r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		require.Equal(t, challenge, base64.RawURLEncoding.EncodeToString(sum[:]))
		exchanges.Add(1)
		_, err := fmt.Fprint(w, `{"access_token":"private-access","refresh_token":"private-refresh","expires_in":3600}`)
		require.NoError(t, err)
	}))
	defer server.Close()
	cfg := config.DefaultModelRuntimeConfig()
	cfg.StateDir = t.TempDir()
	core := runtimeCore(t, map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: server.URL + "/v1", OAuth: config.OAuthConfig{Enabled: true, Flow: "pkce", ClientID: "test-client", AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token", RedirectURL: "http://localhost:1455/callback"}, Models: map[string]config.ModelConfig{"model": {}}}}, "p.model", cfg)
	login, err := core.AuthCommand(context.Background(), []string{"login", "p"})
	require.NoError(t, err)
	info := login.(map[string]any)
	authorize, err := url.Parse(info["authorization_url"].(string))
	require.NoError(t, err)
	challenge = authorize.Query().Get("code_challenge")
	state := info["state"].(string)
	_, err = core.AuthCommand(context.Background(), []string{"finish", "p", state, "http://localhost:1455/callback?code=valid-code&state=wrong"})
	require.Error(t, err)
	require.Zero(t, exchanges.Load())
	finished, err := core.AuthCommand(context.Background(), []string{"finish", "p", state, "http://localhost:1455/callback?code=valid-code&state=" + url.QueryEscape(state)})
	require.NoError(t, err)
	require.EqualValues(t, 1, exchanges.Load())
	data, err := json.Marshal(finished)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-access")
	require.NotContains(t, string(data), "private-refresh")
	path := core.runtime.auth.tokenPath("p")
	file, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), file.Mode().Perm())
	parent, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), parent.Mode().Perm())
	_, err = core.AuthCommand(context.Background(), []string{"finish", "p", state, "http://localhost:1455/callback?code=valid-code&state=" + state})
	require.Error(t, err)
	require.EqualValues(t, 1, exchanges.Load())
}

func TestModelOAuth_RefreshSerializedAcrossIndependentCores(t *testing.T) {
	var refreshes, completions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			require.NoError(t, r.ParseForm())
			require.Equal(t, "old-refresh", r.Form.Get("refresh_token"))
			refreshes.Add(1)
			_, err := fmt.Fprint(w, `{"access_token":"fresh-access","refresh_token":"new-refresh","expires_in":3600}`)
			require.NoError(t, err)
			return
		}
		require.Equal(t, "Bearer fresh-access", r.Header.Get("Authorization"))
		completions.Add(1)
		require.NoError(t, completionJSON(w, "model", "authenticated"))
	}))
	defer server.Close()
	runtime := config.DefaultModelRuntimeConfig()
	runtime.StateDir = t.TempDir()
	providers := map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: server.URL + "/v1", OAuth: config.OAuthConfig{Enabled: true, Flow: "pkce", ClientID: "client", AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}, Models: map[string]config.ModelConfig{"model": {}}}}
	first := runtimeCore(t, providers, "p.model", runtime)
	second := runtimeCore(t, providers, "p.model", runtime)
	require.NoError(t, writePrivateJSON(first.runtime.auth.tokenPath("p"), oauthToken{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute)}))
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, core := range []*Core{first, second} {
		wg.Add(1)
		go func(core *Core) {
			defer wg.Done()
			client, err := core.GetClient("p.model")
			if err == nil {
				_, err = client.CreateChatCompletion(context.Background(), agent.Request{Model: "p.model", Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hello"}}})
			}
			errors <- err
		}(core)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, refreshes.Load())
	require.EqualValues(t, 2, completions.Load())
	var token oauthToken
	require.NoError(t, readPrivateJSON(first.runtime.auth.tokenPath("p"), &token))
	require.Equal(t, "new-refresh", token.RefreshToken)
	require.False(t, token.Dead)
}

func TestModelOAuth_InterruptedRotationIsNotReplayed(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(503)
		_, err := fmt.Fprint(w, `{"error":"server_error"}`)
		require.NoError(t, err)
	}))
	defer server.Close()
	runtime := config.DefaultModelRuntimeConfig()
	runtime.StateDir = t.TempDir()
	providers := map[string]config.ProviderConfig{"p": {Type: "openai", BaseURL: server.URL + "/v1", OAuth: config.OAuthConfig{Enabled: true, Flow: "pkce", ClientID: "client", AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}, Models: map[string]config.ModelConfig{"model": {}}}}
	core := runtimeCore(t, providers, "p.model", runtime)
	path := core.runtime.auth.tokenPath("p")
	require.NoError(t, writePrivateJSON(path, oauthToken{AccessToken: "old", RefreshToken: "single-use", ExpiresAt: time.Now().Add(-time.Minute)}))
	_, err := core.runtime.auth.token(context.Background(), "p", path, false)
	require.Error(t, err)
	core = runtimeCore(t, providers, "p.model", runtime)
	_, err = core.runtime.auth.token(context.Background(), "p", path, false)
	require.Error(t, err)
	require.EqualValues(t, 1, requests.Load())
}

func TestModelOAuth_UnsafeTokenPathRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"access_token":"secret"}`), 0600))
	link := filepath.Join(dir, "link.json")
	require.NoError(t, os.Symlink(target, link))
	var token oauthToken
	require.Error(t, readPrivateJSON(link, &token))
	require.Error(t, writePrivateJSON(link, oauthToken{AccessToken: "replacement"}))
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Contains(t, string(data), "secret")
	require.NotContains(t, string(data), "replacement")
}
