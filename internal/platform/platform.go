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

// TaskDelivery 是可选端口：能接收任务与定时计划结果的接入端实现它。
//
// 不把它并进 Platform 是因为并非每个入口都有「把一条长任务结果投回会话」的语义，
// 强制实现只会让接入端编出一个没人调用的方法。
type TaskDelivery interface {
	// DeliveryAdapter 返回一次性投递用的出站 adapter。
	DeliveryAdapter() chat.Adapter
}

// Registry 管理已注册的平台实现，并保留注册顺序作为启动顺序。
type Registry struct {
	platforms map[string]Platform
	order     []string
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
	r.order = append(r.order, name)
	slog.Info("平台已注册", "platform", name)
}

// Enabled 按注册顺序返回配置中应启动的平台列表。
//
// 启停一律由 config.Config.PlatformEnabled 决定，注册表本身不再对任何平台名
// 做特殊判断：接入新平台只需登记配置开关，不必改动这里。
// 调用方负责依次 Start 和统一 Stop。
func (r *Registry) Enabled(cfg *config.Config) []Platform {
	var result []Platform
	for _, name := range r.order {
		if cfg.PlatformEnabled(name) {
			result = append(result, r.platforms[name])
		}
	}
	return result
}

// Lookup 按名称查找已注册的平台。
func (r *Registry) Lookup(name string) (Platform, bool) {
	p, ok := r.platforms[name]
	return p, ok
}
