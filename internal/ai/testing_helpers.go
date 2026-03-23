//go:build goolm

// Package ai 提供测试辅助函数。
package ai

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

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

// GenerateTestMessages 生成测试消息列表。
//
// 用于基准测试时批量生成指定数量和长度的测试消息。
//
// 参数:
//   - count: 消息数量
//   - msgLen: 每条消息的字符长度
//
// 返回值:
//   - []string: 生成的消息列表
func GenerateTestMessages(count, msgLen int) []string {
	messages := make([]string, count)
	for i := 0; i < count; i++ {
		messages[i] = strings.Repeat("a", msgLen)
	}
	return messages
}

// BenchmarkContextManager 创建用于基准测试的预填充上下文管理器。
//
// 该函数创建一个上下文管理器，并预先填充指定数量的房间和消息，
// 适用于测试大量数据场景下的性能。
//
// 参数:
//   - roomCount: 房间数量
//   - messagesPerRoom: 每个房间的消息数量
//   - msgLen: 每条消息的字符长度
//
// 返回值:
//   - *ContextManager: 预填充的上下文管理器
func BenchmarkContextManager(roomCount, messagesPerRoom, msgLen int) *ContextManager {
	cm := NewTestContextManager(WithMaxMessages(100))
	for r := 0; r < roomCount; r++ {
		roomID := TestRoomID(r)
		userID := TestUserID(r)
		for m := 0; m < messagesPerRoom; m++ {
			cm.AddMessage(roomID, RoleUser, strings.Repeat("a", msgLen), userID)
		}
	}
	return cm
}
