// Package matrixplatform 是 Matrix 聊天平台接入端：持有 Matrix 账号与出站 adapter，
// 并把平台原生命令规范化为 chat.Message 后交给共享的聊天链路。
package matrixplatform

import (
	"context"
	"errors"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/platform"
)

// Platform 以 bot 身份接入 Matrix 房间与私聊。
type Platform struct {
	cfg      *config.Config
	commands *matrix.CommandService
	media    *matrix.MediaService
}

// New 绑定 Matrix 账号与共享聊天链路。
func New(cfg *config.Config, commands *matrix.CommandService, media *matrix.MediaService) *Platform {
	return &Platform{cfg: cfg, commands: commands, media: media}
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return "matrix" }

// Start 记录共享 handler。Matrix 的登录、同步与 E2EE 仍由 Matrix 客户端管理，
// 搬迁到本平台属于后续阶段。
func (p *Platform) Start(_ context.Context, handler chat.Handler) error {
	if p.commands == nil {
		return errors.New("matrix 平台缺少命令服务，无法启动")
	}
	return nil
}

// Stop 断开平台。当前 Matrix 同步生命周期在装配层，因此这里不改动作。
func (p *Platform) Stop() {}

// ChatAdapter 返回带流式编辑与媒体解析的 adapter，用于展示入站命令的回复。
func (p *Platform) ChatAdapter(handler chat.Handler) *matrix.ChatAdapter {
	return matrix.NewChatAdapter(p.commands, p.media, p.cfg.Matrix.Media, p.cfg.Matrix.StreamEdit.Enabled, handler)
}

// DeliveryAdapter 返回任务投递 adapter，是否原地编辑由 Matrix 展示开关决定。
func (p *Platform) DeliveryAdapter() chat.Adapter {
	return matrix.NewChatAdapter(p.commands, p.media, p.cfg.Matrix.Media, p.cfg.Matrix.StreamEdit.Enabled, nil)
}

// 确保实现满足平台接口与可选的任务投递端口。
var (
	_ platform.Platform     = (*Platform)(nil)
	_ platform.TaskDelivery = (*Platform)(nil)
)
