<!-- Parent: ../AGENTS.md -->
<!-- Generated: 2026-03-26 | Updated: 2026-03-26 -->

# context

## Purpose

提供跨包共享的上下文键和类型安全的上下文值存取函数。避免在多个包中重复定义 `contextKey` 类型。

## Key Files

| File | Description |
|------|-------------|
| `keys.go` | 泛型上下文键定义和存取函数 |
| `user.go` | 用户上下文便捷函数 |

## For AI Agents

### Working In This Directory

- **小包**: 只有两个文件
- **泛型实现**: 使用 Go 泛型确保类型安全

### Common Patterns

#### 定义上下文键

```go
var UserIDKey = Key[id.UserID]{name: "userID"}
var RoomIDKey = Key[id.RoomID]{name: "roomID"}
```

#### 设置和获取值

```go
// 设置值
ctx = context.WithValue(ctx, UserIDKey, userID)

// 获取值
userID, ok := context.GetValue(ctx, UserIDKey)
```

#### 用户上下文便捷函数

```go
// 设置用户上下文（同时设置 userID 和 roomID）
ctx = appcontext.WithUserContext(ctx, userID, roomID)

// 获取用户上下文
userID, ok := appcontext.GetUserFromContext(ctx)
roomID, ok := appcontext.GetRoomFromContext(ctx)
```

## Dependencies

### Internal

无

### External

- `context` - Go 标准库
- `maunium.net/go/mautrix/id` - Matrix ID 类型

<!-- MANUAL: -->