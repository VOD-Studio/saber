// Package ai_test 包含上下文管理器的单元测试。
package ai

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// TestNewContextManager 测试 NewContextManager 构造函数。
//
// 该测试覆盖以下场景：
//   - 创建有效的 ContextManager 实例
//   - 零值配置处理
//   - 配置正确设置
func TestNewContextManager(t *testing.T) {
	tests := []struct {
		name   string
		config config.ContextConfig
	}{
		{
			name:   "default config",
			config: config.DefaultContextConfig(),
		},
		{
			name:   "empty config",
			config: config.ContextConfig{},
		},
		{
			name: "custom config",
			config: config.ContextConfig{
				Enabled:       true,
				MaxMessages:   100,
				MaxTokens:     16000,
				ExpiryMinutes: 120,
			},
		},
		{
			name: "zero values",
			config: config.ContextConfig{
				Enabled:       false,
				MaxMessages:   0,
				MaxTokens:     0,
				ExpiryMinutes: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(tt.config)
			if cm == nil {
				t.Error("NewContextManager() returned nil")
				return
			}
			if cm.contexts == nil {
				t.Error("contexts map not initialized")
			}
			if cm.config != tt.config {
				t.Errorf("config not set correctly, got %+v, want %+v", cm.config, tt.config)
			}
		})
	}
}

// TestContextManager_AddMessage 测试 AddMessage 方法。
//
// 该测试覆盖以下场景：
//   - 向新房间添加消息
//   - 向已有房间添加消息
//   - 消息数量限制
//   - 不同角色的消息
func TestContextManager_AddMessage(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name     string
		config   config.ContextConfig
		messages []struct {
			role    MessageRole
			content string
		}
		wantCount int
	}{
		{
			name:   "add single message",
			config: config.ContextConfig{MaxMessages: 10, MaxTokens: 0, ExpiryMinutes: 0},
			messages: []struct {
				role    MessageRole
				content string
			}{
				{RoleUser, "Hello"},
			},
			wantCount: 1,
		},
		{
			name:   "add multiple messages",
			config: config.ContextConfig{MaxMessages: 10, MaxTokens: 0, ExpiryMinutes: 0},
			messages: []struct {
				role    MessageRole
				content string
			}{
				{RoleUser, "Hello"},
				{RoleAssistant, "Hi there!"},
				{RoleUser, "How are you?"},
			},
			wantCount: 3,
		},
		{
			name:   "message limit enforcement",
			config: config.ContextConfig{MaxMessages: 3, MaxTokens: 0, ExpiryMinutes: 0},
			messages: []struct {
				role    MessageRole
				content string
			}{
				{RoleUser, "Message 1"},
				{RoleUser, "Message 2"},
				{RoleUser, "Message 3"},
				{RoleUser, "Message 4"},
				{RoleUser, "Message 5"},
			},
			wantCount: 3, // Should keep only last 3
		},
		{
			name:   "all role types",
			config: config.ContextConfig{MaxMessages: 10, MaxTokens: 0, ExpiryMinutes: 0},
			messages: []struct {
				role    MessageRole
				content string
			}{
				{RoleSystem, "System message"},
				{RoleUser, "User message"},
				{RoleAssistant, "Assistant message"},
			},
			wantCount: 3,
		},
		{
			name:   "empty content",
			config: config.ContextConfig{MaxMessages: 10, MaxTokens: 0, ExpiryMinutes: 0},
			messages: []struct {
				role    MessageRole
				content string
			}{
				{RoleUser, ""},
				{RoleAssistant, ""},
			},
			wantCount: 2,
		},
		{
			name:   "zero max messages no limit",
			config: config.ContextConfig{MaxMessages: 0, MaxTokens: 0, ExpiryMinutes: 0},
			messages: []struct {
				role    MessageRole
				content string
			}{
				{RoleUser, "1"},
				{RoleUser, "2"},
				{RoleUser, "3"},
				{RoleUser, "4"},
				{RoleUser, "5"},
			},
			wantCount: 5, // No limit when MaxMessages is 0
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(tt.config)

			for _, msg := range tt.messages {
				cm.AddMessage(roomID, msg.role, msg.content, userID)
			}

			got := cm.GetContext(roomID)
			if len(got) != tt.wantCount {
				t.Errorf("GetContext() returned %d messages, want %d", len(got), tt.wantCount)
			}
		})
	}
}

// TestContextManager_AddMessage_MultipleRooms 测试多房间消息添加。
//
// 该测试覆盖以下场景：
//   - 向不同房间添加消息
//   - 房间间消息隔离
//   - ListActiveRooms 正确返回房间列表
func TestContextManager_AddMessage_MultipleRooms(t *testing.T) {
	cm := NewContextManager(config.ContextConfig{MaxMessages: 10, MaxTokens: 0, ExpiryMinutes: 0})

	room1 := id.RoomID("!room1:example.com")
	room2 := id.RoomID("!room2:example.com")
	room3 := id.RoomID("!room3:example.com")
	userID := id.UserID("@user:example.com")

	// 向不同房间添加消息
	cm.AddMessage(room1, RoleUser, "Room 1 message 1", userID)
	cm.AddMessage(room1, RoleUser, "Room 1 message 2", userID)
	cm.AddMessage(room2, RoleUser, "Room 2 message 1", userID)
	cm.AddMessage(room3, RoleUser, "Room 3 message 1", userID)
	cm.AddMessage(room3, RoleUser, "Room 3 message 2", userID)
	cm.AddMessage(room3, RoleUser, "Room 3 message 3", userID)

	// 验证房间数量
	rooms := cm.ListActiveRooms()
	if len(rooms) != 3 {
		t.Errorf("ListActiveRooms() returned %d rooms, want 3", len(rooms))
	}

	// 验证每个房间的消息数
	if len(cm.GetContext(room1)) != 2 {
		t.Errorf("Room 1 should have 2 messages, got %d", len(cm.GetContext(room1)))
	}
	if len(cm.GetContext(room2)) != 1 {
		t.Errorf("Room 2 should have 1 message, got %d", len(cm.GetContext(room2)))
	}
	if len(cm.GetContext(room3)) != 3 {
		t.Errorf("Room 3 should have 3 messages, got %d", len(cm.GetContext(room3)))
	}
}

// TestContextManager_GetContext 测试 GetContext 方法。
//
// 该测试覆盖以下场景：
//   - 获取存在的房间上下文
//   - 获取不存在的房间上下文
//   - OpenAI 消息格式转换
func TestContextManager_GetContext(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name        string
		setup       func(*ContextManager)
		roomID      id.RoomID
		wantLen     int
		wantRoles   []string
		wantContent []string
	}{
		{
			name:        "non-existent room",
			setup:       func(cm *ContextManager) {},
			roomID:      roomID,
			wantLen:     0,
			wantRoles:   nil,
			wantContent: nil,
		},
		{
			name: "existing room with messages",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Hello", userID)
				cm.AddMessage(roomID, RoleAssistant, "Hi!", userID)
			},
			roomID:      roomID,
			wantLen:     2,
			wantRoles:   []string{"user", "assistant"},
			wantContent: []string{"Hello", "Hi!"},
		},
		{
			name: "cleared room",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Hello", userID)
				cm.ClearContext(roomID)
			},
			roomID:      roomID,
			wantLen:     0,
			wantRoles:   nil,
			wantContent: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{MaxMessages: 10, MaxTokens: 0, ExpiryMinutes: 0})
			tt.setup(cm)

			got := cm.GetContext(tt.roomID)
			if len(got) != tt.wantLen {
				t.Errorf("GetContext() returned %d messages, want %d", len(got), tt.wantLen)
				return
			}

			for i, msg := range got {
				if i < len(tt.wantRoles) && msg.Role != tt.wantRoles[i] {
					t.Errorf("Message %d role = %s, want %s", i, msg.Role, tt.wantRoles[i])
				}
				if i < len(tt.wantContent) && msg.Content != tt.wantContent[i] {
					t.Errorf("Message %d content = %s, want %s", i, msg.Content, tt.wantContent[i])
				}
			}
		})
	}
}

// TestContextManager_ClearContext 测试 ClearContext 方法。
//
// 该测试覆盖以下场景：
//   - 清除存在的房间上下文
//   - 清除不存在的房间上下文
//   - 清除后再次添加消息
func TestContextManager_ClearContext(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name    string
		setup   func(*ContextManager)
		clearID id.RoomID
		wantLen int
	}{
		{
			name: "clear existing room",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Hello", userID)
				cm.AddMessage(roomID, RoleUser, "World", userID)
			},
			clearID: roomID,
			wantLen: 0,
		},
		{
			name:    "clear non-existent room",
			setup:   func(cm *ContextManager) {},
			clearID: roomID,
			wantLen: 0,
		},
		{
			name: "clear and re-add",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Before clear", userID)
			},
			clearID: roomID,
			wantLen: 0,
		},
		{
			name: "clear then add new message",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Before clear", userID)
				cm.ClearContext(roomID)
				cm.AddMessage(roomID, RoleUser, "After clear", userID)
			},
			clearID: roomID,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{})
			tt.setup(cm)
			cm.ClearContext(tt.clearID)

			got := cm.GetContext(tt.clearID)
			if len(got) != tt.wantLen {
				t.Errorf("After ClearContext, GetContext() returned %d messages, want %d", len(got), tt.wantLen)
			}
		})
	}
}

// TestContextManager_GetContextSize 测试 GetContextSize 方法。
//
// 该测试覆盖以下场景：
//   - 空房间返回 0
//   - 正确计算消息数量
//   - 正确估算令牌数量
func TestContextManager_GetContextSize(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name      string
		setup     func(*ContextManager)
		wantCount int
		minTokens int // 最小令牌数（由于估算算法，使用最小值检查）
	}{
		{
			name:      "non-existent room",
			setup:     func(cm *ContextManager) {},
			wantCount: 0,
			minTokens: 0,
		},
		{
			name: "single short message",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Hi", userID)
			},
			wantCount: 1,
			minTokens: 1, // len("Hi") = 2, tokens ~= 2/0.75 = 2.67 -> 2
		},
		{
			name: "multiple messages",
			setup: func(cm *ContextManager) {
				cm.AddMessage(roomID, RoleUser, "Hello world", userID)
				cm.AddMessage(roomID, RoleAssistant, "Hi there!", userID)
			},
			wantCount: 2,
			minTokens: 10, // Combined length ~20 chars, tokens ~= 26
		},
		{
			name: "long message",
			setup: func(cm *ContextManager) {
				longMsg := strings.Repeat("x", 100)
				cm.AddMessage(roomID, RoleUser, longMsg, userID)
			},
			wantCount: 1,
			minTokens: 100, // 100 chars / 0.75 = 133 tokens
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{MaxMessages: 100, MaxTokens: 0, ExpiryMinutes: 0})
			tt.setup(cm)

			msgCount, tokenCount := cm.GetContextSize(roomID)
			if msgCount != tt.wantCount {
				t.Errorf("GetContextSize() message count = %d, want %d", msgCount, tt.wantCount)
			}
			if tokenCount < tt.minTokens {
				t.Errorf("GetContextSize() token count = %d, want at least %d", tokenCount, tt.minTokens)
			}
		})
	}
}

// TestContextManager_ListActiveRooms 测试 ListActiveRooms 方法。
//
// 该测试覆盖以下场景：
//   - 空管理器返回空列表
//   - 多个房间正确返回
//   - 清除房间后列表更新
func TestContextManager_ListActiveRooms(t *testing.T) {
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name    string
		setup   func(*ContextManager)
		wantLen int
	}{
		{
			name:    "empty manager",
			setup:   func(cm *ContextManager) {},
			wantLen: 0,
		},
		{
			name: "single room",
			setup: func(cm *ContextManager) {
				cm.AddMessage("!room1:example.com", RoleUser, "Hello", userID)
			},
			wantLen: 1,
		},
		{
			name: "multiple rooms",
			setup: func(cm *ContextManager) {
				cm.AddMessage("!room1:example.com", RoleUser, "Hello", userID)
				cm.AddMessage("!room2:example.com", RoleUser, "World", userID)
				cm.AddMessage("!room3:example.com", RoleUser, "Test", userID)
			},
			wantLen: 3,
		},
		{
			name: "after clear",
			setup: func(cm *ContextManager) {
				roomID := id.RoomID("!room1:example.com")
				cm.AddMessage(roomID, RoleUser, "Hello", userID)
				cm.AddMessage("!room2:example.com", RoleUser, "World", userID)
				cm.ClearContext(roomID)
			},
			wantLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{})
			tt.setup(cm)

			rooms := cm.ListActiveRooms()
			if len(rooms) != tt.wantLen {
				t.Errorf("ListActiveRooms() returned %d rooms, want %d", len(rooms), tt.wantLen)
			}
		})
	}
}

// TestContextManager_TokenTruncation 测试令牌截断功能。
//
// 该测试覆盖以下场景：
//   - 不同 MaxTokens 值的截断行为
//   - 消息从开头移除
//   - 零值 MaxTokens 不截断
func TestContextManager_TokenTruncation(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	// 每个 "test message" 约 14 个字符 = ~18 tokens
	// 我们创建多个消息来测试截断

	tests := []struct {
		name        string
		maxTokens   int
		msgCount    int
		wantMaxMsgs int // 预期最大消息数（由于截断）
	}{
		{
			name:        "no token limit",
			maxTokens:   0,
			msgCount:    10,
			wantMaxMsgs: 10, // No truncation
		},
		{
			name:        "small token limit",
			maxTokens:   50, // ~2-3 messages
			msgCount:    10,
			wantMaxMsgs: 3, // Truncated to fit within 50 tokens
		},
		{
			name:        "very small token limit",
			maxTokens:   20, // ~1 message
			msgCount:    10,
			wantMaxMsgs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{
				MaxMessages:   100, // High enough to not affect token test
				MaxTokens:     tt.maxTokens,
				ExpiryMinutes: 0,
			})

			for i := range tt.msgCount {
				cm.AddMessage(roomID, RoleUser, "test message "+fmt.Sprint(i), userID)
			}

			got := cm.GetContext(roomID)
			if len(got) > tt.wantMaxMsgs {
				t.Errorf("GetContext() returned %d messages, want at most %d", len(got), tt.wantMaxMsgs)
			}
		})
	}
}

// TestContextManager_MessageAndTokenLimit 测试消息数和令牌数双重限制。
//
// 该测试覆盖以下场景：
//   - 两个限制同时生效
//   - 更严格的限制优先
func TestContextManager_MessageAndTokenLimit(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name        string
		maxMessages int
		maxTokens   int
		msgCount    int
		wantMaxMsgs int
	}{
		{
			name:        "message limit is stricter",
			maxMessages: 3,
			maxTokens:   10000, // Very high
			msgCount:    10,
			wantMaxMsgs: 3,
		},
		{
			name:        "token limit is stricter",
			maxMessages: 100, // Very high
			maxTokens:   30,  // ~1-2 messages
			msgCount:    10,
			wantMaxMsgs: 2,
		},
		{
			name:        "both limits same effect",
			maxMessages: 5,
			maxTokens:   100, // ~5 messages
			msgCount:    10,
			wantMaxMsgs: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{
				MaxMessages:   tt.maxMessages,
				MaxTokens:     tt.maxTokens,
				ExpiryMinutes: 0,
			})

			for i := range tt.msgCount {
				cm.AddMessage(roomID, RoleUser, "message "+fmt.Sprint(i), userID)
			}

			got := cm.GetContext(roomID)
			if len(got) > tt.wantMaxMsgs {
				t.Errorf("GetContext() returned %d messages, want at most %d", len(got), tt.wantMaxMsgs)
			}
		})
	}
}

// TestContextManager_ExpiryCleanup 测试过期清理功能。
//
// 该测试覆盖以下场景：
//   - 零 ExpiryMinutes 不清理
//   - 消息正确过期
//   - 部分消息过期
func TestContextManager_ExpiryCleanup(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	t.Run("no expiry when ExpiryMinutes is zero", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   10,
			MaxTokens:     0,
			ExpiryMinutes: 0,
		})

		cm.AddMessage(roomID, RoleUser, "Message", userID)
		// 直接获取，不应过期
		got := cm.GetContext(roomID)
		if len(got) != 1 {
			t.Errorf("Expected 1 message, got %d", len(got))
		}
	})

	t.Run("messages added after cleanup are kept", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   10,
			MaxTokens:     0,
			ExpiryMinutes: 1,
		})

		cm.AddMessage(roomID, RoleUser, "New message", userID)
		got := cm.GetContext(roomID)
		if len(got) != 1 {
			t.Errorf("Expected 1 message, got %d", len(got))
		}
	})

	t.Run("cleanup triggered with expired messages", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   10,
			MaxTokens:     0,
			ExpiryMinutes: 1,
		})

		oldTime := time.Now().Add(-5 * time.Minute)
		cm.mu.Lock()
		cm.contexts[roomID] = []ChatMessage{
			{
				Role:      RoleUser,
				Content:   "Old message 1",
				UserID:    userID,
				RoomID:    roomID,
				Timestamp: oldTime,
			},
			{
				Role:      RoleUser,
				Content:   "Old message 2",
				UserID:    userID,
				RoomID:    roomID,
				Timestamp: oldTime,
			},
		}
		cm.mu.Unlock()

		cm.AddMessage(roomID, RoleUser, "New message", userID)

		got := cm.GetContext(roomID)
		if len(got) != 1 {
			t.Errorf("Expected 1 message after cleanup of expired, got %d", len(got))
		}
		if len(got) > 0 && got[0].Content != "New message" {
			t.Errorf("Expected new message to remain, got %s", got[0].Content)
		}
	})

	t.Run("partial expiry keeps recent messages", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   10,
			MaxTokens:     0,
			ExpiryMinutes: 5,
		})

		oldTime := time.Now().Add(-10 * time.Minute)
		recentTime := time.Now().Add(-1 * time.Minute)
		cm.mu.Lock()
		cm.contexts[roomID] = []ChatMessage{
			{
				Role:      RoleUser,
				Content:   "Old expired",
				UserID:    userID,
				RoomID:    roomID,
				Timestamp: oldTime,
			},
			{
				Role:      RoleUser,
				Content:   "Recent message",
				UserID:    userID,
				RoomID:    roomID,
				Timestamp: recentTime,
			},
		}
		cm.mu.Unlock()

		cm.AddMessage(roomID, RoleUser, "New message", userID)

		got := cm.GetContext(roomID)
		if len(got) != 2 {
			t.Errorf("Expected 2 messages (recent + new), got %d", len(got))
		}
	})

	t.Run("empty room context cleaned up", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   10,
			MaxTokens:     0,
			ExpiryMinutes: 1,
		})

		oldTime := time.Now().Add(-5 * time.Minute)
		cm.mu.Lock()
		cm.contexts[roomID] = []ChatMessage{
			{
				Role:      RoleUser,
				Content:   "Old message",
				UserID:    userID,
				RoomID:    roomID,
				Timestamp: oldTime,
			},
		}
		cm.mu.Unlock()

		room2 := id.RoomID("!room2:example.com")
		cm.AddMessage(room2, RoleUser, "New message in room2", userID)

		if len(cm.GetContext(roomID)) != 0 {
			t.Error("Room 1 should be cleaned up")
		}
		if len(cm.GetContext(room2)) != 1 {
			t.Error("Room 2 should have message")
		}
	})
}

// TestContextManager_Concurrency 测试并发安全性。
//
// 该测试覆盖以下场景：
//   - 并发添加消息
//   - 并发读取上下文
//   - 并发清除上下文
//   - 并发获取房间列表
func TestContextManager_Concurrency(t *testing.T) {
	const goroutines = 100
	const messagesPerGoroutine = 10

	cm := NewContextManager(config.ContextConfig{
		MaxMessages:   1000,
		MaxTokens:     0,
		ExpiryMinutes: 0,
	})

	errChan := make(chan error, goroutines*4)

	// 并发添加消息到不同房间
	for i := range goroutines {
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in AddMessage: %v", r)
					return
				}
				errChan <- nil
			}()

			roomID := id.RoomID(fmt.Sprintf("!room%d:example.com", idx%10))
			userID := id.UserID("@user:example.com")

			for j := range messagesPerGoroutine {
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d-%d", idx, j), userID)
			}
		}(i)
	}

	// 并发读取上下文
	for range goroutines {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in GetContext: %v", r)
					return
				}
				errChan <- nil
			}()

			roomID := id.RoomID("!room0:example.com")
			_ = cm.GetContext(roomID)
		}()
	}

	// 并发获取上下文大小
	for range goroutines {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in GetContextSize: %v", r)
					return
				}
				errChan <- nil
			}()

			roomID := id.RoomID("!room0:example.com")
			_, _ = cm.GetContextSize(roomID)
		}()
	}

	// 并发获取活跃房间列表
	for range goroutines {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in ListActiveRooms: %v", r)
					return
				}
				errChan <- nil
			}()

			_ = cm.ListActiveRooms()
		}()
	}

	// 收集所有结果
	totalTests := goroutines * 4
	for range totalTests {
		if err := <-errChan; err != nil {
			t.Errorf("并发测试失败: %v", err)
		}
	}

	// 验证最终状态
	rooms := cm.ListActiveRooms()
	if len(rooms) == 0 {
		t.Error("Expected some rooms to have messages after concurrent operations")
	}
}

// TestContextManager_Concurrency_SameRoom 测试同一房间的并发操作。
//
// 该测试覆盖以下场景：
//   - 多个 goroutine 同时向同一房间添加消息
//   - 确保最终状态一致
func TestContextManager_Concurrency_SameRoom(t *testing.T) {
	const goroutines = 100
	const messagesPerGoroutine = 10

	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	cm := NewContextManager(config.ContextConfig{
		MaxMessages:   goroutines * messagesPerGoroutine, // 足够大以容纳所有消息
		MaxTokens:     0,
		ExpiryMinutes: 0,
	})

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			for j := range messagesPerGoroutine {
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Goroutine %d Message %d", idx, j), userID)
			}
		}(i)
	}

	wg.Wait()

	// 验证消息数量
	msgCount, _ := cm.GetContextSize(roomID)
	expectedMax := goroutines * messagesPerGoroutine
	if msgCount != expectedMax {
		t.Errorf("Expected %d messages, got %d", expectedMax, msgCount)
	}
}

// TestContextManager_BoundaryCases 测试边界情况。
//
// 该测试覆盖以下场景：
//   - 空字符串内容
//   - 非常长的消息内容
//   - Unicode 字符处理
//   - 特殊字符处理
func TestContextManager_BoundaryCases(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "empty content",
			content: "",
			wantErr: false,
		},
		{
			name:    "whitespace only",
			content: "   ",
			wantErr: false,
		},
		{
			name:    "unicode chinese",
			content: "你好世界，这是一个测试消息。",
			wantErr: false,
		},
		{
			name:    "unicode emoji",
			content: "Hello 👋 World 🌍 Test 🧪",
			wantErr: false,
		},
		{
			name:    "unicode mixed",
			content: "Hello 世界 🌍 Привет мир",
			wantErr: false,
		},
		{
			name:    "special characters",
			content: "Special: \n\t\r\"'<>&",
			wantErr: false,
		},
		{
			name:    "very long message",
			content: strings.Repeat("x", 100000),
			wantErr: false,
		},
		{
			name:    "newlines only",
			content: "\n\n\n\n",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{
				MaxMessages:   10,
				MaxTokens:     0,
				ExpiryMinutes: 0,
			})

			// 不应该 panic
			defer func() {
				if r := recover(); r != nil {
					if !tt.wantErr {
						t.Errorf("Unexpected panic: %v", r)
					}
				}
			}()

			cm.AddMessage(roomID, RoleUser, tt.content, userID)
			got := cm.GetContext(roomID)

			if len(got) != 1 {
				t.Errorf("Expected 1 message, got %d", len(got))
				return
			}

			if got[0].Content != tt.content {
				t.Errorf("Content mismatch: got %q, want %q", got[0].Content, tt.content)
			}
		})
	}
}

// TestContextManager_RaceCondition 测试竞态条件。
//
// 该测试使用竞态检测器验证并发安全性。
func TestContextManager_RaceCondition(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	cm := NewContextManager(config.ContextConfig{
		MaxMessages:   100,
		MaxTokens:     0,
		ExpiryMinutes: 0,
	})

	var wg sync.WaitGroup

	// 写入 goroutine
	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := range 100 {
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Writer %d msg %d", idx, j), userID)
			}
		}(i)
	}

	// 读取 goroutine
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = cm.GetContext(roomID)
				_, _ = cm.GetContextSize(roomID)
				_ = cm.ListActiveRooms()
			}
		}()
	}

	// 清除 goroutine
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				cm.ClearContext(roomID)
			}
		}()
	}

	wg.Wait()
}

// TestContextManager_TokenEstimationAccuracy 测试令牌估算算法准确性。
//
// 该测试验证令牌估算公式是否正确应用。
func TestContextManager_TokenEstimationAccuracy(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	tests := []struct {
		name      string
		content   string
		minTokens int
		maxTokens int
	}{
		{
			name:      "simple ascii",
			content:   "Hello World", // 11 chars
			minTokens: 10,            // 11/0.75 = 14.67 -> 14
			maxTokens: 20,
		},
		{
			name:      "long message",
			content:   strings.Repeat("a", 100), // 100 chars
			minTokens: 100,                      // 100/0.75 = 133
			maxTokens: 150,
		},
		{
			name:      "empty message",
			content:   "",
			minTokens: 0,
			maxTokens: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cm := NewContextManager(config.ContextConfig{
				MaxMessages:   10,
				MaxTokens:     10000, // High enough to not truncate
				ExpiryMinutes: 0,
			})

			cm.AddMessage(roomID, RoleUser, tt.content, userID)
			_, tokens := cm.GetContextSize(roomID)

			if tokens < tt.minTokens || tokens > tt.maxTokens {
				t.Errorf("Token estimation out of range: got %d, want [%d, %d]", tokens, tt.minTokens, tt.maxTokens)
			}
		})
	}
}

// TestContextManager_MessageOrder 测试消息顺序保持。
//
// 该测试覆盖以下场景：
//   - 消息按添加顺序存储
//   - 截断后保留最新消息
func TestContextManager_MessageOrder(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	t.Run("message order preserved", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   10,
			MaxTokens:     0,
			ExpiryMinutes: 0,
		})

		for i := range 5 {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
		}

		got := cm.GetContext(roomID)
		for i, msg := range got {
			expected := fmt.Sprintf("Message %d", i)
			if msg.Content != expected {
				t.Errorf("Message %d: got %q, want %q", i, msg.Content, expected)
			}
		}
	})

	t.Run("truncation keeps newest", func(t *testing.T) {
		cm := NewContextManager(config.ContextConfig{
			MaxMessages:   3,
			MaxTokens:     0,
			ExpiryMinutes: 0,
		})

		for i := range 5 {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
		}

		got := cm.GetContext(roomID)
		// 应该保留最后 3 条消息 (2, 3, 4)
		expected := []string{"Message 2", "Message 3", "Message 4"}
		for i, msg := range got {
			if msg.Content != expected[i] {
				t.Errorf("Message %d: got %q, want %q", i, msg.Content, expected[i])
			}
		}
	})
}

// TestContextManager_ZeroConfig 测试零值配置行为。
//
// 该测试覆盖以下场景：
//   - 所有配置为零值时正确工作
//   - 不应用任何限制
func TestContextManager_ZeroConfig(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	cm := NewContextManager(config.ContextConfig{
		Enabled:       false,
		MaxMessages:   0,
		MaxTokens:     0,
		ExpiryMinutes: 0,
	})

	// 添加大量消息
	for i := range 100 {
		cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
	}

	// 不应该有任何截断
	got := cm.GetContext(roomID)
	if len(got) != 100 {
		t.Errorf("Expected 100 messages with zero config, got %d", len(got))
	}
}

// TestContextManager_ClearMultipleTimes 测试多次清除同一房间。
func TestContextManager_ClearMultipleTimes(t *testing.T) {
	roomID := id.RoomID("!test:example.com")
	userID := id.UserID("@user:example.com")

	cm := NewContextManager(config.ContextConfig{})

	cm.AddMessage(roomID, RoleUser, "Test", userID)

	// 多次清除不应 panic
	for range 10 {
		cm.ClearContext(roomID)
	}

	if len(cm.GetContext(roomID)) != 0 {
		t.Error("Context should be empty after clear")
	}
}

// TestContextManager_NilUserID 测试空 UserID 处理。
func TestContextManager_NilUserID(t *testing.T) {
	roomID := id.RoomID("!test:example.com")

	cm := NewContextManager(config.ContextConfig{
		MaxMessages:   10,
		MaxTokens:     0,
		ExpiryMinutes: 0,
	})

	// 不应 panic
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Unexpected panic with empty userID: %v", r)
		}
	}()

	cm.AddMessage(roomID, RoleUser, "Test", "")
	got := cm.GetContext(roomID)

	if len(got) != 1 {
		t.Errorf("Expected 1 message, got %d", len(got))
	}
}
