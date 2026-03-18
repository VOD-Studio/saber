//go:build goolm

package mcp

import (
	"context"
	"errors"
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

func TestValidateRoomID(t *testing.T) {
	tests := []struct {
		name    string
		roomID  id.RoomID
		wantErr bool
	}{
		{"valid room ID", "!abc123:matrix.org", false},
		{"valid room ID with dots", "!abc.def:matrix.org", false},
		{"valid room ID with dashes", "!abc-def:matrix.org", false},
		{"valid room ID with underscores", "!abc_def:matrix.org", false},
		{"empty room ID", "", true},
		{"missing prefix", "abc123:matrix.org", true},
		{"wrong prefix (@)", "@abc123:matrix.org", true},
		{"missing server", "!abc123:", true},
		{"invalid server (no domain)", "!abc123:localhost", false},
		{"missing localpart", "!:matrix.org", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRoomID(tt.roomID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRoomID(%q) error = %v, wantErr %v", tt.roomID, err, tt.wantErr)
			}
		})
	}
}

func TestValidateUserID(t *testing.T) {
	tests := []struct {
		name    string
		userID  id.UserID
		wantErr bool
	}{
		{"valid user ID", "@alice:matrix.org", false},
		{"valid user ID with dots", "@alice.bob:matrix.org", false},
		{"valid user ID with dashes", "@alice-bob:matrix.org", false},
		{"valid user ID with underscores", "@alice_bob:matrix.org", false},
		{"empty user ID", "", true},
		{"missing prefix", "alice:matrix.org", true},
		{"wrong prefix (!)", "!alice:matrix.org", true},
		{"missing server", "@alice:", true},
		{"invalid server (no domain)", "@alice:localhost", false},
		{"missing localpart", "@:matrix.org", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateUserID(tt.userID)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateUserID(%q) error = %v, wantErr %v", tt.userID, err, tt.wantErr)
			}
		})
	}
}

func TestRateLimitMiddleware(t *testing.T) {
	limiter := NewRateLimiter(1)
	middleware := RateLimitMiddleware(limiter)

	userID := id.UserID("@test:example.com")
	roomID := id.RoomID("!room:example.com")

	handler := func(ctx context.Context) (any, error) {
		return "success", nil
	}

	ctx := context.Background()

	result, err := middleware(ctx, userID, roomID, "test_tool", handler)
	if err != nil {
		t.Errorf("First call should succeed: %v", err)
	}
	if result != "success" {
		t.Errorf("Result = %v, want 'success'", result)
	}
}

func TestValidationMiddleware(t *testing.T) {
	middleware := ValidationMiddleware()

	handler := func(ctx context.Context) (any, error) {
		return "success", nil
	}

	tests := []struct {
		name    string
		userID  id.UserID
		roomID  id.RoomID
		wantErr bool
	}{
		{"valid IDs", "@alice:matrix.org", "!room:matrix.org", false},
		{"invalid user ID", "invalid", "!room:matrix.org", true},
		{"invalid room ID", "@alice:matrix.org", "invalid", true},
		{"both invalid", "invalid", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, err := middleware(ctx, tt.userID, tt.roomID, "test_tool", handler)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidationMiddleware() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChainMiddleware(t *testing.T) {
	callOrder := []string{}
	var mu sync.Mutex

	recordCall := func(name string) {
		mu.Lock()
		callOrder = append(callOrder, name)
		mu.Unlock()
	}

	m1 := func(ctx context.Context, userID id.UserID, roomID id.RoomID, toolName string, next ToolHandler) (any, error) {
		recordCall("m1-before")
		result, err := next(ctx)
		recordCall("m1-after")
		return result, err
	}

	m2 := func(ctx context.Context, userID id.UserID, roomID id.RoomID, toolName string, next ToolHandler) (any, error) {
		recordCall("m2-before")
		result, err := next(ctx)
		recordCall("m2-after")
		return result, err
	}

	handler := func(ctx context.Context) (any, error) {
		recordCall("handler")
		return "success", nil
	}

	chain := ChainMiddleware(m1, m2)
	ctx := context.Background()

	result, err := chain(ctx, "@user:example.com", "!room:example.com", "test_tool", handler)
	if err != nil {
		t.Errorf("ChainMiddleware() error = %v", err)
	}
	if result != "success" {
		t.Errorf("Result = %v, want 'success'", result)
	}

	expected := []string{"m1-before", "m2-before", "handler", "m2-after", "m1-after"}
	if len(callOrder) != len(expected) {
		t.Errorf("Call order length = %d, want %d", len(callOrder), len(expected))
	}
	for i, got := range callOrder {
		if got != expected[i] {
			t.Errorf("Call order[%d] = %q, want %q", i, got, expected[i])
		}
	}
}

func TestChainMiddleware_Empty(t *testing.T) {
	handler := func(ctx context.Context) (any, error) {
		return "success", nil
	}

	chain := ChainMiddleware()
	ctx := context.Background()

	result, err := chain(ctx, "@user:example.com", "!room:example.com", "test_tool", handler)
	if err != nil {
		t.Errorf("Empty chain should just call handler: %v", err)
	}
	if result != "success" {
		t.Errorf("Result = %v, want 'success'", result)
	}
}

func TestChainMiddleware_ErrorPropagation(t *testing.T) {
	expectedErr := errors.New("handler error")

	handler := func(ctx context.Context) (any, error) {
		return nil, expectedErr
	}

	chain := ChainMiddleware()
	ctx := context.Background()

	_, err := chain(ctx, "@user:example.com", "!room:example.com", "test_tool", handler)
	if err != expectedErr {
		t.Errorf("Error should propagate: got %v, want %v", err, expectedErr)
	}
}
