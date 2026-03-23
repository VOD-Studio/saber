// Package context 提供跨包共享的上下文键和访问函数。
//
// 这避免了在多个包中重复定义 contextKey 类型和访问函数，
// 并提供类型安全的上下文值存取。
package context

import (
	"context"

	"maunium.net/go/mautrix/id"
)

// Key 是用于上下文值的类型安全键。
//
// 使用泛型确保存储和检索的值类型一致。
type Key[T any] struct {
	name string
}

// 上下文键定义
var (
	// UserIDKey 是 Matrix 用户 ID 的上下文键。
	UserIDKey = Key[id.UserID]{name: "userID"}
	// RoomIDKey 是 Matrix 房间 ID 的上下文键。
	RoomIDKey = Key[id.RoomID]{name: "roomID"}
)

// WithValue 将值设置到上下文中。
//
// 使用泛型确保类型安全。
func WithValue[T any](ctx context.Context, key Key[T], value T) context.Context {
	return context.WithValue(ctx, key, value)
}

// GetValue 从上下文中获取值。
//
// 返回值和一个布尔值表示是否找到。
func GetValue[T any](ctx context.Context, key Key[T]) (T, bool) {
	v, ok := ctx.Value(key).(T)
	return v, ok
}
