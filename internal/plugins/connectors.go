package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
	"rua.plus/saber/internal/vault"
)

type connectorManager struct {
	cfg             config.PluginsConfig
	host            toolkit.Host
	mu              sync.Mutex
	credentialLocks sync.Map
	vault           *vault.Store
	sessions        map[string]*mcp.ClientSession
	flows           map[string]*oauthFlow
	closed          bool
	http            *http.Client
}

func newConnectorManager(cfg config.PluginsConfig, host toolkit.Host) *connectorManager {
	return &connectorManager{cfg: cfg, host: host, sessions: map[string]*mcp.ClientSession{}, flows: map[string]*oauthFlow{}, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *connectorManager) attachVault(store *vault.Store) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vault = store
}
func (c *connectorManager) definitions() []toolkit.Definition {
	if len(c.cfg.Connectors) == 0 {
		return nil
	}
	out := []toolkit.Definition{}
	for _, item := range []struct{ name, description string }{
		{"manage_connections", "Manage configured accounts: status, connect, reconnect, continue, cancel, disconnect, accounts, remove_account. Managed app attempts are persistent and never replayed after restart."},
		{"connector_search", "Search currently permitted live connector tools. Does not execute them."},
		{"connector_describe", "Get a currently permitted connector tool's complete schema."},
		{"connector_call", "Execute one permitted connector tool using its exact schema and original authorization."},
	} {
		d := managementDefinition(item.name, item.description)
		d.External = true
		d.Tool.Function.Parameters = map[string]any{"type": "object", "properties": map[string]any{
			"name":      map[string]any{"type": "string", "description": "Administrator-configured connector name"},
			"action":    map[string]any{"type": "string", "enum": []string{"status", "connect", "reconnect", "continue", "cancel", "disconnect", "accounts", "remove_account"}},
			"connector": map[string]any{"type": "string", "description": "Managed app slug, not an account or arbitrary server URL"},
			"query":     map[string]any{"type": "string"}, "tool": map[string]any{"type": "string"},
			"account_alias": map[string]any{"type": "string", "description": "Administrator-configured alias"},
			"arguments":     map[string]any{"type": "object"},
		}, "additionalProperties": false}
		out = append(out, d)
	}
	return out
}
func (c *connectorManager) authorize(ctx context.Context, name, tool string) (config.PluginConnectorConfig, error) {
	cfg, ok := c.cfg.Connectors[name]
	if !ok || !cfg.Enabled {
		return cfg, errors.New("connector not configured or disabled")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return cfg, errors.New("connector requires trusted identity")
	}
	if err := ctx.Err(); err != nil {
		return cfg, err
	}
	if c.host.Check == nil {
		return cfg, errors.New("connector authorization unavailable")
	}
	if err := c.host.Check(ctx, "connector_"+strings.ReplaceAll(name, "-", "_"), true); err != nil {
		return cfg, err
	}
	if tool != "" {
		if !contains(cfg.AllowedTools, tool) {
			return cfg, errors.New("connector tool not permitted by configured allowlist")
		}
		if err := c.host.Check(ctx, "connector_"+strings.ReplaceAll(name, "-", "_")+"__"+strings.ReplaceAll(tool, "-", "_"), true); err != nil {
			return cfg, err
		}
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return cfg, errors.New("connectors closed")
	}
	return cfg, nil
}
func connectorKey(ctx context.Context, name string) string {
	i, _ := chat.IdentityFromContext(ctx)
	return i.UserKey() + ":" + name
}
func (c *connectorManager) credentialLock(ctx context.Context, name string) *sync.Mutex {
	lock, _ := c.credentialLocks.LoadOrStore(connectorKey(ctx, name), &sync.Mutex{})
	return lock.(*sync.Mutex)
}
func (c *connectorManager) token(ctx context.Context, name string, cfg config.PluginConnectorConfig) (string, error) {
	lock := c.credentialLock(ctx, name)
	lock.Lock()
	defer lock.Unlock()
	if cfg.TokenEnv != "" {
		if token := os.Getenv(cfg.TokenEnv); token != "" {
			return token, nil
		}
	}
	c.mu.Lock()
	store := c.vault
	c.mu.Unlock()
	if store == nil {
		return "", errors.New("connector credential unavailable; attach encrypted vault or configure token_env")
	}
	data, err := store.DecodeSecret(ctx, "connector:"+name)
	if err != nil {
		return "", errors.New("connector requires authorization; use manage_connections connect")
	}
	defer clear(data)
	var tokens oauthTokens
	if json.Unmarshal(data, &tokens) != nil {
		return "", errors.New("stored connector authorization invalid")
	}
	if tokens.AccessToken == "" {
		return "", errors.New("connector access token missing")
	}
	if tokens.ExpiresAt != 0 && time.Now().Unix() >= tokens.ExpiresAt-30 {
		if tokens.RefreshToken == "" {
			return "", errors.New("connector authorization expired; reconnect required")
		}
		updated, err := c.refresh(ctx, cfg, tokens)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(updated)
		if err != nil {
			return "", err
		}
		defer clear(data)
		if _, err := c.authorize(ctx, name, ""); err != nil {
			return "", err
		}
		if err := store.EncodeSecret(ctx, "connector:"+name, data); err != nil {
			return "", err
		}
		tokens = updated
	}
	return tokens.AccessToken, nil
}

type authTransport struct {
	token    string
	endpoint string
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, _ := url.Parse(t.endpoint)
	if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
		return nil, errors.New("connector credential destination changed")
	}
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	if t.token != "" {
		copy.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(copy)
}
func (c *connectorManager) connectMCP(ctx context.Context, name string, cfg config.PluginConnectorConfig, token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "saber-connector", Version: "1"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: &http.Client{Timeout: time.Duration(c.cfg.CallTimeoutSeconds) * time.Second, Transport: authTransport{token, cfg.URL}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return client.Connect(ctx, transport, nil)
}
func (c *connectorManager) session(ctx context.Context, name string, cfg config.PluginConnectorConfig) (*mcp.ClientSession, error) {
	key := connectorKey(ctx, name)
	c.mu.Lock()
	old := c.sessions[key]
	c.mu.Unlock()
	if old != nil {
		return old, nil
	}
	token, err := c.token(ctx, name, cfg)
	if err != nil {
		if cfg.TokenEnv == "" && cfg.OAuth.ClientID == "" && cfg.OAuth.AuthorizationURL == "" {
			token = ""
		} else {
			return nil, err
		}
	}
	session, err := c.connectMCP(ctx, name, cfg, token)
	if err != nil {
		return nil, fmt.Errorf("MCP connector initialize failed: %w", err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = session.Close()
		return nil, errors.New("connector closed during connection")
	}
	if existing := c.sessions[key]; existing != nil {
		c.mu.Unlock()
		_ = session.Close()
		return existing, nil
	}
	c.sessions[key] = session
	c.mu.Unlock()
	return session, nil
}
func (c *connectorManager) request(ctx context.Context, name string, cfg config.PluginConnectorConfig, method, path string, body any) (map[string]any, error) {
	token, err := c.token(ctx, name, cfg)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/" + strings.TrimLeft(path, "/")
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	i, _ := chat.IdentityFromContext(ctx)
	req.Header.Set("X-Saber-Session", string(i.Session.Key()))
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(c.cfg.MaxOutputBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > c.cfg.MaxOutputBytes {
		return nil, errors.New("connector response exceeds limit")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("connector returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code, _ := result["code"].(string)
		return nil, fmt.Errorf("connector request failed HTTP %d (%s)", response.StatusCode, code)
	}
	return result, nil
}
func (c *connectorManager) tools(ctx context.Context, name string, cfg config.PluginConnectorConfig) ([]*mcp.Tool, error) {
	session, err := c.session(ctx, name, cfg)
	if err != nil {
		return nil, err
	}
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	out := []*mcp.Tool{}
	for _, tool := range result.Tools {
		if _, err := c.authorize(ctx, name, tool.Name); err == nil {
			out = append(out, tool)
		}
	}
	return out, nil
}
func (c *connectorManager) execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil, errors.New("trusted identity required")
	}
	connector, _ := args["name"].(string)
	action, _ := args["action"].(string)
	tool, _ := args["tool"].(string)
	target, _ := args["connector"].(string)
	if tool == "" {
		tool, _ = args["operation"].(string)
	}
	if name == "manage_connections" && (action == "status" || action == "list" || action == "") {
		out := []map[string]any{}
		names := []string{}
		for candidate := range c.cfg.Connectors {
			names = append(names, candidate)
		}
		sort.Strings(names)
		for _, candidate := range names {
			cfg, err := c.authorize(ctx, candidate, "")
			if err != nil {
				continue
			}
			c.mu.Lock()
			flow := c.flows[connectorKey(ctx, candidate)]
			session := c.sessions[connectorKey(ctx, candidate)]
			c.mu.Unlock()
			entry := map[string]any{"name": candidate, "type": cfg.Type, "connected": session != nil}
			if flow != nil {
				entry["authorization"] = flow.snapshot()
			} else if state, err := c.restoredFlow(ctx, candidate); err == nil {
				entry["authorization"] = state
			}
			out = append(out, entry)
		}
		return out, nil
	}
	cfg, err := c.authorize(ctx, connector, "")
	if err != nil {
		return nil, err
	}
	switch name {
	case "manage_connections":
		if cfg.Type == "managed" && target != "" {
			switch action {
			case "connect", "reconnect":
				return c.managedConnect(ctx, connector, target, cfg, action == "reconnect")
			case "continue", "accounts":
				return c.managedContinue(ctx, connector, target, cfg)
			case "cancel":
				return c.managedCancel(ctx, connector, target)
			case "disconnect", "remove_account":
				return c.managedRemove(ctx, connector, target, cfg)
			}
		}
		switch action {
		case "connect", "reconnect":
			return c.beginOAuth(ctx, connector, cfg)
		case "cancel":
			return c.cancelOAuth(ctx, connector)
		case "disconnect":
			return c.disconnect(ctx, connector, cfg)
		case "continue":
			c.mu.Lock()
			flow := c.flows[connectorKey(ctx, connector)]
			c.mu.Unlock()
			if flow == nil {
				return c.restoredFlow(ctx, connector)
			}
			return flow.snapshot(), nil
		default:
			return nil, errors.New("managed accounts require a recorded app slug; unsupported connection action")
		}
	case "connector_search":
		query, _ := args["query"].(string)
		if query == "" {
			return nil, errors.New("connector search requires query")
		}
		if cfg.Type == "managed" {
			result, err := c.request(ctx, connector, cfg, http.MethodPost, "v1/connectors/search", map[string]any{"queries": []any{map[string]any{"useCase": query}}})
			if err != nil {
				return nil, err
			}
			return c.filterManagedSearch(ctx, connector, cfg, result), nil
		}
		tools, err := c.tools(ctx, connector, cfg)
		if err != nil {
			return nil, err
		}
		out := []map[string]any{}
		for _, candidate := range tools {
			if strings.Contains(strings.ToLower(candidate.Name+" "+candidate.Description), strings.ToLower(query)) {
				out = append(out, map[string]any{"name": candidate.Name, "description": candidate.Description})
			}
		}
		return out, nil
	case "connector_describe":
		if _, err := c.authorize(ctx, connector, tool); err != nil {
			return nil, err
		}
		if cfg.Type == "managed" {
			return c.managedSchema(ctx, connector, tool, cfg)
		}
		tools, err := c.tools(ctx, connector, cfg)
		if err != nil {
			return nil, err
		}
		for _, candidate := range tools {
			if candidate.Name == tool {
				return candidate, nil
			}
		}
		return nil, errors.New("connector tool not found")
	case "connector_call":
		if _, err := c.authorize(ctx, connector, tool); err != nil {
			return nil, err
		}
		values, ok := args["arguments"].(map[string]any)
		if !ok {
			return nil, errors.New("connector arguments must be object")
		}
		if cfg.Type == "managed" {
			schema, err := c.managedSchema(ctx, connector, tool, cfg)
			if err != nil {
				return nil, err
			}
			actualTarget, _ := schema["connector"].(string)
			actualTool, _ := schema["tool"].(string)
			if actualTarget == "" || actualTool == "" || (target != "" && actualTarget != target) {
				return nil, errors.New("managed schema is outside requested app scope")
			}
			call := map[string]any{"connector": actualTarget, "tool": actualTool, "arguments": cleanArguments(values)}
			if alias, _ := args["account_alias"].(string); alias != "" {
				account, ok := cfg.Accounts[alias]
				if !ok {
					return nil, errors.New("account alias not configured")
				}
				call["account"] = account
			}
			result, err := c.request(ctx, connector, cfg, http.MethodPost, "v1/connectors/execute", map[string]any{"call": call, "op": randomID()})
			if err != nil {
				return nil, err
			}
			if err := checkDataUse(cfg, result); err != nil {
				return nil, err
			}
			return result, nil
		}
		session, err := c.session(ctx, connector, cfg)
		if err != nil {
			return nil, err
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: cleanArguments(values)})
		if err != nil {
			c.mu.Lock()
			delete(c.sessions, connectorKey(ctx, connector))
			c.mu.Unlock()
			_ = session.Close()
			return nil, err
		}
		return mcpResult(result)
	default:
		return nil, errors.New("unknown connector tool")
	}
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func checkDataUse(cfg config.PluginConnectorConfig, result map[string]any) error {
	raw, _ := result["result"].(map[string]any)
	use, _ := raw["dataUse"].(map[string]any)
	requires, _ := use["requires"].([]any)
	for _, r := range requires {
		if r == "no_training" && !cfg.NoTraining || r == "zero_retention" && !cfg.ZeroRetention {
			return errors.New("connector result requires a configured no-training/zero-retention model policy; data withheld")
		}
	}
	return nil
}
func (c *connectorManager) disconnect(ctx context.Context, name string, cfg config.PluginConnectorConfig) (any, error) {
	lock := c.credentialLock(ctx, name)
	lock.Lock()
	defer lock.Unlock()
	if _, err := c.cancelOAuth(ctx, name); err != nil {
		return nil, err
	}
	key := connectorKey(ctx, name)
	c.mu.Lock()
	session := c.sessions[key]
	delete(c.sessions, key)
	store := c.vault
	c.mu.Unlock()
	if session != nil {
		if err := session.Close(); err != nil {
			return nil, err
		}
	}
	if store != nil {
		data, err := store.DecodeSecret(ctx, "connector:"+name)
		if err == nil {
			var tokens oauthTokens
			if json.Unmarshal(data, &tokens) != nil {
				clear(data)
				return nil, errors.New("stored connector authorization invalid")
			}
			clear(data)
			endpoint := cfg.OAuth.RevocationURL
			if endpoint == "" {
				endpoint = tokens.RevocationURL
			}
			if endpoint != "" && tokens.AccessToken != "" {
				values := url.Values{"token": {tokens.AccessToken}, "client_id": {tokens.ClientID}}
				if tokens.ClientSecret != "" {
					values.Set("client_secret", tokens.ClientSecret)
				}
				if _, err := c.oauthRequest(ctx, endpoint, values); err != nil {
					return nil, err
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err := store.DeleteSecret(ctx, "connector:"+name); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"disconnected": true}, nil
}
func (c *connectorManager) close() error {
	c.mu.Lock()
	c.closed = true
	sessions := c.sessions
	c.sessions = map[string]*mcp.ClientSession{}
	flows := c.flows
	c.mu.Unlock()
	var errs []error
	for _, f := range flows {
		f.cancel()
	}
	for _, s := range sessions {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}
