package config

import "fmt"

// APIConfig 控制持久 Agent HTTP 接口的资源边界。
type APIConfig struct {
	Enabled         bool  `yaml:"enabled"`
	MaxRequestBytes int64 `yaml:"max_request_bytes"`
	PollIntervalMS  int   `yaml:"poll_interval_ms"`
}

// DefaultAPIConfig 返回兼容现有本机入口的默认配置。
func DefaultAPIConfig() APIConfig {
	return APIConfig{Enabled: true, MaxRequestBytes: 8 << 20, PollIntervalMS: 100}
}

// Validate 校验 API 请求和事件轮询边界。
func (c APIConfig) Validate() error {
	if c.MaxRequestBytes < 0 || c.MaxRequestBytes > 64<<20 {
		return fmt.Errorf("api.max_request_bytes 必须在 0..67108864")
	}
	if c.PollIntervalMS < 0 || c.PollIntervalMS > 10000 {
		return fmt.Errorf("api.poll_interval_ms 必须在 0..10000")
	}
	return nil
}
