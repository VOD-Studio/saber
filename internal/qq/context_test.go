package qq

import (
	"sync"
	"testing"

	"rua.plus/saber/internal/config"
)

func TestContextManager_AddMessage(t *testing.T) {
	cfg := config.ContextConfig{MaxMessages: 10}
	m := NewContextManager(cfg)

	m.AddMessage("user1", "user", "你好")
	m.AddMessage("user1", "assistant", "你好！有什么可以帮助你的？")

	ctx := m.GetContext("user1")
	if len(ctx) != 2 {
		t.Errorf("GetContext() len = %d, want 2", len(ctx))
	}

	if ctx[0].Role != "user" || ctx[0].Content != "你好" {
		t.Errorf("First message mismatch: %+v", ctx[0])
	}
}

func TestContextManager_MaxMessages(t *testing.T) {
	cfg := config.ContextConfig{MaxMessages: 3}
	m := NewContextManager(cfg)

	// 添加 5 条消息，应该只保留最后 3 条
	for i := 0; i < 5; i++ {
		m.AddMessage("user1", "user", "msg")
	}

	ctx := m.GetContext("user1")
	if len(ctx) != 3 {
		t.Errorf("GetContext() len = %d, want 3", len(ctx))
	}
}

func TestContextManager_ClearContext(t *testing.T) {
	cfg := config.ContextConfig{}
	m := NewContextManager(cfg)

	m.AddMessage("user1", "user", "test")
	m.ClearContext("user1")

	ctx := m.GetContext("user1")
	if len(ctx) != 0 {
		t.Errorf("GetContext() after clear = %d, want 0", len(ctx))
	}
}

func TestContextManager_GetContextInfo(t *testing.T) {
	cfg := config.ContextConfig{}
	m := NewContextManager(cfg)

	// 空上下文
	info := m.GetContextInfo("user1")
	if info != "当前无对话上下文" {
		t.Errorf("Empty context info = %q", info)
	}

	// 有消息
	m.AddMessage("user1", "user", "你好")
	m.AddMessage("user1", "assistant", "你好！")
	info = m.GetContextInfo("user1")
	if info == "" {
		t.Error("Context info is empty")
	}
}

func TestContextManager_Concurrent(t *testing.T) {
	cfg := config.ContextConfig{}
	m := NewContextManager(cfg)

	// 并发写入测试
	done := make(chan bool)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			userID := "user1"
			m.AddMessage(userID, "user", "test")
			_ = m.GetContext(userID)
			m.ClearContext(userID)
			done <- true
		}(i)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	for range done {
		// 等待所有 goroutine 完成
	}
}

func TestContextManager_HasContext(t *testing.T) {
	cfg := config.ContextConfig{}
	m := NewContextManager(cfg)

	// 无上下文
	if m.HasContext("user1") {
		t.Error("HasContext() should return false for empty context")
	}

	// 添加消息后
	m.AddMessage("user1", "user", "test")
	if !m.HasContext("user1") {
		t.Error("HasContext() should return true after adding message")
	}

	// 清除后
	m.ClearContext("user1")
	if m.HasContext("user1") {
		t.Error("HasContext() should return false after clear")
	}
}

func TestContextManager_MultipleUsers(t *testing.T) {
	cfg := config.ContextConfig{MaxMessages: 10}
	m := NewContextManager(cfg)

	// 多用户测试
	m.AddMessage("user1", "user", "hello from user1")
	m.AddMessage("user2", "user", "hello from user2")
	m.AddMessage("user1", "assistant", "hi user1")

	// 验证用户隔离
	ctx1 := m.GetContext("user1")
	if len(ctx1) != 2 {
		t.Errorf("user1 context len = %d, want 2", len(ctx1))
	}

	ctx2 := m.GetContext("user2")
	if len(ctx2) != 1 {
		t.Errorf("user2 context len = %d, want 1", len(ctx2))
	}

	// 清除 user1 不影响 user2
	m.ClearContext("user1")
	if m.HasContext("user1") {
		t.Error("user1 should be cleared")
	}
	if !m.HasContext("user2") {
		t.Error("user2 should still have context")
	}
}

func TestContextManager_DefaultMaxMessages(t *testing.T) {
	// MaxMessages 为 0 时应使用默认值 50
	cfg := config.ContextConfig{MaxMessages: 0}
	m := NewContextManager(cfg)

	// 添加 60 条消息，应该只保留最后 50 条
	for i := 0; i < 60; i++ {
		m.AddMessage("user1", "user", "msg")
	}

	ctx := m.GetContext("user1")
	if len(ctx) != 50 {
		t.Errorf("GetContext() len = %d, want 50 (default)", len(ctx))
	}
}