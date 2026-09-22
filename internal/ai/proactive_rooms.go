// Package ai 提供与 AI 相关的功能，包括主动聊天依赖的平台房间端口。
package ai

import (
	"context"

	"rua.plus/saber/internal/chat"
)

// ConversationLister 枚举可主动投递的会话。
//
// 主动聊天需要在没有入站消息时挑选目标房间，因此该能力由平台接入端提供，
// ai 核心不感知具体平台的房间概念。
type ConversationLister interface {
	// ListConversations 返回当前账号可以主动投递的会话元数据列表。
	ListConversations(ctx context.Context) ([]chat.ConversationInfo, error)
}

// ConversationInfoProvider 读取单个会话的元数据。
//
// 决策上下文与新成员欢迎都靠它区分私聊与群聊，因此需要由平台侧提供实时数据。
type ConversationInfoProvider interface {
	// ConversationInfo 返回指定会话的元数据；会话不存在或平台拒绝回答时返回错误。
	ConversationInfo(ctx context.Context, conversationID string) (chat.ConversationInfo, error)
}

// ProactiveRooms 是主动聊天依赖的完整房间端口：枚举会话、读取元数据、无入站投递。
//
// 它是 ai 侧的消费方接口，由平台接入端实现（Matrix 见 internal/platform/matrix 的 Rooms），
// 会话标识一律使用 chat.ConversationInfo.Conversation 的字符串形式。
type ProactiveRooms interface {
	ConversationLister
	ConversationInfoProvider

	// SendText 以普通消息向会话投递主动内容，返回平台消息标识。
	SendText(ctx context.Context, conversationID, text string) (string, error)
	// SendNotice 以低优先级通知向会话投递主动内容，返回平台消息标识。
	SendNotice(ctx context.Context, conversationID, text string) (string, error)
}

// degradedConversation 在平台元数据不可得时给出保守的会话快照。
//
// 名称回退为会话标识，成员数为 0，因此调用方按群聊处理，避免误用私聊语气。
func degradedConversation(conversationID string) chat.ConversationInfo {
	return chat.ConversationInfo{
		Conversation: conversationID,
		Name:         conversationID,
	}
}
