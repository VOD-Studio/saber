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
type DefaultHandler struct {
	client    *Client          // QQ 客户端
	aiService SimpleAIService  // AI 服务接口
	config    *config.QQConfig // QQ 配置
}

// NewDefaultHandler 创建一个新的默认事件处理器。
//
// 参数:
//   - client: QQ 客户端
//   - aiService: AI 服务
//   - config: QQ 配置
//
// 返回值:
//   - *DefaultHandler: 创建的处理器实例
func NewDefaultHandler(client *Client, aiService SimpleAIService, config *config.QQConfig) *DefaultHandler {
	return &DefaultHandler{
		client:    client,
		aiService: aiService,
		config:    config,
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
// 当收到私聊消息时，如果配置了自动回复，会使用 AI 生成回复。
//
// 参数:
//   - event: WebSocket 事件负载
//   - data: 私聊消息数据
//
// 返回值:
//   - error: 处理过程中发生的错误
func (h *DefaultHandler) HandleC2CMessage(event *dto.WSPayload, data *dto.WSC2CMessageData) error {
	if !h.config.DirectChatAutoReply {
		slog.Debug("私聊自动回复已禁用，忽略消息")
		return nil
	}

	// 检查 AI 服务是否启用
	if !h.aiService.IsEnabled() {
		slog.Warn("AI服务未启用，无法回复私聊消息")
		return nil
	}

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

	// 调用 AI 生成回复
	response, err := h.aiService.ChatWithSystem(
		ctx,
		authorID,
		h.config.SystemPrompt,
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
// 当在群聊中被 @ 时，如果配置了群 @ 回复，会使用 AI 生成回复。
//
// 参数:
//   - event: WebSocket 事件负载
//   - data: 群 @ 消息数据
//
// 返回值:
//   - error: 处理过程中发生的错误
func (h *DefaultHandler) HandleGroupATMessage(event *dto.WSPayload, data *dto.WSGroupATMessageData) error {
	if !h.config.GroupChatMentionReply {
		slog.Debug("群@回复已禁用，忽略消息")
		return nil
	}

	// 检查 AI 服务是否启用
	if !h.aiService.IsEnabled() {
		slog.Warn("AI服务未启用，无法回复群消息")
		return nil
	}

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

	// 调用 AI 生成回复
	response, err := h.aiService.ChatWithSystem(
		ctx,
		authorID,
		h.config.SystemPrompt,
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