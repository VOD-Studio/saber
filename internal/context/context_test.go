package context

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestWithValueAndGetValue(t *testing.T) {
	ctx := context.Background()

	// 测试字符串类型
	key := Key[string]{name: "testKey"}
	ctx = WithValue(ctx, key, "testValue")

	got, ok := GetValue(ctx, key)
	if !ok {
		t.Error("GetValue should find the value")
	}
	if got != "testValue" {
		t.Errorf("GetValue = %q, want %q", got, "testValue")
	}

	// 测试不存在的键
	_, ok = GetValue(ctx, Key[int]{name: "nonExistent"})
	if ok {
		t.Error("GetValue should return false for non-existent key")
	}
}

func TestWithUserContext(t *testing.T) {
	ctx := context.Background()
	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")

	ctx = WithUserContext(ctx, userID, roomID)

	// 验证用户 ID
	gotUserID, ok := GetUserFromContext(ctx)
	if !ok {
		t.Error("GetUserFromContext should find the userID")
	}
	if gotUserID != userID {
		t.Errorf("GetUserFromContext = %q, want %q", gotUserID, userID)
	}

	// 验证房间 ID
	gotRoomID, ok := GetRoomFromContext(ctx)
	if !ok {
		t.Error("GetRoomFromContext should find the roomID")
	}
	if gotRoomID != roomID {
		t.Errorf("GetRoomFromContext = %q, want %q", gotRoomID, roomID)
	}
}

func TestGetUserFromContext_NotFound(t *testing.T) {
	ctx := context.Background()

	_, ok := GetUserFromContext(ctx)
	if ok {
		t.Error("GetUserFromContext should return false for empty context")
	}
}

func TestGetRoomFromContext_NotFound(t *testing.T) {
	ctx := context.Background()

	_, ok := GetRoomFromContext(ctx)
	if ok {
		t.Error("GetRoomFromContext should return false for empty context")
	}
}

func TestMultipleContextValues(t *testing.T) {
	ctx := context.Background()

	userID := id.UserID("@user:example.com")
	roomID := id.RoomID("!room:example.com")

	// 设置多个值
	ctx = WithUserContext(ctx, userID, roomID)

	// 验证所有值都能正确获取
	gotUserID, ok := GetUserFromContext(ctx)
	if !ok || gotUserID != userID {
		t.Errorf("UserID not preserved correctly")
	}

	gotRoomID, ok := GetRoomFromContext(ctx)
	if !ok || gotRoomID != roomID {
		t.Errorf("RoomID not preserved correctly")
	}
}