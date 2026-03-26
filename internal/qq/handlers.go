// Package qq 提供 QQ 机器人的适配器实现。
//
// Handler 处理 QQ 事件。
package qq

import (
	"context"
	"log/slog"
	"strings"

	"github.com/tencent-connect/botgo/dto"

	"rua.plus/saber/internal/config"
)

// DefaultHandler 是默认的事件处理器。
//
// 处理 QQ 机器人的各类事件，包括私聊消息和群 @ 消息。
// 支持命令解析和分发，以及 AI 自动回复。
type DefaultHandler struct {
	client     *Client          // QQ 客户端
	aiService  SimpleAIService  // AI 服务接口（可选）
	aiConfig   *config.AIConfig // AI 配置（复用全局配置）
	registry   *CommandRegistry // 命令注册表
	contextMgr *ContextManager  // 上下文管理器（可选）
	buildInfo  *BuildInfo       // 构建信息（可选）
}

// NewDefaultHandler 创建一个新的默认事件处理器。
//
// 参数:
//   - client: QQ 客户端
//   - aiService: AI 服务（可为 nil）
//   - aiConfig: AI 配置
//   - registry: 命令注册表
//   - contextMgr: 上下文管理器（可为 nil）
//   - buildInfo: 构建信息（可为 nil）
//
// 返回值:
//   - *DefaultHandler: 创建的处理器实例
func NewDefaultHandler(client *Client, aiService SimpleAIService, aiConfig *config.AIConfig, registry *CommandRegistry, contextMgr *ContextManager, buildInfo *BuildInfo) *DefaultHandler {
	return &DefaultHandler{
		client:     client,
		aiService:  aiService,
		aiConfig:   aiConfig,
		registry:   registry,
		contextMgr: contextMgr,
		buildInfo:  buildInfo,
	}
}

// HandleReady 处理 Ready 事件。
//
// 当机器人连接成功时会收到此事件。
//
// 参数:
//   - event: WebSocket 事件负载
//   - data: Ready 数据
func (h *DefaultHandler) HandleReady(event *dto.WSPayload, data *dto.WSReadyData) {
	slog.Info("QQ机器人连接成功",
		"version", data.Version,
		"session_id", data.SessionID,
		"shard", data.Shard)
}

// HandleC2CMessage 处理私聊消息事件。
//
// 处理流程：
// 1. 首先尝试解析为命令，如果是命令则分发处理
// 2. 如果不是命令且启用了自动回复，使用 AI 生成回复
//
// 参数:
//   - event: WebSocket 事件负载
//   - data: 私聊消息数据
//
// 返回值:
//   - error: 处理过程中发生的错误
func (h *DefaultHandler) HandleC2CMessage(event *dto.WSPayload, data *dto.WSC2CMessageData) error {
	// 提取消息内容
	content := extractMessageContent(data.Content)
	if content == "" {
		return nil
	}

	authorID := data.Author.ID
	slog.Debug("收到私聊消息",
		"author", authorID,
		"content", content)

	// 创建上下文
	ctx := context.Background()

	// 尝试解析为命令
	if h.registry != nil {
		if parsed := h.registry.Parse(content); parsed != nil {
			slog.Debug("解析为命令", "command", parsed.Name)
			sender := &c2cSender{client: h.client, ctx: ctx}
			found, err := h.registry.Dispatch(ctx, authorID, "", parsed, sender)
			if err != nil {
				slog.Error("命令处理失败", "command", parsed.Name, "error", err)
				return err
			}
			if found {
				return nil
			}
		}
	}

	// 非命令，检查是否启用自动回复
	if !h.aiConfig.DirectChatAutoReply {
		slog.Debug("私聊自动回复已禁用，忽略消息")
		return nil
	}

	// 检查 AI 服务是否启用
	if h.aiService == nil || !h.aiService.IsEnabled() {
		slog.Warn("AI服务未启用，无法回复私聊消息")
		return nil
	}

	// 调用 AI 生成回复
	response, err := h.aiService.ChatWithSystem(
		ctx,
		authorID,
		h.aiConfig.SystemPrompt,
		content,
	)
	if err != nil {
		slog.Error("AI生成回复失败", "error", err)
		return err
	}

	// 发送回复
	if err := SendMessage(ctx, h.client.GetAPI(), authorID, response); err != nil {
		slog.Error("发送私聊回复失败", "error", err)
		return err
	}

	slog.Debug("私聊回复成功",
		"author", authorID,
		"response_length", len(response))

	return nil
}

// HandleGroupATMessage 处理群 @ 消息事件。
//
// 处理流程：
// 1. 首先尝试解析为命令，如果是命令则分发处理
// 2. 如果不是命令且启用了群@回复，使用 AI 生成回复
//
// 参数:
//   - event: WebSocket 事件负载
//   - data: 群 @ 消息数据
//
// 返回值:
//   - error: 处理过程中发生的错误
func (h *DefaultHandler) HandleGroupATMessage(event *dto.WSPayload, data *dto.WSGroupATMessageData) error {
	// 提取消息内容（去除 @ 部分）
	content := extractMessageContent(data.Content)
	content = removeMention(content)
	if content == "" {
		return nil
	}

	groupID := data.GroupID
	authorID := data.Author.ID
	slog.Debug("收到群@消息",
		"group_id", groupID,
		"author", authorID,
		"content", content)

	// 创建上下文
	ctx := context.Background()

	// 尝试解析为命令
	if h.registry != nil {
		if parsed := h.registry.Parse(content); parsed != nil {
			slog.Debug("解析为命令", "command", parsed.Name)
			sender := &groupSender{client: h.client, groupID: groupID}
			found, err := h.registry.Dispatch(ctx, authorID, groupID, parsed, sender)
			if err != nil {
				slog.Error("命令处理失败", "command", parsed.Name, "error", err)
				return err
			}
			if found {
				return nil
			}
		}
	}

	// 非命令，检查是否启用群@回复
	if !h.aiConfig.GroupChatMentionReply {
		slog.Debug("群@回复已禁用，忽略消息")
		return nil
	}

	// 检查 AI 服务是否启用
	if h.aiService == nil || !h.aiService.IsEnabled() {
		slog.Warn("AI服务未启用，无法回复群消息")
		return nil
	}

	// 调用 AI 生成回复
	response, err := h.aiService.ChatWithSystem(
		ctx,
		authorID,
		h.aiConfig.SystemPrompt,
		content,
	)
	if err != nil {
		slog.Error("AI生成回复失败", "error", err)
		return err
	}

	// 发送回复
	if err := SendGroupMessage(ctx, h.client.GetAPI(), groupID, response); err != nil {
		slog.Error("发送群回复失败", "error", err)
		return err
	}

	slog.Debug("群回复成功",
		"group_id", groupID,
		"response_length", len(response))

	return nil
}

// extractMessageContent 从消息内容中提取文本。
//
// 处理 QQ 的消息格式，去除特殊标记。
//
// 参数:
//   - content: 原始消息内容
//
// 返回值:
//   - string: 提取的文本内容
func extractMessageContent(content string) string {
	// QQ 消息可能包含特殊格式，这里做简单处理
	content = strings.TrimSpace(content)
	return content
}

// removeMention 去除消息中的 @ 提及。
//
// 参数:
//   - content: 原始消息内容
//
// 返回值:
//   - string: 去除 @ 后的内容
func removeMention(content string) string {
	// 移除 <@!数字> 格式的提及
	for {
		start := strings.Index(content, "<@!")
		if start == -1 {
			break
		}
		end := strings.Index(content[start:], ">")
		if end == -1 {
			break
		}
		content = content[:start] + content[start+end+1:]
	}
	return strings.TrimSpace(content)
}

// --- CommandSender 实现 ---

// c2cSender 实现私聊的 CommandSender 接口。
type c2cSender struct {
	client *Client
	ctx    context.Context
}

// Send 发送私聊消息。
func (s *c2cSender) Send(ctx context.Context, userID, groupID string, message string) error {
	return SendMessage(ctx, s.client.GetAPI(), userID, message)
}

// groupSender 实现群聊的 CommandSender 接口。
type groupSender struct {
	client  *Client
	groupID string
}

// Send 发送群聊消息。
func (s *groupSender) Send(ctx context.Context, userID, groupID string, message string) error {
	return SendGroupMessage(ctx, s.client.GetAPI(), s.groupID, message)
}