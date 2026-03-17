// Package mcp 提供 MCP (Model Context Protocol) 集成功能。
package mcp

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"golang.org/x/time/rate"
	"maunium.net/go/mautrix/id"
)

// Matrix ID 格式的正则表达式。
var (
	// roomIDRegex 匹配 Matrix 房间 ID，格式：!xxx:server.com
	roomIDRegex = regexp.MustCompile(`^![A-Za-z0-9._-]+:[A-Za-z0-9.-]+$`)
	// userIDRegex 匹配 Matrix 用户 ID，格式：@xxx:server.com
	userIDRegex = regexp.MustCompile(`^@[A-Za-z0-9._-]+:[A-Za-z0-9.-]+$`)
)

// RateLimiter 提供基于用户和房间的速率限制。
//
// 它为每个用户和每个房间维护独立的令牌桶限流器，
// 防止单个用户或房间过度使用 MCP 工具。
type RateLimiter struct {
	mu    sync.RWMutex
	users map[id.UserID]*rate.Limiter
	rooms map[id.RoomID]*rate.Limiter
	limit rate.Limit
	burst int
}

// NewRateLimiter 创建新的速率限制器。
//
// 参数:
//   - callsPerMinute: 每分钟允许的最大调用次数
//
// 返回配置好的 RateLimiter 实例。
func NewRateLimiter(callsPerMinute int) *RateLimiter {
	return &RateLimiter{
		users: make(map[id.UserID]*rate.Limiter),
		rooms: make(map[id.RoomID]*rate.Limiter),
		limit: rate.Limit(float64(callsPerMinute) / 60.0),
		burst: callsPerMinute,
	}
}

// Allow 检查指定用户和房间的工具调用是否被允许。
//
// 返回 true 表示调用被允许，false 表示已达到速率限制。
// 用户和房间的限制是独立的，两者都必须通过才能允许调用。
func (r *RateLimiter) Allow(userID id.UserID, roomID id.RoomID) bool {
	// 检查用户速率限制
	userLimiter := r.getOrCreateUserLimiter(userID)
	if !userLimiter.Allow() {
		return false
	}

	// 检查房间速率限制
	roomLimiter := r.getOrCreateRoomLimiter(roomID)
	return roomLimiter.Allow()
}

// getOrCreateUserLimiter 获取或创建用户的速率限制器。
func (r *RateLimiter) getOrCreateUserLimiter(userID id.UserID) *rate.Limiter {
	r.mu.RLock()
	limiter, ok := r.users[userID]
	r.mu.RUnlock()

	if ok {
		return limiter
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 双重检查，防止并发创建
	if limiter, ok = r.users[userID]; ok {
		return limiter
	}

	limiter = rate.NewLimiter(r.limit, r.burst)
	r.users[userID] = limiter
	return limiter
}

// getOrCreateRoomLimiter 获取或创建房间的速率限制器。
func (r *RateLimiter) getOrCreateRoomLimiter(roomID id.RoomID) *rate.Limiter {
	r.mu.RLock()
	limiter, ok := r.rooms[roomID]
	r.mu.RUnlock()

	if ok {
		return limiter
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 双重检查，防止并发创建
	if limiter, ok = r.rooms[roomID]; ok {
		return limiter
	}

	limiter = rate.NewLimiter(r.limit, r.burst)
	r.rooms[roomID] = limiter
	return limiter
}

// ValidateRoomID 验证 Matrix 房间 ID 格式。
//
// 有效的房间 ID 格式为：!localpart:server.com
// 例如：!abc123:matrix.org
func ValidateRoomID(roomID id.RoomID) error {
	if roomID == "" {
		return fmt.Errorf("room ID cannot be empty")
	}
	if !roomIDRegex.MatchString(string(roomID)) {
		return fmt.Errorf("invalid room ID format: %s (expected: !localpart:server.com)", roomID)
	}
	return nil
}

// ValidateUserID 验证 Matrix 用户 ID 格式。
//
// 有效的用户 ID 格式为：@localpart:server.com
// 例如：@user:matrix.org
func ValidateUserID(userID id.UserID) error {
	if userID == "" {
		return fmt.Errorf("user ID cannot be empty")
	}
	if !userIDRegex.MatchString(string(userID)) {
		return fmt.Errorf("invalid user ID format: %s (expected: @localpart:server.com)", userID)
	}
	return nil
}

// Middleware 定义 MCP 工具调用中间件函数类型。
type Middleware func(ctx context.Context, userID id.UserID, roomID id.RoomID, toolName string, next ToolHandler) (any, error)

// ToolHandler 定义 MCP 工具处理函数类型。
type ToolHandler func(ctx context.Context) (any, error)

// RateLimitMiddleware 创建速率限制中间件。
//
// 在调用工具前检查用户和房间的速率限制。
// 如果超过限制，返回错误而不调用实际工具。
func RateLimitMiddleware(limiter *RateLimiter) Middleware {
	return func(ctx context.Context, userID id.UserID, roomID id.RoomID, toolName string, next ToolHandler) (any, error) {
		if !limiter.Allow(userID, roomID) {
			return nil, fmt.Errorf("rate limit exceeded for user %s in room %s", userID, roomID)
		}
		return next(ctx)
	}
}

// ValidationMiddleware 创建输入验证中间件。
//
// 在调用工具前验证用户 ID 和房间 ID 的格式。
func ValidationMiddleware() Middleware {
	return func(ctx context.Context, userID id.UserID, roomID id.RoomID, toolName string, next ToolHandler) (any, error) {
		if err := ValidateUserID(userID); err != nil {
			return nil, fmt.Errorf("validation failed: %w", err)
		}
		if err := ValidateRoomID(roomID); err != nil {
			return nil, fmt.Errorf("validation failed: %w", err)
		}
		return next(ctx)
	}
}

// ChainMiddleware 将多个中间件链接成一个。
//
// 中间件按传入顺序执行，最先传入的中间件最先执行。
// 例如：ChainMiddleware(m1, m2, m3) 会按 m1 -> m2 -> m3 的顺序执行。
func ChainMiddleware(middlewares ...Middleware) Middleware {
	return func(ctx context.Context, userID id.UserID, roomID id.RoomID, toolName string, next ToolHandler) (any, error) {
		// 从后向前构建中间件链
		for i := len(middlewares) - 1; i >= 0; i-- {
			// 捕获当前的 next 和 middleware
			currentNext := next
			middleware := middlewares[i]
			next = func(ctx context.Context) (any, error) {
				return middleware(ctx, userID, roomID, toolName, currentNext)
			}
		}
		return next(ctx)
	}
}
