// Package platform 定义可插拔聊天平台接入端的统一接口与注册机制。
package platform

import (
	"context"
	"fmt"
	"log/slog"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// Platform 一个可插拔聊天平台接入端。
// 每个实现负责自己的连接生命周期、消息规范化（入站）和 chat.Adapter（出站）。
type Platform interface {
	// Name 返回平台标识，用于 chat.Session.Platform 和配置键。
	Name() string
	// Start 连接平台并开始接收消息。handler 是所有平台共享的消息处理入口。
	Start(ctx context.Context, handler chat.Handler) error
	// Stop 优雅断开连接。
	Stop()
}

// Registry 管理已注册的平台实现。
type Registry struct {
	platforms map[string]Platform
}

// NewRegistry 创建空的平台注册表。
func NewRegistry() *Registry {
	return &Registry{platforms: make(map[string]Platform)}
}

// Register 注册一个平台实现。同名平台会触发 panic，避免运行时静默覆盖。
func (r *Registry) Register(p Platform) {
	name := p.Name()
	if _, ok := r.platforms[name]; ok {
		panic(fmt.Sprintf("platform %q is already registered", name))
	}
	r.platforms[name] = p
	slog.Info("平台已注册", "platform", name)
}

// Enabled 根据配置返回当前应启动的平台列表。
// 调用方负责依次 Start 和统一 Stop。
func (r *Registry) Enabled(cfg *config.Config) []Platform {
	var result []Platform
	// Terminal 平台始终启用——HTTP server 入口不与任何外部平台绑定。
	if p, ok := r.platforms["terminal"]; ok {
		result = append(result, p)
	}
	// Matrix 平台通过显式开关控制。
	if cfg.Matrix.Enabled {
		if p, ok := r.platforms["matrix"]; ok {
			result = append(result, p)
		}
	}
	return result
}

// Lookup 按名称查找已注册的平台。
func (r *Registry) Lookup(name string) (Platform, bool) {
	p, ok := r.platforms[name]
	return p, ok
}
