//go:build goolm

// Package ai 提供测试辅助函数。
package ai

import (
	"context"
	"fmt"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// TestRoomID 生成测试用房间 ID。
//
// 参数:
//   - n: 房间序号
//
// 返回值:
//   - id.RoomID: 格式为 !test{n}:example.com 的房间 ID
func TestRoomID(n int) id.RoomID {
	return id.RoomID(fmt.Sprintf("!test%d:example.com", n))
}

// TestUserID 生成测试用用户 ID。
//
// 参数:
//   - n: 用户序号
//
// 返回值:
//   - id.UserID: 格式为 @user{n}:example.com 的用户 ID
func TestUserID(n int) id.UserID {
	return id.UserID(fmt.Sprintf("@user%d:example.com", n))
}

// ContextManagerOption 是 ContextManager 配置选项函数。
type ContextManagerOption func(*config.ContextConfig)

// WithMaxMessages 设置最大消息数。
func WithMaxMessages(n int) ContextManagerOption {
	return func(c *config.ContextConfig) {
		c.MaxMessages = n
	}
}

// WithMaxTokens 设置最大 token 数。
func WithMaxTokens(n int) ContextManagerOption {
	return func(c *config.ContextConfig) {
		c.MaxTokens = n
	}
}

// WithExpiry 设置过期时间（分钟）。
func WithExpiry(minutes int) ContextManagerOption {
	return func(c *config.ContextConfig) {
		c.ExpiryMinutes = minutes
	}
}

// WithContextEnabled 设置是否启用上下文。
func WithContextEnabled(enabled bool) ContextManagerOption {
	return func(c *config.ContextConfig) {
		c.Enabled = enabled
	}
}

// NewTestContextManager 创建用于测试的上下文管理器。
//
// 参数:
//   - opts: 可选的配置选项
//
// 返回值:
//   - *ContextManager: 配置好的上下文管理器
func NewTestContextManager(opts ...ContextManagerOption) *ContextManager {
	cfg := config.DefaultContextConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return NewContextManager(cfg)
}

// AssertEventually 断言条件最终为真。
//
// 该函数会定期检查条件，直到超时或条件为真。
//
// 参数:
//   - t: 测试上下文
//   - condition: 条件函数
//   - timeout: 超时时间
//   - message: 失败时的错误消息
func AssertEventually(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v: %s", timeout, message)
}

// AssertNever 断言条件在指定时间内从未为真。
//
// 参数:
//   - t: 测试上下文
//   - condition: 条件函数
//   - duration: 检查持续时间
//   - message: 条件为真时的错误消息
func AssertNever(t *testing.T, condition func() bool, duration time.Duration, message string) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if condition() {
			t.Fatalf("condition was true: %s", message)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ProactiveSend 记录一次主动投递，便于断言目标会话与消息类型。
type ProactiveSend struct {
	// Conversation 是收到消息的平台会话标识。
	Conversation string
	// Text 是投递的正文。
	Text string
	// Notice 表示是否以通知消息投递。
	Notice bool
}

// FakeProactiveRooms 是 ProactiveRooms 的共享测试替身：
// 返回预设的会话元数据，并记录每一次主动投递。
type FakeProactiveRooms struct {
	// Conversations 是 ListConversations 的返回值。
	Conversations []chat.ConversationInfo
	// Info 是 ConversationInfo 的返回值；未填会话标识时回退为被查询的标识。
	Info chat.ConversationInfo
	// ListErr / InfoErr / SendErr 分别让枚举、元数据与投递失败，用于验证降级分支。
	ListErr error
	InfoErr error
	SendErr error
	// Sent 按调用顺序记录投递。
	Sent []ProactiveSend
}

// ListConversations 返回预设的会话列表。
func (f *FakeProactiveRooms) ListConversations(context.Context) ([]chat.ConversationInfo, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return f.Conversations, nil
}

// ConversationInfo 返回预设的会话元数据。
func (f *FakeProactiveRooms) ConversationInfo(_ context.Context, conversationID string) (chat.ConversationInfo, error) {
	if f.InfoErr != nil {
		return chat.ConversationInfo{}, f.InfoErr
	}
	info := f.Info
	if info.Conversation == "" {
		info.Conversation = conversationID
	}
	return info, nil
}

// SendText 记录一次普通消息投递。
func (f *FakeProactiveRooms) SendText(_ context.Context, conversationID, text string) (string, error) {
	return f.send(conversationID, text, false)
}

// SendNotice 记录一次通知消息投递。
func (f *FakeProactiveRooms) SendNotice(_ context.Context, conversationID, text string) (string, error) {
	return f.send(conversationID, text, true)
}

func (f *FakeProactiveRooms) send(conversationID, text string, notice bool) (string, error) {
	if f.SendErr != nil {
		return "", f.SendErr
	}
	f.Sent = append(f.Sent, ProactiveSend{Conversation: conversationID, Text: text, Notice: notice})
	return "$fake_event", nil
}

// SentTo 返回发往指定会话的第一条投递，没有则返回 false。
func (f *FakeProactiveRooms) SentTo(conversationID string) (ProactiveSend, bool) {
	for _, s := range f.Sent {
		if s.Conversation == conversationID {
			return s, true
		}
	}
	return ProactiveSend{}, false
}

// 确保共享替身满足主动聊天房间端口。
var _ ProactiveRooms = (*FakeProactiveRooms)(nil)
