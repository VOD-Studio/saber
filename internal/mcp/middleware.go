// Package mcp 提供 MCP (Model Context Protocol) 集成功能。
package mcp

import (
	"sync"

	"golang.org/x/time/rate"
)

// RateLimiter 提供基于用户和房间的速率限制。
//
// 它为每个用户和每个房间维护独立的令牌桶限流器，
// 防止单个用户或房间过度使用 MCP 工具。
type RateLimiter struct {
	mu    sync.RWMutex
	users map[string]*rate.Limiter
	rooms map[string]*rate.Limiter
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
		users: make(map[string]*rate.Limiter),
		rooms: make(map[string]*rate.Limiter),
		limit: rate.Limit(float64(callsPerMinute) / 60.0),
		burst: callsPerMinute,
	}
}

// Allow 检查已带平台和账号作用域的用户键与会话键。
//
// 返回 true 表示调用被允许，false 表示已达到速率限制。
// 用户和房间的限制是独立的，两者都必须通过才能允许调用。
func (r *RateLimiter) Allow(userID string, roomID string) bool {
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
func (r *RateLimiter) getOrCreateUserLimiter(userID string) *rate.Limiter {
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
func (r *RateLimiter) getOrCreateRoomLimiter(roomID string) *rate.Limiter {
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
