package model

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// ProviderCaller 由受授权的插件管理器实现；模型只能调用配置指定插件。
type ProviderCaller interface {
	ProviderCall(context.Context, string, string, string, map[string]any) (any, error)
}

type cachedModelResponse struct {
	Response agent.Response
	Expires  time.Time
	Added    time.Time
}
type stickyModelRoute struct {
	Model   string
	Expires time.Time
}

type modelRuntime struct {
	core    *Core
	cfg     config.ModelRuntimeConfig
	auth    *authManager
	mu      sync.Mutex
	cache   map[string]cachedModelResponse
	sticky  map[string]stickyModelRoute
	cursor  map[string]uint64
	plugins ProviderCaller
	advice  map[string]moaAdvice
	retry   *RetryConfigWrapper
}

// ConfigureRuntime 原子安装路由、凭据池与协作策略；不改动已经派发的请求快照。
func (c *Core) ConfigureRuntime(cfg config.ModelRuntimeConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	known := func(id string) bool { _, found := c.globalConfig.GetModelConfig(id); return found }
	for name, route := range cfg.Routes {
		if !known(name) {
			return fmt.Errorf("route source %q is not configured", name)
		}
		for _, target := range append(append([]string{}, route.Targets...), route.Fallbacks...) {
			if !known(target) {
				return fmt.Errorf("route target %q is not configured", target)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("model routing cycle at %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		route := cfg.Routes[id]
		for _, target := range append(append([]string{}, route.Targets...), route.Fallbacks...) {
			if target != id {
				if err := visit(target); err != nil {
					return err
				}
			}
		}
		if modelCfg, ok := c.globalConfig.GetModelConfig(id); ok && modelCfg.Provider == "moa" {
			preset, ok := cfg.MoA.Presets[modelCfg.Model]
			if !cfg.MoA.Enabled || !ok || !preset.Enabled {
				return fmt.Errorf("MoA model %q has no enabled preset", id)
			}
			for _, slot := range append(append([]config.MoASlot{}, preset.References...), preset.Aggregator) {
				if !known(slot.Model) {
					return fmt.Errorf("MoA slot %q is not configured", slot.Model)
				}
				if err := visit(slot.Model); err != nil {
					return err
				}
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for _, info := range c.modelRegistry.ListModels() {
		if err := visit(info.ID); err != nil {
			return err
		}
	}
	stateDir := cfg.StateDir
	if stateDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		stateDir = filepath.Join(dir, "saber")
	}
	providers := make(map[string]config.ProviderConfig, len(c.globalConfig.Providers))
	for name, provider := range c.globalConfig.Providers {
		if provider.Type == "" {
			provider.Type = name
		}
		provider.OAuth = provider.OAuth.WithDefaults(provider.Type)
		if err := provider.OAuth.Validate(); err != nil {
			return fmt.Errorf("provider %s OAuth: %w", name, err)
		}
		providers[name] = provider
	}
	auth := &authManager{dir: filepath.Join(stateDir, "models"), providers: providers, pending: map[string]oauthPending{}}
	rt := &modelRuntime{core: c, cfg: cfg, auth: auth, cache: map[string]cachedModelResponse{}, sticky: map[string]stickyModelRoute{}, cursor: map[string]uint64{}, advice: map[string]moaAdvice{}}
	c.clientsMu.Lock()
	defer c.clientsMu.Unlock()
	if c.runtime != nil {
		c.runtime.mu.Lock()
		rt.plugins = c.runtime.plugins
		c.runtime.mu.Unlock()
	}
	c.runtime = rt
	c.clients = map[string]*Client{}
	return nil
}

// SetPlugins 接入已经实现授权与声明检查的进程外 provider；不直接执行插件进程。
func (c *Core) SetPlugins(plugins ProviderCaller) {
	c.clientsMu.RLock()
	rt := c.runtime
	c.clientsMu.RUnlock()
	if rt != nil {
		rt.mu.Lock()
		rt.plugins = plugins
		rt.mu.Unlock()
	}
}

func (c *Core) providerFor(id string, cfg config.ModelConfig) (string, config.ProviderConfig) {
	if name, _, err := config.ParseModelID(id); err == nil {
		if p, ok := c.globalConfig.Providers[name]; ok {
			return name, p
		}
	}
	if alias, ok := c.globalConfig.Models[id]; ok {
		name := alias.Provider
		if name == "" {
			name, _, _ = config.ParseModelID(c.globalConfig.DefaultModel)
		}
		if p, ok := c.globalConfig.Providers[name]; ok {
			return name, p
		}
	}
	return cfg.Provider, config.ProviderConfig{Type: cfg.Provider, API: cfg.API, BaseURL: cfg.BaseURL, APIKey: cfg.APIKey}
}

func (rt *modelRuntime) configureClient(client *Client, id, providerName string, provider config.ProviderConfig) error {
	provider.OAuth = provider.OAuth.WithDefaults(client.config.Provider)
	client.runtime = rt
	client.modelID = id
	client.provider = provider
	provider.Type = client.config.Provider
	provider.API = client.config.API
	provider.BaseURL = client.config.BaseURL
	// 模型级 key 覆盖提供商时不能悄悄混入另一个账号池。
	if client.config.APIKey != provider.APIKey {
		provider.APIKey = client.config.APIKey
		provider.Credentials = nil
		provider.OAuth.Enabled = false
	}
	if provider.Type == "moa" || provider.Type == "plugin" {
		return nil
	}
	endpoint, err := url.Parse(provider.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil {
		return errors.New("model provider requires trusted HTTP(S) base_url without userinfo")
	}
	pool := newCredentialPool(providerName, provider, rt.auth)
	transport := &providerTransport{base: sharedTransport, pool: pool, headers: provider.Headers, endpoint: endpoint}
	if provider.Google.CredentialsFile != "" {
		source, err := googleTokenSource(provider.Google.CredentialsFile)
		if err != nil {
			return err
		}
		transport.googleSource = source
	}
	if (provider.Type == "vertex" || provider.Type == "google-vertex") && transport.googleSource == nil && !provider.OAuth.Enabled && provider.APIKey == "" {
		return errors.New("vertex requires explicit Google service-account file or OAuth configuration")
	}
	if effectiveAPI(provider.Type, provider.API) == "bedrock-converse" {
		transport.pool = nil
	}
	client.httpClient.Transport = transport
	client.httpClient.CheckRedirect = rejectProviderRedirect
	return nil
}

func (rt *modelRuntime) targets(model string) ([]string, int) {
	route, ok := rt.cfg.Routes[model]
	if !ok {
		return []string{model}, 0
	}
	targets := append([]string{}, route.Targets...)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if sticky := rt.sticky[model]; time.Now().Before(sticky.Expires) {
		for i, target := range targets {
			if target == sticky.Model {
				targets[0], targets[i] = targets[i], targets[0]
				break
			}
		}
	} else if route.Strategy == "round_robin" && len(targets) > 0 {
		index := int(rt.cursor[model] % uint64(len(targets)))
		rt.cursor[model]++
		targets = append(targets[index:], targets[:index]...)
	}
	return append(targets, route.Fallbacks...), route.StickySeconds
}

func (rt *modelRuntime) cacheKey(ctx context.Context, req agent.Request) string {
	if !rt.cfg.Cache.Enabled || req.Stream || len(req.Tools) > 0 || req.Temperature != 0 || req.Purpose == "" {
		return ""
	}
	allowed := false
	for _, purpose := range rt.cfg.Cache.Purposes {
		if purpose == req.Purpose {
			allowed = true
		}
	}
	if !allowed {
		return ""
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || identity.Session.Validate() != nil || identity.SenderID == "" {
		return ""
	}
	data, err := json.Marshal(struct {
		Session chat.SessionID
		Sender  string
		Direct  bool
		Purpose string
		Request agent.Request
	}{identity.Session.Key(), identity.SenderID, identity.Direct, req.Purpose, req})
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:])
}

func cloneResponse(response agent.Response) agent.Response {
	response.ToolCalls = append([]openai.ToolCall{}, response.ToolCalls...)
	response.ResponsesOutput = append([]json.RawMessage{}, response.ResponsesOutput...)
	return response
}

func (rt *modelRuntime) complete(ctx context.Context, source *Client, req agent.Request, handler StreamingChatCompletionHandler) (*agent.Response, error) {
	var stackErr error
	ctx, stackErr = enterModelCall(ctx, source.modelID)
	if stackErr != nil {
		return nil, stackErr
	}
	key := rt.cacheKey(ctx, req)
	if key != "" {
		rt.mu.Lock()
		cached, ok := rt.cache[key]
		rt.mu.Unlock()
		if ok && time.Now().Before(cached.Expires) {
			response := cloneResponse(cached.Response)
			response.Usage = openai.Usage{}
			return &response, nil
		}
	}
	targets, stickySeconds := rt.targets(source.modelID)
	rt.mu.Lock()
	configuredRetry := rt.retry
	rt.mu.Unlock()
	retry := &RetryConfigWrapper{}
	if configuredRetry != nil {
		copy := *configuredRetry
		retry = &copy
	}
	if auxPurpose(req.Purpose) {
		// 摘要与评估失败要快速降级，不能占用主请求的重试预算。
		retry = &RetryConfigWrapper{}
	}
	if len(rt.cfg.Routes[source.modelID].Targets) == 0 {
		targets = append(targets, retry.FallbackModels...)
	}
	var lastErr error
	var result *agent.Response
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target] {
			continue
		}
		seen[target] = true
		if err := ctx.Err(); err != nil {
			return result, err
		}
		client := source
		if target != source.modelID {
			var err error
			client, err = rt.core.GetClient(target)
			if err != nil {
				return nil, err
			}
		}
		request := req
		request.Model = target
		if target != source.modelID {
			request.MaxTokens = client.config.MaxTokens
			request.ReasoningEffort = client.config.ReasoningEffort
			if client.config.Temperature != nil {
				request.Temperature = *client.config.Temperature
			}
		}
		var err error
		_, err = retry.WithRetry(ctx, func() (any, error) {
			var requestErr error
			switch client.config.Provider {
			case "moa":
				callCtx := ctx
				if target != source.modelID {
					callCtx, requestErr = enterModelCall(ctx, target)
				}
				if requestErr == nil {
					result, requestErr = rt.completeMoA(callCtx, client, request, handler)
				}
			case "plugin":
				result, requestErr = rt.completePlugin(ctx, client, request, handler)
			default:
				if target != source.modelID && len(rt.cfg.Routes[target].Targets) > 0 {
					result, requestErr = rt.complete(ctx, client, request, handler)
				} else {
					result, requestErr = rt.actualRequest(ctx, client, request, handler)
				}
			}
			if requestErr != nil && result != nil && (result.Content != "" || len(result.ToolCalls) > 0) {
				requestErr = &partialModelError{err: requestErr}
			}
			return result, requestErr
		})
		if err == nil {
			if stickySeconds > 0 {
				rt.mu.Lock()
				rt.sticky[source.modelID] = stickyModelRoute{Model: target, Expires: time.Now().Add(time.Duration(stickySeconds) * time.Second)}
				rt.mu.Unlock()
			}
			if key != "" && result != nil && len(result.ToolCalls) == 0 {
				now := time.Now()
				rt.mu.Lock()
				if len(rt.cache) >= rt.cfg.Cache.MaxEntries {
					oldest := ""
					var when time.Time
					for cacheKey, value := range rt.cache {
						if oldest == "" || value.Added.Before(when) {
							oldest, when = cacheKey, value.Added
						}
					}
					delete(rt.cache, oldest)
				}
				rt.cache[key] = cachedModelResponse{Response: cloneResponse(*result), Added: now, Expires: now.Add(time.Duration(rt.cfg.Cache.TTLSeconds) * time.Second)}
				rt.mu.Unlock()
			}
			return result, nil
		}
		lastErr = err
		// 已展示或已经返回部分响应时绝不跨模型重放这一轮。
		if result != nil && (result.Content != "" || len(result.ToolCalls) > 0) {
			return result, err
		}
		if !routeFallbackAllowed(err) {
			return result, err
		}
	}
	return result, fmt.Errorf("configured model routes exhausted: %w", lastErr)
}

func routeFallbackAllowed(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, agent.ErrBudgetExhausted) {
		return false
	}
	if errors.Is(err, errCredentialsUnavailable) {
		return true
	}
	var partial *partialModelError
	if errors.As(err, &partial) {
		return false
	}
	var api *openai.APIError
	if errors.As(err, &api) {
		return api.HTTPStatusCode == 401 || api.HTTPStatusCode == 402 || api.HTTPStatusCode == 429 || api.HTTPStatusCode >= 500
	}
	var protocol *ProtocolError
	if errors.As(err, &protocol) {
		return protocol.Status == 401 || protocol.Status == 402 || protocol.Status == 429 || protocol.Status >= 500
	}
	var auth *oauthError
	if errors.As(err, &auth) {
		return auth.Code != "access_denied"
	}
	return RetryableError(err) || errors.Is(err, ErrCircuitOpen)
}

func (rt *modelRuntime) completePlugin(ctx context.Context, client *Client, req agent.Request, handler StreamingChatCompletionHandler) (result *agent.Response, err error) {
	name, _ := client.provider.Extra["plugin"].(string)
	rt.mu.Lock()
	plugins := rt.plugins
	rt.mu.Unlock()
	if plugins == nil || name == "" {
		return nil, errors.New("model plugin provider is not configured or attached")
	}
	gate, gated := agent.AttemptGateFromContext(ctx)
	if gated {
		req, err = gate.Admit(req)
		if err != nil {
			return nil, err
		}
		defer func() {
			response := agent.Response{}
			if result != nil {
				response = *result
			}
			gate.Record(req, response)
		}()
	}
	request := req
	request.Model = client.config.Model
	request.Stream = false
	value, err := plugins.ProviderCall(ctx, name, "model", "complete", map[string]any{"model": client.config.Model, "request": request})
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var response agent.Response
	if err = json.Unmarshal(data, &response); err != nil {
		return nil, errors.New("invalid model plugin response")
	}
	if err = agent.ValidateResponse(response); err != nil {
		return &response, err
	}
	if handler != nil {
		handler.OnChunk(ctx, response.Content)
		notifyNative(ctx, &response, handler)
	}
	return &response, nil
}

type forwardingStream struct {
	target   StreamingChatCompletionHandler
	content  strings.Builder
	thinking strings.Builder
	usage    openai.Usage
	model    string
	calls    []openai.ToolCall
	finish   string
	err      error
}

func (s *forwardingStream) OnChunk(ctx context.Context, text string) {
	s.content.WriteString(text)
	s.target.OnChunk(ctx, text)
}
func (s *forwardingStream) OnComplete(ctx context.Context, content string, usage openai.Usage, model string) {
	s.usage, s.model = usage, model
	s.target.OnComplete(ctx, content, usage, model)
}
func (s *forwardingStream) OnError(_ context.Context, err error) { s.err = err }
func (s *forwardingStream) OnThinkingChunk(ctx context.Context, text string) {
	s.thinking.WriteString(text)
	if target, ok := s.target.(interface{ OnThinkingChunk(context.Context, string) }); ok {
		target.OnThinkingChunk(ctx, text)
	}
}
func (s *forwardingStream) OnToolCallChunk(ctx context.Context, index int, id, name, args string) {
	for len(s.calls) <= index {
		s.calls = append(s.calls, openai.ToolCall{Type: openai.ToolTypeFunction})
	}
	call := &s.calls[index]
	if id != "" {
		call.ID = id
	}
	if name != "" {
		call.Function.Name = name
	}
	call.Function.Arguments += args
	if target, ok := s.target.(StreamingToolCallHandler); ok {
		target.OnToolCallChunk(ctx, index, id, name, args)
	}
}
func (s *forwardingStream) OnFinishReason(ctx context.Context, reason string) {
	s.finish = reason
	if target, ok := s.target.(StreamingToolCallHandler); ok {
		target.OnFinishReason(ctx, reason)
	}
}
func (s *forwardingStream) GetAccumulatedToolCalls() []openai.ToolCall { return s.calls }
func (s *forwardingStream) HasToolCalls() bool                         { return len(s.calls) > 0 }
func (s *forwardingStream) response() agent.Response {
	return agent.Response{Content: s.content.String(), Thinking: s.thinking.String(), Usage: s.usage, Model: s.model, ToolCalls: s.calls, FinishReason: s.finish}
}

func (c *Client) createNative(ctx context.Context, req agent.Request, handler StreamingChatCompletionHandler) (result *agent.Response, err error) {
	defer func() {
		if err != nil || result == nil {
			return
		}
		for index, raw := range result.ResponsesOutput {
			var history nativeHistory
			if decodeErr := json.Unmarshal(raw, &history); decodeErr != nil {
				err = decodeErr
				return
			}
			history.Origin, history.Model = c.responseOrigin(), c.getModelName(req.Model)
			encoded, encodeErr := json.Marshal(history)
			if encodeErr != nil {
				err = encodeErr
				return
			}
			result.ResponsesOutput[index] = encoded
		}
	}()
	switch effectiveAPI(c.config.Provider, c.config.API) {
	case "anthropic-messages":
		return c.createAnthropic(ctx, req, handler)
	case "google-gemini":
		return c.createGemini(ctx, req, handler)
	case "bedrock-converse":
		return c.createBedrock(ctx, req, handler)
	}
	return nil, errors.New("virtual model provider requires ConfigureRuntime")
}

func (c *Client) completeRequest(ctx context.Context, req agent.Request, handler StreamingChatCompletionHandler) (*agent.Response, error) {
	ctx = context.WithValue(ctx, modelHTTPKey{}, c.getModelName(req.Model))
	if c.runtime != nil {
		return c.runtime.complete(ctx, c, req, handler)
	}
	if c.usesNative() {
		return c.createNative(ctx, req, handler)
	}
	if c.usesResponses() {
		return c.createResponse(ctx, req, handler)
	}
	if handler == nil {
		return c.CreateChatCompletion(ctx, req)
	}
	collector := &forwardingStream{target: handler}
	err := c.CreateStreamingChatCompletionWithTools(ctx, req, collector)
	response := collector.response()
	return &response, err
}

func (rt *modelRuntime) actualRequest(ctx context.Context, client *Client, req agent.Request, handler StreamingChatCompletionHandler) (result *agent.Response, err error) {
	gate, gated := agent.AttemptGateFromContext(ctx)
	if gated && effectiveAPI(client.config.Provider, client.config.API) != "google-gemini" {
		req, err = gate.Admit(req)
		if err != nil {
			return nil, err
		}
		defer func() {
			response := agent.Response{}
			if result != nil {
				response = *result
			}
			gate.Record(req, response)
		}()
	}
	copy := *client
	copy.runtime = nil
	if handler == nil {
		return copy.completeRequest(ctx, req, nil)
	}
	collector := &forwardingStream{target: handler}
	result, err = copy.completeRequest(ctx, req, collector)
	if result == nil {
		partial := collector.response()
		result = &partial
	}
	if result.Thinking == "" {
		result.Thinking = collector.thinking.String()
	}
	return result, err
}

type partialModelError struct{ err error }

func (e *partialModelError) Error() string { return e.err.Error() }
func (e *partialModelError) Unwrap() error { return e.err }

// ConfigureRetryPolicy 让即时聊天、辅助请求、协作槽及 Agent 共用宿主的重试与备用模型策略。
func (c *Core) ConfigureRetryPolicy(policy *RetryConfigWrapper) error {
	c.clientsMu.RLock()
	rt := c.runtime
	c.clientsMu.RUnlock()
	if rt == nil {
		return errors.New("ConfigureRuntime must precede ConfigureRetryPolicy")
	}
	if policy == nil {
		return errors.New("model retry policy is required")
	}
	for _, name := range policy.FallbackModels {
		if _, found := c.globalConfig.GetModelConfig(name); !found {
			return fmt.Errorf("fallback model %q is not configured", name)
		}
	}
	copy := *policy
	copy.FallbackModels = append([]string{}, policy.FallbackModels...)
	rt.mu.Lock()
	rt.retry = &copy
	rt.mu.Unlock()
	return nil
}

type modelCallStackKey struct{}

func enterModelCall(ctx context.Context, model string) (context.Context, error) {
	stack, _ := ctx.Value(modelCallStackKey{}).([]string)
	for _, active := range stack {
		if active == model {
			return ctx, fmt.Errorf("model fallback or MoA dependency cycle at %q", model)
		}
	}
	return context.WithValue(ctx, modelCallStackKey{}, append(append([]string{}, stack...), model)), nil
}

type modelHTTPKey struct{}

// auxPurpose 标记允许快速降级的辅助请求类型。
func auxPurpose(purpose string) bool {
	return purpose == "auxiliary"
}
