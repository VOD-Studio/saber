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
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleSystem    MessageRole = "system"
)

// ChatMessage 表示存储在上下文中的单个聊天消息。
type ChatMessage struct {
	Role      MessageRole
	Content   string
	UserID    id.UserID
	RoomID    id.RoomID
	Timestamp time.Time
}

// ContextManager 管理多个房间的对话上下文。
type ContextManager struct {
	mu             sync.RWMutex
	contexts       map[id.RoomID][]ChatMessage
	tokenCount     map[id.RoomID]int
	lastActivity   map[id.RoomID]time.Time // 记录每个房间最后活动时间
	config         config.ContextConfig
	stopCleanup    chan struct{}
	cleanupStopped sync.WaitGroup
	stopOnce       sync.Once // 确保 Stop 只执行一次
}

// NewContextManager 创建并返回一个新的上下文管理器实例。
func NewContextManager(config config.ContextConfig) *ContextManager {
	cm := &ContextManager{
		contexts:     make(map[id.RoomID][]ChatMessage),
		tokenCount:   make(map[id.RoomID]int),
		lastActivity: make(map[id.RoomID]time.Time),
		config:       config,
		stopCleanup:  make(chan struct{}),
	}
	cm.startBackgroundCleanup()
	return cm
}

// estimateTokens 估算文本的 token 数量。
func estimateTokens(text string) int {
	return int(float64(len(text)) / 0.75)
}

// startBackgroundCleanup 启动后台清理 goroutine。
func (cm *ContextManager) startBackgroundCleanup() {
	if cm.config.ExpiryMinutes <= 0 && cm.config.InactiveRoomHours <= 0 {
		return
	}

	cm.cleanupStopped.Add(1)
	go func() {
		defer cm.cleanupStopped.Done()

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-cm.stopCleanup:
				return
			case <-ticker.C:
				cm.cleanupAllExpired()
				cm.cleanupInactiveRooms()
			}
		}
	}()
}

// Stop 停止后台清理 goroutine。
// 此方法可以安全地多次调用，后续调用不会产生任何效果。
func (cm *ContextManager) Stop() {
	cm.stopOnce.Do(func() {
		close(cm.stopCleanup)
		cm.cleanupStopped.Wait()
	})
}

// AddMessage 向指定房间的上下文中添加新消息。
func (cm *ContextManager) AddMessage(roomID id.RoomID, role MessageRole, content string, userID id.UserID) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// 更新最后活动时间
	cm.lastActivity[roomID] = time.Now()

	cm.cleanupRoomContext(roomID)

	messages := cm.contexts[roomID]
	if messages == nil {
		messages = make([]ChatMessage, 0)
		cm.tokenCount[roomID] = 0
	}

	newMessage := ChatMessage{
		Role:      role,
		Content:   content,
		UserID:    userID,
		RoomID:    roomID,
		Timestamp: time.Now(),
	}
	messages = append(messages, newMessage)
	newTokens := estimateTokens(content)
	cm.tokenCount[roomID] += newTokens

	if cm.config.MaxMessages > 0 && len(messages) > cm.config.MaxMessages {
		removed := messages[:len(messages)-cm.config.MaxMessages]
		for _, m := range removed {
			cm.tokenCount[roomID] -= estimateTokens(m.Content)
		}
		messages = messages[len(messages)-cm.config.MaxMessages:]
	}

	messages, cm.tokenCount[roomID] = cm.truncateByTokensWithCount(messages, cm.tokenCount[roomID])

	cm.contexts[roomID] = messages
}

// GetContext 返回指定房间的上下文，格式化为OpenAI API所需的格式。
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
func (cm *ContextManager) ClearContext(roomID id.RoomID) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	delete(cm.contexts, roomID)
	delete(cm.tokenCount, roomID)
	delete(cm.lastActivity, roomID)
}

// cleanupRoomContext 清理指定房间的过期消息。
func (cm *ContextManager) cleanupRoomContext(roomID id.RoomID) {
	if cm.config.ExpiryMinutes <= 0 {
		return
	}

	messages, exists := cm.contexts[roomID]
	if !exists || len(messages) == 0 {
		return
	}

	expiryDuration := time.Duration(cm.config.ExpiryMinutes) * time.Minute
	now := time.Now()

	if now.Sub(messages[0].Timestamp) <= expiryDuration {
		return
	}

	cutoffIndex := 0
	for i, msg := range messages {
		if now.Sub(msg.Timestamp) <= expiryDuration {
			cutoffIndex = i
			break
		}
	}

	if cutoffIndex == 0 {
		delete(cm.contexts, roomID)
		delete(cm.tokenCount, roomID)
	} else {
		removed := messages[:cutoffIndex]
		for _, m := range removed {
			cm.tokenCount[roomID] -= estimateTokens(m.Content)
		}
		cm.contexts[roomID] = messages[cutoffIndex:]
	}
}

// cleanupAllExpired 清理所有房间的过期消息。
func (cm *ContextManager) cleanupAllExpired() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for roomID := range cm.contexts {
		cm.cleanupRoomContext(roomID)
	}
}

// cleanupInactiveRooms 清理长时间无活动的房间上下文。
//
// 这防止了内存泄漏：当机器人加入大量房间但大部分房间长期不活跃时，
// 上下文数据会持续占用内存。此方法会定期清理超过阈值未活动的房间。
func (cm *ContextManager) cleanupInactiveRooms() {
	if cm.config.InactiveRoomHours <= 0 {
		return
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now()
	inactiveThreshold := time.Duration(cm.config.InactiveRoomHours) * time.Hour

	for roomID, lastTime := range cm.lastActivity {
		if now.Sub(lastTime) > inactiveThreshold {
			delete(cm.contexts, roomID)
			delete(cm.tokenCount, roomID)
			delete(cm.lastActivity, roomID)
		}
	}
}

// truncateByTokensWithCount 根据令牌限制截断消息列表，返回截断后的消息和新的 token 计数。
func (cm *ContextManager) truncateByTokensWithCount(messages []ChatMessage, currentTokens int) ([]ChatMessage, int) {
	if cm.config.MaxTokens <= 0 || currentTokens <= cm.config.MaxTokens {
		return messages, currentTokens
	}

	for len(messages) > 0 && currentTokens > cm.config.MaxTokens {
		firstTokens := estimateTokens(messages[0].Content)
		currentTokens -= firstTokens
		messages = messages[1:]
	}

	return messages, currentTokens
}

// GetContextSize 返回指定房间上下文的大小信息。
func (cm *ContextManager) GetContextSize(roomID id.RoomID) (int, int) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	messages, exists := cm.contexts[roomID]
	if !exists {
		return 0, 0
	}

	return len(messages), cm.tokenCount[roomID]
}

// ListActiveRooms 返回所有有活动上下文的房间ID列表。
func (cm *ContextManager) ListActiveRooms() []id.RoomID {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	rooms := make([]id.RoomID, 0, len(cm.contexts))
	for roomID := range cm.contexts {
		rooms = append(rooms, roomID)
	}

	return rooms
}
