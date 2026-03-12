// Package ai 提供与AI相关的功能，包括对话上下文管理。
package ai

import (
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// MessageRole 表示聊天消息的角色类型。
type MessageRole string

const (
	// RoleUser 表示用户发送的消息。
	RoleUser MessageRole = "user"
	// RoleAssistant 表示AI助手发送的消息。
	RoleAssistant MessageRole = "assistant"
	// RoleSystem 表示系统消息。
	RoleSystem MessageRole = "system"
)

// ChatMessage 表示存储在上下文中的单个聊天消息。
//
// 每个消息包含角色、内容、用户ID、房间ID和时间戳信息。
type ChatMessage struct {
	// Role 是消息的角色（用户、助手或系统）。
	Role MessageRole
	// Content 是消息的实际内容。
	Content string
	// UserID 是发送消息的用户ID。
	UserID id.UserID
	// RoomID 是消息所属的房间ID。
	RoomID id.RoomID
	// Timestamp 是消息创建的时间戳。
	Timestamp time.Time
}

// ContextManager 管理多个房间的对话上下文。
//
// 它提供线程安全的上下文存储、消息限制和自动清理功能。
type ContextManager struct {
	// mu 保护 contexts 映射的读写锁。
	mu sync.RWMutex
	// contexts 存储每个房间的聊天消息列表。
	contexts map[id.RoomID][]ChatMessage
	// config 包含上下文管理的配置选项。
	config config.ContextConfig
}

// NewContextManager 创建并返回一个新的上下文管理器实例。
//
// 参数:
//   - config: 上下文管理配置
func NewContextManager(config config.ContextConfig) *ContextManager {
	return &ContextManager{
		contexts: make(map[id.RoomID][]ChatMessage),
		config:   config,
	}
}

// AddMessage 向指定房间的上下文中添加新消息。
//
// 该方法是线程安全的，会先清理过期的上下文，然后添加新消息，
// 并根据配置限制消息数量和令牌数量。
//
// 参数:
//   - roomID: 房间ID
//   - role: 消息角色
//   - content: 消息内容
//   - userID: 用户ID
func (cm *ContextManager) AddMessage(roomID id.RoomID, role MessageRole, content string, userID id.UserID) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 清理过期的上下文
	cm.cleanupExpiredContexts()

	// 获取或创建房间的上下文
	messages := cm.contexts[roomID]
	if messages == nil {
		messages = make([]ChatMessage, 0)
	}

	// 添加新消息
	newMessage := ChatMessage{
		Role:      role,
		Content:   content,
		UserID:    userID,
		RoomID:    roomID,
		Timestamp: time.Now(),
	}
	messages = append(messages, newMessage)

	// 根据最大消息数限制截断
	if cm.config.MaxMessages > 0 && len(messages) > cm.config.MaxMessages {
		messages = messages[len(messages)-cm.config.MaxMessages:]
	}

	// 根据令牌限制截断
	messages = cm.truncateByTokens(messages)

	// 更新上下文
	cm.contexts[roomID] = messages
}

// GetContext 返回指定房间的上下文，格式化为OpenAI API所需的格式。
//
// 参数:
//   - roomID: 房间ID
//
// 返回值:
//   - []openai.ChatCompletionMessage: OpenAI格式的消息列表
func (cm *ContextManager) GetContext(roomID id.RoomID) []openai.ChatCompletionMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	messages, exists := cm.contexts[roomID]
	if !exists {
		return []openai.ChatCompletionMessage{}
	}

	openaiMessages := make([]openai.ChatCompletionMessage, len(messages))
	for i, msg := range messages {
		openaiMessages[i] = openai.ChatCompletionMessage{
			Role:    string(msg.Role),
			Content: msg.Content,
		}
	}

	return openaiMessages
}

// ClearContext 清除指定房间的上下文。
//
// 参数:
//   - roomID: 房间ID
func (cm *ContextManager) ClearContext(roomID id.RoomID) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	delete(cm.contexts, roomID)
}

// cleanupExpiredContexts 清理过期的上下文。
//
// 如果配置了ExpiryMinutes > 0，则删除所有超过指定分钟数的上下文。
// 这是一个私有方法，在AddMessage中被调用。
func (cm *ContextManager) cleanupExpiredContexts() {
	if cm.config.ExpiryMinutes <= 0 {
		return
	}

	expiryDuration := time.Duration(cm.config.ExpiryMinutes) * time.Minute
	now := time.Now()

	// 遍历所有房间的上下文
	for roomID, messages := range cm.contexts {
		if len(messages) == 0 {
			continue
		}

		// 检查第一个消息是否过期（如果第一个消息没过期，后面的也不会过期）
		firstMessage := messages[0]
		if now.Sub(firstMessage.Timestamp) > expiryDuration {
			// 找到第一个未过期的消息
			cutoffIndex := 0
			for i, msg := range messages {
				if now.Sub(msg.Timestamp) <= expiryDuration {
					cutoffIndex = i
					break
				}
			}

			if cutoffIndex == 0 {
				// 所有消息都过期了
				delete(cm.contexts, roomID)
			} else {
				// 保留未过期的消息
				cm.contexts[roomID] = messages[cutoffIndex:]
			}
		}
	}
}

// truncateByTokens 根据令牌限制截断消息列表。
//
// 使用简单的字符计数估算（0.75个字符约等于1个令牌）。
// 这是一个私有方法，在AddMessage中被调用。
//
// 参数:
//   - messages: 要截断的消息列表
//
// 返回值:
//   - []ChatMessage: 截断后的消息列表
func (cm *ContextManager) truncateByTokens(messages []ChatMessage) []ChatMessage {
	if cm.config.MaxTokens <= 0 {
		return messages
	}

	// 计算当前令牌数
	var totalTokens int
	for _, msg := range messages {
		// 估算令牌数：0.75个字符约等于1个令牌
		tokens := int(float64(len(msg.Content)) / 0.75)
		totalTokens += tokens
	}

	// 如果总令牌数在限制内，直接返回
	if totalTokens <= cm.config.MaxTokens {
		return messages
	}

	// 从开头开始移除消息，直到令牌数在限制内
	for len(messages) > 0 && totalTokens > cm.config.MaxTokens {
		// 移除第一条消息
		firstMessage := messages[0]
		firstTokens := int(float64(len(firstMessage.Content)) / 0.75)
		totalTokens -= firstTokens
		messages = messages[1:]
	}

	return messages
}

// GetContextSize 返回指定房间上下文的大小信息。
//
// 参数:
//   - roomID: 房间ID
//
// 返回值:
//   - int: 消息数量
//   - int: 估算的令牌数量
func (cm *ContextManager) GetContextSize(roomID id.RoomID) (int, int) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	messages, exists := cm.contexts[roomID]
	if !exists {
		return 0, 0
	}

	messageCount := len(messages)
	tokenCount := 0
	for _, msg := range messages {
		tokenCount += int(float64(len(msg.Content)) / 0.75)
	}

	return messageCount, tokenCount
}

// ListActiveRooms 返回所有有活动上下文的房间ID列表。
//
// 返回值:
//   - []id.RoomID: 房间ID列表
func (cm *ContextManager) ListActiveRooms() []id.RoomID {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	rooms := make([]id.RoomID, 0, len(cm.contexts))
	for roomID := range cm.contexts {
		rooms = append(rooms, roomID)
	}

	return rooms
}
