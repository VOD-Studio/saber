package qq

import (
	"fmt"
	"sync"
	"time"

	"rua.plus/saber/internal/config"
)

// ChatMessage 表示一条聊天消息。
type ChatMessage struct {
	// Role 是消息角色: "user" 或 "assistant"。
	Role string
	// Content 是消息内容。
	Content string
	// Timestamp 是消息时间戳。
	Timestamp time.Time
}

// ContextManager 管理 QQ 用户的对话上下文。
//
// 为每个用户维护独立的对话历史，支持上下文相关的 AI 对话。
// 使用 userID 作为 key，用户上下文跨群共享。
//
// 线程安全：所有方法都是并发安全的。
type ContextManager struct {
	mu       sync.RWMutex
	contexts map[string][]ChatMessage // key: userID
	config   config.ContextConfig
}

// NewContextManager 创建一个新的上下文管理器。
//
// 参数:
//   - cfg: 上下文配置，控制消息数量限制等
//
// 返回值:
//   - *ContextManager: 创建的管理器实例
func NewContextManager(cfg config.ContextConfig) *ContextManager {
	return &ContextManager{
		contexts: make(map[string][]ChatMessage),
		config:   cfg,
	}
}

// AddMessage 添加消息到用户上下文。
//
// 自动管理上下文大小，超出限制时移除最旧的消息。
//
// 参数:
//   - userID: 用户 ID
//   - role: 消息角色 ("user" 或 "assistant")
//   - content: 消息内容
func (m *ContextManager) AddMessage(userID, role, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	msg := ChatMessage{
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}

	m.contexts[userID] = append(m.contexts[userID], msg)

	// 限制消息数量
	maxMsgs := m.config.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = 50 // 默认值
	}
	if len(m.contexts[userID]) > maxMsgs {
		// 保留最后 maxMsgs 条消息
		m.contexts[userID] = m.contexts[userID][len(m.contexts[userID])-maxMsgs:]
	}
}

// GetContext 获取用户的对话上下文。
//
// 参数:
//   - userID: 用户 ID
//
// 返回值:
//   - []ChatMessage: 用户的对话历史
func (m *ContextManager) GetContext(userID string) []ChatMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msgs := m.contexts[userID]
	if msgs == nil {
		return nil
	}

	// 返回副本，避免外部修改
	result := make([]ChatMessage, len(msgs))
	copy(result, msgs)
	return result
}

// ClearContext 清除用户的对话上下文。
//
// 参数:
//   - userID: 用户 ID
func (m *ContextManager) ClearContext(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.contexts, userID)
}

// GetContextInfo 获取用户上下文信息。
//
// 返回人类可读的上下文状态描述。
//
// 参数:
//   - userID: 用户 ID
//
// 返回值:
//   - string: 上下文信息描述
func (m *ContextManager) GetContextInfo(userID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msgs := m.contexts[userID]
	if len(msgs) == 0 {
		return "当前无对话上下文"
	}

	// 统计各角色消息数
	userCount := 0
	assistantCount := 0
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		}
	}

	return fmt.Sprintf("当前对话: %d 条消息 (用户: %d, 助手: %d)",
		len(msgs), userCount, assistantCount)
}

// HasContext 检查用户是否有上下文。
//
// 参数:
//   - userID: 用户 ID
//
// 返回值:
//   - bool: 如果用户有上下文则返回 true
func (m *ContextManager) HasContext(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.contexts[userID]) > 0
}
