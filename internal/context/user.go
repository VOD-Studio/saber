package context

import (
	"context"

	"maunium.net/go/mautrix/id"
	"rua.plus/saber/internal/chat"
)

// WithUserContext 将用户 ID 和房间 ID 添加到上下文中。
//
// 这允许在请求链中传递用户和房间信息，供后续处理使用。
// 通常在处理 Matrix 事件时调用，将事件来源信息注入上下文。
//
// 参数:
//   - ctx: 原始上下文
//   - userID: Matrix 用户 ID（如 @user:matrix.org）
//   - roomID: Matrix 房间 ID（如 !room:matrix.org）
//
// 返回值:
//   - context.Context: 包含用户和房间信息的新上下文
//
// 示例:
//
//	ctx := WithUserContext(context.Background(), userID, roomID)
//	userID, ok := GetUserFromContext(ctx)
func WithUserContext(ctx context.Context, userID id.UserID, roomID id.RoomID) context.Context {
	// 兼容仍使用旧 Matrix 上下文的调用方；新聊天链路会提供完整账号作用域。
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		ctx = chat.WithIdentity(ctx, chat.Identity{Session: chat.Session{Platform: "matrix", Account: "legacy", Conversation: string(roomID)}, SenderID: string(userID)})
	}
	ctx = WithValue(ctx, UserIDKey, userID)
	return WithValue(ctx, RoomIDKey, roomID)
}

// GetUserFromContext 从上下文中提取用户 ID。
//
// 配合 WithUserContext 使用，用于在处理链中获取用户信息。
//
// 参数:
//   - ctx: 包含用户信息的上下文（由 WithUserContext 创建）
//
// 返回值:
//   - id.UserID: 用户 ID（如果存在）
//   - bool: 是否存在用户 ID
func GetUserFromContext(ctx context.Context) (id.UserID, bool) {
	return GetValue(ctx, UserIDKey)
}

// GetRoomFromContext 从上下文中提取房间 ID。
//
// 配合 WithUserContext 使用，用于在处理链中获取房间信息。
//
// 参数:
//   - ctx: 包含房间信息的上下文（由 WithUserContext 创建）
//
// 返回值:
//   - id.RoomID: 房间 ID（如果存在）
//   - bool: 是否存在房间 ID
func GetRoomFromContext(ctx context.Context) (id.RoomID, bool) {
	return GetValue(ctx, RoomIDKey)
}
