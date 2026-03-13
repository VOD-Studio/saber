//go:build goolm

// Package ai 提供测试辅助函数。
package ai

import (
	"fmt"
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
