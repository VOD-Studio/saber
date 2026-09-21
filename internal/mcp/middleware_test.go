//go:build goolm

package mcp

import (
	"sync"
	"testing"

	"rua.plus/saber/internal/chat"
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
	userID := string("@test:example.com")
	roomID := string("!room:example.com")

	if !limiter.Allow(userID, roomID) {
		t.Error("First call should be allowed")
	}
}

func TestRateLimiter_Allow_Concurrent(t *testing.T) {
	limiter := NewRateLimiter(100)
	userID := string("@test:example.com")
	roomID := string("!room:example.com")

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

	user1 := string("@user1:example.com")
	user2 := string("@user2:example.com")
	roomID := string("!room:example.com")

	if !limiter.Allow(user1, roomID) {
		t.Error("User1 first call should be allowed")
	}
	if !limiter.Allow(user2, roomID) {
		t.Error("User2 first call should be allowed")
	}
}

func TestRateLimiter_ChatIdentityIsolation(t *testing.T) {
	limiter := NewRateLimiter(1)
	identities := []chat.Identity{
		{Session: chat.Session{Platform: "matrix", Account: "a", Conversation: "same"}, SenderID: "same"},
		{Session: chat.Session{Platform: "memory", Account: "a", Conversation: "same"}, SenderID: "same"},
		{Session: chat.Session{Platform: "matrix", Account: "b", Conversation: "same"}, SenderID: "same"},
	}
	for _, identity := range identities {
		if !limiter.Allow(identity.UserKey(), string(identity.Session.Key())) {
			t.Fatalf("another platform/account consumed this budget: %+v", identity)
		}
		if limiter.Allow(identity.UserKey(), string(identity.Session.Key())) {
			t.Fatal("same source bypassed limit")
		}
	}
}
