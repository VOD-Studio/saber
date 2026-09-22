// Package matrixplatform 是 Matrix 聊天平台接入端：持有 Matrix 账号与出站 adapter，
// 并把平台原生命令规范化为 chat.Message 后交给共享的聊天链路。
package matrixplatform

import (
	"context"
	"errors"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/agent"
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
	// run 是共享聊天链路，由装配层注入，例如 ai.Service.HandleChatModel。
	run ChatRunner
	// handler 是 Start 记录的通用入口，未指定模型时使用。
	handler chat.Handler
}

// ChatRunner 处理一条已规范化的消息并指定本轮模型。
// 与 chat.Handler 的差别只有 modelName，便于平台解析出自己的模型别名后复用核心链路。
type ChatRunner func(ctx context.Context, message chat.Message, adapter chat.Adapter, modelName string) (agent.Result, error)

// New 绑定 Matrix 账号与共享聊天链路。run 为空时平台仍可出站投递，但无法处理入站命令。
func New(cfg *config.Config, commands *matrix.CommandService, media *matrix.MediaService, run ChatRunner) *Platform {
	return &Platform{cfg: cfg, commands: commands, media: media, run: run}
}

// Name 返回平台标识，与 chat.Session.Platform 和配置键一致。
func (p *Platform) Name() string { return "matrix" }

// Start 记录共享 handler。Matrix 的登录、同步与 E2EE 仍由 Matrix 客户端管理，
// 搬迁到本平台属于后续阶段。
func (p *Platform) Start(_ context.Context, handler chat.Handler) error {
	if p.commands == nil {
		return errors.New("matrix 平台缺少命令服务，无法启动")
	}
	p.handler = handler
	return nil
}

// Stop 断开平台。当前 Matrix 同步生命周期在装配层，因此这里不改动作。
func (p *Platform) Stop() {}

// ChatAdapter 返回带流式编辑与媒体解析的 adapter，用于展示入站命令的回复。
func (p *Platform) ChatAdapter(handler chat.Handler) *matrix.ChatAdapter {
	return matrix.NewChatAdapter(p.commands, p.media, p.cfg.Matrix.Media, p.cfg.Matrix.StreamEdit.Enabled, handler)
}

// DeliveryAdapter 返回一次性发送的 adapter，用于任务与定时计划的结果投递。
func (p *Platform) DeliveryAdapter() chat.Adapter {
	return matrix.NewChatAdapter(p.commands, p.media, p.cfg.Matrix.Media, false, nil)
}

// NormalizeCommand 实现 ai.ChatEntrypoint：把平台命令文本规范化为通用消息，
// 返回的 adapter 只做一次性发送，供 !task list 这类只读命令回执。
func (p *Platform) NormalizeCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, text string) (chat.Message, chat.Adapter, error) {
	if p.commands == nil {
		return chat.Message{}, nil, errors.New("matrix 平台缺少命令服务，无法规范化命令")
	}
	adapter := matrix.NewChatAdapter(p.commands, nil, p.cfg.Matrix.Media, false, nil)
	return adapter.Message(ctx, userID, roomID, text), adapter, nil
}

// OutboundAdapter 实现 ai.ChatEntrypoint：交付 Agent 结果时复用带流式编辑与媒体解析的 adapter。
func (p *Platform) OutboundAdapter() chat.Adapter { return p.ChatAdapter(nil) }

// EventID 实现 ai.ChatEntrypoint：Matrix 的事件标识来自入站上下文。
func (p *Platform) EventID(ctx context.Context) string { return string(matrix.GetEventID(ctx)) }

// Session 实现 ai.ChatEntrypoint：把房间与线程解析成带账号作用域的通用会话。
// 命令服务缺失时返回空会话，由调用方的校验拒绝。
func (p *Platform) Session(ctx context.Context, roomID id.RoomID) chat.Session {
	if p.commands == nil {
		return chat.Session{}
	}
	return p.ChatAdapter(nil).Session(ctx, roomID)
}

// HandleCommand 实现 ai.ChatEntrypoint：规范化平台命令后按指定模型走共享聊天链路。
// modelName 为空时退回 Start 记录的通用入口，由其选择默认模型。
func (p *Platform) HandleCommand(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string, modelName string) error {
	if p.run == nil || p.handler == nil {
		return errors.New("matrix 平台尚未接入聊天链路")
	}
	if modelName == "" {
		return p.ChatAdapter(p.handler).Handle(ctx, userID, roomID, args)
	}
	run := p.run
	adapter := p.ChatAdapter(func(ctx context.Context, message chat.Message, reply chat.Adapter) (agent.Result, error) {
		return run(ctx, message, reply, modelName)
	})
	return adapter.Handle(ctx, userID, roomID, args)
}

// 确保实现满足平台接口与可选的任务投递端口。
var (
	_ platform.Platform     = (*Platform)(nil)
	_ platform.TaskDelivery = (*Platform)(nil)
)
