package ai

import (
	"encoding/json"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/conversation"
)

// MessageRole 保留 Matrix 命令层的角色名称。
type MessageRole = conversation.MessageRole

const (
	// RoleUser 表示用户消息。
	RoleUser = conversation.RoleUser
	// RoleAssistant 表示助手消息。
	RoleAssistant = conversation.RoleAssistant
	// RoleSystem 表示系统消息。
	RoleSystem = conversation.RoleSystem
)

// ContextManager 是旧 Matrix 历史命令的兼容 adapter，存储由 conversation 管理。
type ContextManager struct {
	history *conversation.ContextManager
	account string
	config  config.ContextConfig
}

// NewContextManager 创建独立历史存储，正式账号由 NewService 绑定。
func NewContextManager(cfg config.ContextConfig) *ContextManager {
	return &ContextManager{history: conversation.NewContextManager(cfg), account: "legacy", config: cfg}
}
func (cm *ContextManager) key(roomID id.RoomID) chat.SessionID {
	return (chat.Session{Platform: "matrix", Account: cm.account, Conversation: string(roomID)}).Key()
}

// Stop 停止历史清理任务。
func (cm *ContextManager) Stop() { cm.history.Stop() }

// AddMessage 为旧 Matrix 调用方追加消息。
func (cm *ContextManager) AddMessage(roomID id.RoomID, role MessageRole, content string, userID id.UserID) {
	cm.history.AddMessage(cm.key(roomID), role, content, string(userID))
}

// GetContext 返回当前 Matrix 账号房间历史。
func (cm *ContextManager) GetContext(roomID id.RoomID) []openai.ChatCompletionMessage {
	return cm.history.GetContext(cm.key(roomID))
}

// ClearContext 清除当前 Matrix 账号房间历史。
func (cm *ContextManager) ClearContext(roomID id.RoomID) { cm.history.ClearContext(cm.key(roomID)) }

// GetContextSize 返回当前 Matrix 账号房间历史大小。
func (cm *ContextManager) GetContextSize(roomID id.RoomID) (int, int) {
	return cm.history.GetContextSize(cm.key(roomID))
}

// ListActiveRooms 只返回当前 Matrix 账号的主会话，避免将其他平台历史当作房间。
func (cm *ContextManager) ListActiveRooms() []id.RoomID {
	var rooms []id.RoomID
	for _, key := range cm.history.ListActiveRooms() {
		var fields []string
		if json.Unmarshal([]byte(key), &fields) == nil && len(fields) == 4 && fields[0] == "matrix" && fields[1] == cm.account && fields[3] == "" {
			rooms = append(rooms, id.RoomID(fields[2]))
		}
	}
	return rooms
}
