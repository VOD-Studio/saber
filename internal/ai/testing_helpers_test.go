//go:build goolm

package ai

import (
	"testing"
	"time"

	"rua.plus/saber/internal/config"
)

// TestWithMaxMessages 测试 WithMaxMessages 选项。
func TestWithMaxMessages(t *testing.T) {
	cfg := config.DefaultContextConfig()
	WithMaxMessages(100)(&cfg)

	if cfg.MaxMessages != 100 {
		t.Errorf("WithMaxMessages() = %d, want 100", cfg.MaxMessages)
	}
}

// TestWithMaxTokens 测试 WithMaxTokens 选项。
func TestWithMaxTokens(t *testing.T) {
	cfg := config.DefaultContextConfig()
	WithMaxTokens(4000)(&cfg)

	if cfg.MaxTokens != 4000 {
		t.Errorf("WithMaxTokens() = %d, want 4000", cfg.MaxTokens)
	}
}

// TestWithExpiry 测试 WithExpiry 选项。
func TestWithExpiry(t *testing.T) {
	cfg := config.DefaultContextConfig()
	WithExpiry(60)(&cfg)

	if cfg.ExpiryMinutes != 60 {
		t.Errorf("WithExpiry() = %d, want 60", cfg.ExpiryMinutes)
	}
}

// TestWithContextEnabled 测试 WithContextEnabled 选项。
func TestWithContextEnabled(t *testing.T) {
	cfg := config.DefaultContextConfig()
	WithContextEnabled(false)(&cfg)

	if cfg.Enabled {
		t.Error("WithContextEnabled(false) should set Enabled to false")
	}

	WithContextEnabled(true)(&cfg)
	if !cfg.Enabled {
		t.Error("WithContextEnabled(true) should set Enabled to true")
	}
}

// TestNewTestContextManagerWithOptions 测试带选项的 NewTestContextManager。
func TestNewTestContextManagerWithOptions(t *testing.T) {
	cm := NewTestContextManager(
		WithMaxMessages(50),
		WithMaxTokens(2000),
		WithExpiry(30),
		WithContextEnabled(true),
	)

	if cm == nil {
		t.Fatal("NewTestContextManager() returned nil")
	}

	// 验证配置已应用
	if cm.config.MaxMessages != 50 {
		t.Errorf("MaxMessages = %d, want 50", cm.config.MaxMessages)
	}
	if cm.config.MaxTokens != 2000 {
		t.Errorf("MaxTokens = %d, want 2000", cm.config.MaxTokens)
	}
	if cm.config.ExpiryMinutes != 30 {
		t.Errorf("ExpiryMinutes = %d, want 30", cm.config.ExpiryMinutes)
	}
	if !cm.config.Enabled {
		t.Error("Enabled should be true")
	}
}

// TestAssertEventually_True 测试条件最终为真。
func TestAssertEventually_True(t *testing.T) {
	counter := 0
	condition := func() bool {
		counter++
		return counter >= 3
	}

	// 应该在条件为真时返回
	AssertEventually(t, condition, 1*time.Second, "condition should become true")

	if counter < 3 {
		t.Errorf("condition was called %d times, expected at least 3", counter)
	}
}

// TestAssertNever 测试条件始终为假。
func TestAssertNever(t *testing.T) {
	// 创建一个子测试来验证 AssertNever
	ran := false
	condition := func() bool {
		ran = true
		return false
	}

	AssertNever(t, condition, 100*time.Millisecond, "condition should never be true")

	if !ran {
		t.Error("condition should have been called")
	}
}