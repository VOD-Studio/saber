// Package qq 提供 QQ 机器人的适配器实现。
//
// Message 提供消息发送工具函数。
package qq

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/dto"
)

// SendMessage 发送文本消息到私聊。
//
// 该方法使用 botgo API 向指定用户发送私聊消息。
// 注意：私聊消息回复有效期为 60 分钟。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - api: botgo API 实例
//   - openid: 用户 OpenID
//   - content: 消息内容（纯文本）
//
// 返回值:
//   - error: 发送过程中发生的错误
//
// 错误情况：
//   - 消息内容为空
//   - API 调用失败
//   - 超出回复有效期（60分钟）
//
// 使用示例:
//
//	err := SendMessage(ctx, api, "user_openid", "你好，我是 Saber 机器人！")
func SendMessage(ctx context.Context, api openapi.OpenAPI, openid, content string) error {
	if content == "" {
		return fmt.Errorf("消息内容不能为空")
	}

	// 创建消息请求
	msg := &dto.MessageToCreate{
		Content: content,
		MsgType: 0, // 文本消息
	}

	// 发送消息
	result, err := api.PostC2CMessage(ctx, openid, msg)
	if err != nil {
		return fmt.Errorf("发送私聊消息失败: %w", err)
	}

	slog.Debug("私聊消息发送成功",
		"openid", openid,
		"msg_id", result.ID,
		"content_length", len(content))

	return nil
}

// SendGroupMessage 发送文本消息到群聊。
//
// 该方法使用 botgo API 向指定群发送消息。
// 注意：群消息回复有效期为 5 分钟。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - api: botgo API 实例
//   - groupOpenid: 群 OpenID
//   - content: 消息内容（纯文本）
//
// 返回值:
//   - error: 发送过程中发生的错误
//
// 错误情况：
//   - 消息内容为空
//   - API 调用失败
//   - 超出回复有效期（5分钟）
//
// 使用示例:
//
//	err := SendGroupMessage(ctx, api, "group_openid", "大家好！")
func SendGroupMessage(ctx context.Context, api openapi.OpenAPI, groupOpenid, content string) error {
	if content == "" {
		return fmt.Errorf("消息内容不能为空")
	}

	// 创建消息请求
	msg := &dto.MessageToCreate{
		Content: content,
		MsgType: 0, // 文本消息
	}

	// 发送消息
	result, err := api.PostGroupMessage(ctx, groupOpenid, msg)
	if err != nil {
		return fmt.Errorf("发送群消息失败: %w", err)
	}

	slog.Debug("群消息发送成功",
		"group_openid", groupOpenid,
		"msg_id", result.ID,
		"content_length", len(content))

	return nil
}

// SendReplyMessage 发送私聊回复消息。
//
// 该方法使用 botgo API 回复指定私聊消息。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - api: botgo API 实例
//   - openid: 用户 OpenID
//   - msgID: 要回复的消息 ID
//   - content: 回复内容
//
// 返回值:
//   - error: 发送过程中发生的错误
func SendReplyMessage(ctx context.Context, api openapi.OpenAPI, openid, msgID, content string) error {
	if content == "" {
		return fmt.Errorf("消息内容不能为空")
	}

	// 创建消息请求，包含引用消息
	msg := &dto.MessageToCreate{
		Content: content,
		MsgType: 0, // 文本消息
		MsgID:   msgID,
	}

	// 发送消息
	result, err := api.PostC2CMessage(ctx, openid, msg)
	if err != nil {
		return fmt.Errorf("发送私聊回复失败: %w", err)
	}

	slog.Debug("私聊回复发送成功",
		"openid", openid,
		"msg_id", result.ID,
		"reply_to", msgID)

	return nil
}

// SendGroupReplyMessage 发送群聊回复消息。
//
// 该方法使用 botgo API 回复指定群聊消息。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - api: botgo API 实例
//   - groupOpenid: 群 OpenID
//   - msgID: 要回复的消息 ID
//   - content: 回复内容
//
// 返回值:
//   - error: 发送过程中发生的错误
func SendGroupReplyMessage(ctx context.Context, api openapi.OpenAPI, groupOpenid, msgID, content string) error {
	if content == "" {
		return fmt.Errorf("消息内容不能为空")
	}

	// 创建消息请求，包含引用消息
	msg := &dto.MessageToCreate{
		Content: content,
		MsgType: 0, // 文本消息
		MsgID:   msgID,
	}

	// 发送消息
	result, err := api.PostGroupMessage(ctx, groupOpenid, msg)
	if err != nil {
		return fmt.Errorf("发送群聊回复失败: %w", err)
	}

	slog.Debug("群聊回复发送成功",
		"group_openid", groupOpenid,
		"msg_id", result.ID,
		"reply_to", msgID)

	return nil
}

// TruncateMessage 截断消息内容以适应 QQ 消息长度限制。
//
// QQ 消息有长度限制，该方法确保消息不超过最大长度。
// 如果消息被截断，会添加省略号。
//
// 参数:
//   - content: 原始消息内容
//   - maxLength: 最大允许长度
//
// 返回值:
//   - string: 处理后的消息内容
func TruncateMessage(content string, maxLength int) string {
	if maxLength <= 0 {
		return content
	}

	if len(content) <= maxLength {
		return content
	}

	// 预留省略号空间
	if maxLength > 3 {
		return content[:maxLength-3] + "..."
	}

	return content[:maxLength]
}

// ValidateMessageContent 验证消息内容是否有效。
//
// 参数:
//   - content: 消息内容
//
// 返回值:
//   - error: 验证错误，如果内容有效则返回 nil
func ValidateMessageContent(content string) error {
	if content == "" {
		return fmt.Errorf("消息内容不能为空")
	}

	// QQ 消息长度限制（实际限制可能更高，这里做合理限制）
	const maxMessageLength = 4096
	if len(content) > maxMessageLength {
		return fmt.Errorf("消息内容过长（最大%d字符）", maxMessageLength)
	}

	return nil
}

// MessageSender 是消息发送器接口。
//
// 该接口抽象了消息发送功能，便于测试和 mock。
type MessageSender interface {
	// SendC2CMessage 发送私聊消息
	SendC2CMessage(ctx context.Context, openid string, msg *dto.MessageToCreate) (*dto.Message, error)
	// SendGroupMessage 发送群消息
	SendGroupMessage(ctx context.Context, groupOpenid string, msg *dto.MessageToCreate) (*dto.Message, error)
}

// DefaultMessageSender 是默认的消息发送器实现。
type DefaultMessageSender struct {
	api openapi.OpenAPI
}

// NewDefaultMessageSender 创建一个新的默认消息发送器。
func NewDefaultMessageSender(api openapi.OpenAPI) *DefaultMessageSender {
	return &DefaultMessageSender{api: api}
}

// SendC2CMessage 发送私聊消息。
func (s *DefaultMessageSender) SendC2CMessage(ctx context.Context, openid string, msg *dto.MessageToCreate) (*dto.Message, error) {
	return s.api.PostC2CMessage(ctx, openid, msg)
}

// SendGroupMessage 发送群消息。
func (s *DefaultMessageSender) SendGroupMessage(ctx context.Context, groupOpenid string, msg *dto.MessageToCreate) (*dto.Message, error) {
	return s.api.PostGroupMessage(ctx, groupOpenid, msg)
}

// SendMessageWithTimeout 带超时的消息发送。
//
// 参数:
//   - ctx: 父上下文
//   - sender: 消息发送器
//   - timeout: 超时时间
//   - openid: 用户 OpenID（私聊）或群 OpenID（群聊）
//   - msg: 消息内容
//   - isGroup: 是否为群聊
//
// 返回值:
//   - error: 发送过程中发生的错误
func SendMessageWithTimeout(
	ctx context.Context,
	sender MessageSender,
	timeout time.Duration,
	openid string,
	msg *dto.MessageToCreate,
	isGroup bool,
) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if isGroup {
		_, err := sender.SendGroupMessage(ctx, openid, msg)
		return err
	}
	_, err := sender.SendC2CMessage(ctx, openid, msg)
	return err
}
