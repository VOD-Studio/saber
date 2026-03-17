// Package matrix 提供 Matrix 事件处理和命令处理功能。
package matrix

import (
	"context"

	"maunium.net/go/mautrix/id"
)

// contextKey 是用于上下文键的未导出类型，以避免冲突。
type contextKey struct {
	name string
}

var eventIDKey = &contextKey{"eventID"}

// WithEventID 返回一个新的上下文，其中包含给定的 EventID。
//
// 参数:
//   - ctx: 父上下文
//   - eventID: 要注入的 Matrix 事件 ID
//
// 返回值:
//   - context.Context: 包含 EventID 的新上下文
func WithEventID(ctx context.Context, eventID id.EventID) context.Context {
	return context.WithValue(ctx, eventIDKey, eventID)
}

// GetEventID 从上下文中检索 EventID。
//
// 参数:
//   - ctx: 要检索的上下文
//
// 返回值:
//   - id.EventID: 存储在上下文中的 EventID，如果未找到则返回空字符串
func GetEventID(ctx context.Context) id.EventID {
	if v := ctx.Value(eventIDKey); v != nil {
		if eventID, ok := v.(id.EventID); ok {
			return eventID
		}
	}
	return ""
}
