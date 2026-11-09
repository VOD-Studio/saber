package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
	"rua.plus/saber/internal/vault"
)

func TestConnectorOAuthPKCECommitAndCanceledAttempt(t *testing.T) {
	var verifierExpected atomic.Value
	verifierExpected.Store("")
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/connectors" {
			if r.Header.Get("Authorization") != "Bearer private-access-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			return
		}
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != verifierExpected.Load().(string) || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "consented" {
			http.Error(w, "invalid grant", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "private-access-token", "refresh_token": "private-refresh-token", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer server.Close()
	cfg := config.DefaultPluginsConfig()
	cfg.Connectors = map[string]config.PluginConnectorConfig{"account": {Enabled: true, Type: "managed", URL: server.URL, OAuth: config.PluginConnectorOAuthConfig{ClientID: "native-test", AuthorizationURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}}}
	host := toolkit.Host{StateDir: t.TempDir(), Check: func(context.Context, string, bool) error { return nil }}
	store, err := vault.New(config.DefaultVaultConfig(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	connectors := newConnectorManager(cfg, host)
	connectors.attachVault(store)
	defer func() { _ = connectors.close() }()
	result, err := connectors.beginOAuth(pluginContext(), "account", cfg.Connectors["account"])
	if err != nil {
		t.Fatal(err)
	}
	flow := result.(map[string]any)
	auth, err := url.Parse(flow["authorization_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	verifierExpected.Store(auth.Query().Get("code_challenge"))
	callback, err := url.Parse(auth.Query().Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	q := callback.Query()
	q.Set("state", auth.Query().Get("state"))
	q.Set("code", "consented")
	callback.RawQuery = q.Encode()
	response, err := http.Get(callback.String())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("callback commit failed HTTP %d", response.StatusCode)
	}
	token, err := connectors.token(pluginContext(), "account", cfg.Connectors["account"])
	if err != nil || token != "private-access-token" {
		t.Fatalf("encrypted credential consumer failed: %q %v", token, err)
	}
	items, err := store.List(pluginContext(), "")
	if err != nil || len(items) != 0 {
		t.Fatalf("tokens exposed as model metadata: %v %v", items, err)
	}
	second, err := connectors.beginOAuth(pluginContext(), "account", cfg.Connectors["account"])
	if err != nil {
		t.Fatal(err)
	}
	secondAuth, _ := url.Parse(second.(map[string]any)["authorization_url"].(string))
	if _, err := connectors.cancelOAuth(pluginContext(), "account"); err != nil {
		t.Fatal(err)
	}
	oldRequests := requests.Load()
	secondCallback, _ := url.Parse(secondAuth.Query().Get("redirect_uri"))
	q = secondCallback.Query()
	q.Set("state", secondAuth.Query().Get("state"))
	q.Set("code", "consented")
	secondCallback.RawQuery = q.Encode()
	client := &http.Client{Timeout: time.Second}
	if response, err := client.Get(secondCallback.String()); err == nil {
		_ = response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("canceled callback committed")
		}
	}
	if requests.Load() != oldRequests {
		t.Fatal("canceled attempt requested a token")
	}
	token, err = connectors.token(pluginContext(), "account", cfg.Connectors["account"])
	if err != nil || token != "private-access-token" {
		t.Fatal("cancel removed already committed credentials")
	}
}

func TestConnectorRevokedGrantBlocksHTTPAndUnknownAccountRemoval(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()
	t.Setenv("SABER_TEST_CONNECTOR_TOKEN", "token")
	cfg := config.DefaultPluginsConfig()
	cfg.Connectors = map[string]config.PluginConnectorConfig{"account": {Enabled: true, Type: "managed", URL: server.URL, TokenEnv: "SABER_TEST_CONNECTOR_TOKEN", AllowedTools: []string{"read_mail"}}}
	host := toolkit.Host{StateDir: t.TempDir(), Check: func(context.Context, string, bool) error { return errors.New("revoked") }}
	connectors := newConnectorManager(cfg, host)
	if _, err := connectors.execute(pluginContext(), "connector_call", map[string]any{"name": "account", "tool": "read_mail", "arguments": map[string]any{}}); err == nil || calls.Load() != 0 {
		t.Fatal("revoked connector dispatched HTTP")
	}
	connectors.host.Check = func(context.Context, string, bool) error { return nil }
	if _, err := connectors.managedRemove(pluginContext(), "account", "gmail", cfg.Connectors["account"]); err == nil || calls.Load() != 0 {
		t.Fatal("unowned managed account removal dispatched")
	}
}

func TestManagedConnectionResumeReadsAccountWithoutRemintingLink(t *testing.T) {
	var connects, reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/connectors/connections":
			connects.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"connector": "gmail", "status": "initiated", "connectionId": "owned-account", "connectUrl": "https://vendor.example/consent"}}})
		case "/v1/connectors/accounts/owned-account":
			reads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"connector": "gmail", "connectionId": "owned-account", "status": "active"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("SABER_TEST_CONNECTOR_TOKEN", "token")
	cfg := config.DefaultPluginsConfig()
	entry := config.PluginConnectorConfig{Enabled: true, Type: "managed", URL: server.URL, TokenEnv: "SABER_TEST_CONNECTOR_TOKEN", AllowedTools: []string{"gmail"}}
	cfg.Connectors = map[string]config.PluginConnectorConfig{"account": entry}
	host := toolkit.Host{StateDir: t.TempDir(), Check: func(context.Context, string, bool) error { return nil }}
	first := newConnectorManager(cfg, host)
	if _, err := first.managedConnect(pluginContext(), "account", "gmail", entry, false); err != nil {
		t.Fatal(err)
	}
	resumed := newConnectorManager(cfg, host)
	result, err := resumed.managedContinue(pluginContext(), "account", "gmail", entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.(managedOperation).Status != "active" || connects.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("resume replayed or failed: %v connects=%d reads=%d", result, connects.Load(), reads.Load())
	}
	if _, err := resumed.managedRead(vaultContextOther(), "account", "gmail"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("operation crossed users: %v", err)
	}
}
func vaultContextOther() context.Context {
	identity, _ := chat.IdentityFromContext(pluginContext())
	identity.SenderID = "bob"
	return chat.WithIdentity(context.Background(), identity)
}
