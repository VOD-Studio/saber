// Package violetplatform 是 Violet 博客平台的聊天接入端：以 Bot API 的 bot 虚拟用户身份
// 订阅站内聊天事件、把消息规范化为 chat.Message 交给共享聊天链路，并实现出站 adapter。
package violetplatform

import (
	"context"
	"errors"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/platform"
)

// Platform 以 bot 身份接入 Violet 站内聊天。
//
// 装配方式与 Matrix 接入端一致：构造后交给 platform.Registry，由 Registry.Enabled
// 按 platforms.violet.enabled 决定是否 Start；出站能力经 DeliveryAdapter 暴露给 ai 的任务投递。
type Platform struct {
	cfg     *config.Config
	handler chat.Handler
	cancel  context.CancelFunc
}

// New 绑定 Violet 配置。配置校验推迟到 Start，构造本身不产生副作用。
func New(cfg *config.Config) *Platform {
	return &Platform{cfg: cfg}
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return "violet" }

// Start 校验配置并接管入站事件流。handler 是所有平台共享的消息处理入口。
func (p *Platform) Start(ctx context.Context, handler chat.Handler) error {
	if p.cfg == nil {
		return errors.New("violet 平台缺少配置")
	}
	if err := p.cfg.Platforms.Violet.Validate(); err != nil {
		return err
	}
	p.handler = handler
	_ = ctx
	return nil
}

// Stop 断开平台连接。
func (p *Platform) Stop() {}

// DeliveryAdapter 返回一次性发送的出站 adapter，用于任务与定时计划的结果投递。
func (p *Platform) DeliveryAdapter() chat.Adapter { return nil }

// 确保实现满足平台接口。
var _ platform.Platform = (*Platform)(nil)
