//go:build goolm

package mcp

import (
	"sync"
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestNewRateLimiter(t *testing.T) {
	limiter := NewRateLimiter(60)
	if limiter.users == nil {
		t.Error("users map not initialized")
	}
	if limiter.rooms == nil {
		t.Error("rooms map not initialized")
	}
}

func TestRateLimiter_Allow(t *testing.T) {
	limiter := NewRateLimiter(10)
	userID := id.UserID("@test:example.com")
	roomID := id.RoomID("!room:example.com")

	if !limiter.Allow(userID, roomID) {
		t.Error("First call should be allowed")
	}
}

func TestRateLimiter_Allow_Concurrent(t *testing.T) {
	limiter := NewRateLimiter(100)
	userID := id.UserID("@test:example.com")
	roomID := id.RoomID("!room:example.com")

	var wg sync.WaitGroup
	allowed := make([]bool, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			allowed[idx] = limiter.Allow(userID, roomID)
		}(i)
	}
	wg.Wait()

	allowedCount := 0
	for _, a := range allowed {
		if a {
			allowedCount++
		}
	}

	if allowedCount == 0 {
		t.Error("At least one request should be allowed")
	}
}

func TestRateLimiter_MultipleUsers(t *testing.T) {
	limiter := NewRateLimiter(10)

	user1 := id.UserID("@user1:example.com")
	user2 := id.UserID("@user2:example.com")
	roomID := id.RoomID("!room:example.com")

	if !limiter.Allow(user1, roomID) {
		t.Error("User1 first call should be allowed")
	}
	if !limiter.Allow(user2, roomID) {
		t.Error("User2 first call should be allowed")
	}
}
