// Package media 提供 Go 原生媒体协议、入站转录和实时语音，所有副作用沿宿主权限执行。
package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// Service 持有可信供应商配置与取消域；不保存或返回供应商密钥。
type Service struct {
	cfg          config.MediaToolsConfig
	host         toolkit.Host
	client       *http.Client
	download     *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed       bool
	streams      map[*TranscriptionStream]struct{}
	speechModels map[string]string
}

// New 仅初始化已启用的能力，不自动安装模型、连接远端或开启麦克风。
func New(cfg config.MediaToolsConfig, host toolkit.Host) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{cfg: cfg, host: host, ctx: ctx, cancel: cancel, streams: make(map[*TranscriptionStream]struct{})}
	s.client = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 0 && (req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme) {
			return errors.New("media provider cross-origin redirect refused")
		}
		if len(via) > 5 {
			return errors.New("media redirect limit")
		}
		return nil
	}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("media host has no addresses")
		}
		for _, ip := range ips {
			if !s.allowedHost(host) && !publicIP(ip.IP) {
				return nil, errors.New("media private address refused; configure allowed_media_hosts only for trusted media servers")
			}
		}
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	s.download = &http.Client{Transport: transport, Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("media redirect limit")
		}
		return validateURL(req.URL)
	}}
	return s, nil
}

func publicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	if v := ip.To4(); v != nil {
		if v[0] >= 224 {
			return false
		}
		switch {
		case v[0] == 100 && v[1] >= 64 && v[1] <= 127: // 100.64.0.0/10 运营商级 NAT
			return false
		case v[0] == 198 && (v[1] == 18 || v[1] == 19): // 198.18.0.0/15 基准测试
			return false
		case v[0] == 192 && v[1] == 0 && (v[2] == 0 || v[2] == 2): // 192.0.0.0/24、192.0.2.0/24 特殊用途
			return false
		case v[0] == 198 && v[1] == 51 && v[2] == 100: // 198.51.100.0/24 TEST-NET-2
			return false
		case v[0] == 203 && v[1] == 0 && v[2] == 113: // 203.0.113.0/24 TEST-NET-3
			return false
		}
		return true
	}
	// 2001:db8::/32 仅用于文档示例，不能作为真实下载源。
	return len(ip) != 16 || ip[0] != 0x20 || ip[1] != 0x01 || ip[2] != 0x0d || ip[3] != 0xb8
}
func (s *Service) allowedHost(host string) bool {
	for _, h := range s.cfg.AllowedMediaHosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}
func validateURL(u *url.URL) error {
	if u == nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("media requires HTTP(S) URL without credentials")
	}
	return nil
}
func external(p config.MediaProviderConfig) bool {
	switch p.Provider {
	case "command", "whisper_cpp", "piper", "espeak":
		return false
	default:
		return true
	}
}
func (s *Service) authorize(ctx context.Context, name string, p config.MediaProviderConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("media service closed")
	}
	if !s.cfg.Enabled || !p.Enabled {
		return fmt.Errorf("media capability %s disabled; configure media_tools", name)
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("media requires trusted chat identity")
	}
	if s.host.Check == nil {
		return errors.New("media authorization unavailable")
	}
	if err := s.host.Check(ctx, name, external(p)); err != nil {
		return err
	}
	if !external(p) {
		return s.host.Check(ctx, "exec", false)
	}
	return nil
}
func (s *Service) scope(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.TimeoutSeconds)*time.Second)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}
func providerRuntime(p config.MediaProviderConfig) (string, string, error) {
	base := strings.TrimRight(p.BaseURL, "/")
	key := p.APIKey
	if key == "" && p.APIKeyEnv != "" {
		key = os.Getenv(p.APIKeyEnv)
	}
	defaults := map[string][2]string{"openai": {"https://api.openai.com/v1", "OPENAI_API_KEY"}, "groq": {"https://api.groq.com/openai/v1", "GROQ_API_KEY"}, "mistral": {"https://api.mistral.ai/v1", "MISTRAL_API_KEY"}, "xai": {"https://api.x.ai/v1", "XAI_API_KEY"}, "elevenlabs": {"https://api.elevenlabs.io/v1", "ELEVENLABS_API_KEY"}, "deepinfra": {"https://api.deepinfra.com/v1/openai", "DEEPINFRA_API_KEY"}, "minimax": {"https://api.minimax.io/v1", "MINIMAX_API_KEY"}, "gemini": {"https://generativelanguage.googleapis.com/v1beta", "GEMINI_API_KEY"}, "fal": {"https://queue.fal.run", "FAL_KEY"}, "openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"}}
	if d, ok := defaults[p.Provider]; ok {
		if base == "" {
			base = d[0]
		}
		if key == "" && p.APIKeyEnv == "" {
			key = os.Getenv(d[1])
		}
	}
	if base == "" {
		return "", "", errors.New("media provider base_url required")
	}
	if key == "" && p.BaseURL == "" {
		return "", "", fmt.Errorf("media %s credentials missing: configure api_key or api_key_env", p.Provider)
	}
	return base, key, nil
}
func (s *Service) request(ctx context.Context, p config.MediaProviderConfig, method, target, contentType string, body io.Reader) ([]byte, string, error) {
	_, key, err := providerRuntime(p)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, "", err
	}
	if key != "" {
		switch p.Provider {
		case "elevenlabs":
			req.Header.Set("xi-api-key", key)
		case "gemini":
			req.Header.Set("x-goog-api-key", key)
		case "fal":
			req.Header.Set("Authorization", "Key "+key)
		default:
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("media %s transport failed: %w", p.Provider, redactError(err, key))
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := boundedRead(res.Body, s.cfg.MaxBytes)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		detail := strings.TrimSpace(string(raw[:min(len(raw), 1200)]))
		if key != "" {
			detail = strings.ReplaceAll(detail, key, "[redacted]")
		}
		return nil, "", fmt.Errorf("media %s HTTP %d: %s", p.Provider, res.StatusCode, detail)
	}
	return raw, res.Header.Get("Content-Type"), nil
}
func redactError(err error, key string) error {
	if key == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "[redacted]"))
}
func boundedRead(r io.Reader, limit int64) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("media payload exceeds max_bytes")
	}
	return b, nil
}
func (s *Service) jsonRequest(ctx context.Context, p config.MediaProviderConfig, method, target string, payload any) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	raw, _, err := s.request(ctx, p, method, target, "application/json", body)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("media malformed provider JSON: %w", err)
	}
	if v, ok := result["error"]; ok && v != nil {
		return nil, fmt.Errorf("media provider error: %v", v)
	}
	return result, nil
}
func (s *Service) load(ctx context.Context, a chat.Attachment) ([]byte, string, error) {
	if err := a.Validate(); err != nil {
		return nil, "", err
	}
	if len(a.Data) > 0 {
		if int64(len(a.Data)) > s.cfg.MaxBytes {
			return nil, "", errors.New("media attachment exceeds max_bytes")
		}
		return a.Data, a.MIMEType, nil
	}
	if strings.HasPrefix(a.URL, "data:") {
		parts := strings.SplitN(a.URL, ",", 2)
		if len(parts) != 2 || !strings.HasSuffix(parts[0], ";base64") {
			return nil, "", errors.New("invalid media data URL")
		}
		if int64(base64.StdEncoding.DecodedLen(len(parts[1]))) > s.cfg.MaxBytes {
			return nil, "", errors.New("media data URL exceeds max_bytes")
		}
		b, e := base64.StdEncoding.DecodeString(parts[1])
		return b, strings.TrimSuffix(strings.TrimPrefix(parts[0], "data:"), ";base64"), e
	}
	if s.host.Check == nil {
		return nil, "", errors.New("media download authorization unavailable")
	}
	if err := s.host.Check(ctx, "media_download", true); err != nil {
		return nil, "", err
	}
	u, e := url.Parse(a.URL)
	if e != nil {
		return nil, "", e
	}
	if e = validateURL(u); e != nil {
		return nil, "", e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, "", e
	}
	res, e := s.download.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return nil, "", fmt.Errorf("media download HTTP %d", res.StatusCode)
	}
	b, e := boundedRead(res.Body, s.cfg.MaxBytes)
	return b, res.Header.Get("Content-Type"), e
}
func attachment(kind, mime string, data []byte) chat.Attachment {
	return chat.Attachment{Kind: kind, MIMEType: mime, Data: data, Size: int64(len(data)), URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}
}
func encode64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }
func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}
func str(m map[string]any, key string) string            { v, _ := m[key].(string); return v }
func object(m map[string]any, key string) map[string]any { v, _ := m[key].(map[string]any); return v }
func list(m map[string]any, key string) []any            { v, _ := m[key].([]any); return v }
func model(p config.MediaProviderConfig, fallback string) string {
	if p.Model != "" {
		return p.Model
	}
	return fallback
}
func options(p config.MediaProviderConfig) map[string]any {
	m := make(map[string]any, len(p.Options))
	for k, v := range p.Options {
		m[k] = v
	}
	return m
}
func tool(name, description string, props map[string]any, required []string, p config.MediaProviderConfig) toolkit.Definition {
	d := toolkit.Definition{Permission: name, External: external(p), Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}}}
	if !external(p) {
		d.Requires = []string{"exec"}
	}
	if p.Provider == "plugin" {
		d.Requires = append(d.Requires, p.PluginTool)
	}
	return d
}
func textProp() map[string]any { return map[string]any{"type": "string"} }

// Close 取消网络请求、麦克风和实时连接；重启后不重放已派发的媒体生成。
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	streams := make([]*TranscriptionStream, 0, len(s.streams))
	for st := range s.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	var errs []error
	for _, st := range streams {
		errs = append(errs, st.Close())
	}
	s.client.CloseIdleConnections()
	s.download.CloseIdleConnections()
	return errors.Join(errs...)
}
