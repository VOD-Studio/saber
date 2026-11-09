// Package memoryproviders 接入已核实协议的原生记忆服务；作用域及权限由 Saber 决定。
package memoryproviders

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// Backend 是真实服务的生命周期操作，不允许通过模型参数更换账号或服务端点。
type Backend interface {
	Call(context.Context, string, map[string]any) (any, error)
	Close() error
}

// Service 冻结管理员配置并按原生名称分派，只在实际调用时使用网络或本地依赖。
type Service struct {
	host     toolkit.Host
	backends map[string]Backend
}

// Factory 将共享本地权威存储的原生适配器接入分派，不创建第二份记忆数据库。
type Factory func(string, config.MemoryBackendConfig, toolkit.Host) (Backend, error)

// New 初始化已配置适配器；一个适配器无效时不会发布其余半套配置。
func New(cfg config.MemoryBackendsConfig, host toolkit.Host, additional Factory) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Service{host: host, backends: make(map[string]Backend, len(cfg.Backends))}
	for name, c := range cfg.Backends {
		var b Backend
		var err error
		switch c.Type {
		case "hindsight":
			b, err = NewHindsight(c, host)
		case "supermemory":
			b, err = NewSupermemory(c, host)
		case "holographic":
			b, err = NewHolographic(c, host)
		case "retaindb":
			b, err = NewRetainDB(c, host)
		case "byterover":
			b, err = NewByteRover(c, host)
		case "memori":
			b, err = NewMemori(c, host)
		default:
			if additional == nil {
				err = fmt.Errorf("memory backend type %q has no constructor", c.Type)
			} else {
				b, err = additional(name, c, host)
			}
		}
		if err != nil {
			return nil, errors.Join(fmt.Errorf("memory backend %s: %w", name, err), s.Close())
		}
		if b == nil {
			return nil, errors.Join(fmt.Errorf("memory backend %s did not initialize", name), s.Close())
		}
		s.backends[name] = b
	}
	return s, nil
}

// Contains 区分已配置原生适配器和插件，未知名称不会静默改用其他服务。
func (s *Service) Contains(name string) bool {
	_, ok := s.backends[strings.TrimPrefix(name, "native:")]
	return strings.HasPrefix(name, "native:") && ok
}

// ProviderCall 复核当前身份、精确权限和 external 授权后才调用服务，忽略模型的身份字段。
func (s *Service) ProviderCall(ctx context.Context, plugin, kind, operation string, args map[string]any) (any, error) {
	if !s.Contains(plugin) {
		return nil, fmt.Errorf("native memory backend %q is not configured", plugin)
	}
	if kind != "memory" {
		return nil, errors.New("native memory backend only supports memory operations")
	}
	identity, err := ScopedIdentity(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimPrefix(plugin, "native:")
	if s.host.Check == nil {
		return nil, errors.New("memory authorization is unavailable")
	}
	if err := s.host.Check(ctx, "memory_"+name, true); err != nil {
		return nil, err
	}
	input := make(map[string]any, len(args)+3)
	for k, v := range args {
		input[k] = v
	}
	input["identity"] = identity
	input["scope"] = identity
	input["user_id"] = identity
	return s.backends[name].Call(ctx, operation, input)
}

// ScopedIdentity 返回不可碰撞的当前个人或群标识；不从操作参数接受租户。
func ScopedIdentity(ctx context.Context) (string, error) {
	i, ok := chat.IdentityFromContext(ctx)
	if !ok || i.Session.Validate() != nil || i.SenderID == "" {
		return "", errors.New("memory service requires trusted identity")
	}
	kind, id := "group", i.Session.Conversation
	if i.Direct || i.Session.Platform == "terminal" {
		kind, id = "user", i.SenderID
	}
	data, err := json.Marshal([]string{kind, i.Session.Platform, i.Session.Account, id})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "saber-" + hex.EncodeToString(digest[:]), nil
}

// Close 关闭全部适配器并保留每一个清理错误。
func (s *Service) Close() error {
	var errs []error
	for _, b := range s.backends {
		if err := b.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HTTPJSON 是适配器共用的有界 HTTP 传输；端点只来自管理员配置，禁止重定向和凭据 URL。
// httpStatusError 保留真实状态码，供各后端区分「资源不存在」与真实故障。
type httpStatusError struct{ status int }

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("memory service returned HTTP %d", e.status)
}

// HTTPStatusCode 让调用方用 errors.As 读取状态码。
func (e *httpStatusError) HTTPStatusCode() int { return e.status }

func HTTPJSON(ctx context.Context, cfg config.MemoryBackendConfig, method, relative string, body any, headers map[string]string) (json.RawMessage, error) {
	base, err := url.Parse(cfg.Endpoint)
	if err != nil || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return nil, errors.New("memory endpoint is invalid")
	}
	ref, err := url.Parse(relative)
	if err != nil || ref.IsAbs() || ref.Host != "" || strings.HasPrefix(relative, "//") {
		return nil, errors.New("memory request path is invalid")
	}
	target := base.ResolveReference(ref)
	if target.Host != base.Host || target.Scheme != base.Scheme {
		return nil, errors.New("memory request left configured endpoint")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	key := cfg.APIKey
	if cfg.APIKeyEnv != "" {
		key = os.Getenv(cfg.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("memory credential environment %s is not set", cfg.APIKeyEnv)
		}
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	timeout := cfg.TimeoutSeconds
	if timeout == 0 {
		timeout = 30
	}
	client := http.Client{Timeout: time.Duration(timeout) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("memory response exceeds 8 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{status: resp.StatusCode}
	}
	if len(data) == 0 {
		return json.RawMessage(`null`), nil
	}
	if !json.Valid(data) {
		return nil, errors.New("memory service returned invalid JSON")
	}
	return json.RawMessage(data), nil
}
