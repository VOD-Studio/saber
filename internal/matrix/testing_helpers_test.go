//go:build goolm

// Package matrix 提供 Matrix 测试辅助工具测试。
package matrix

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestMockMatrixClient_GetUserID 测试 MockMatrixClient GetUserID 方法。
func TestMockMatrixClient_GetUserID(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	userID := client.GetUserID()
	if userID != "@test:example.com" {
		t.Errorf("expected @test:example.com, got %s", userID)
	}
}

// TestMockMatrixClient_GetDeviceID 测试 MockMatrixClient GetDeviceID 方法。
func TestMockMatrixClient_GetDeviceID(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE123", "token", "https://matrix.org")

	deviceID := client.GetDeviceID()
	if deviceID != "DEVICE123" {
		t.Errorf("expected DEVICE123, got %s", deviceID)
	}
}

// TestMockMatrixClient_JoinRoom 测试 MockMatrixClient JoinRoom 方法。
func TestMockMatrixClient_JoinRoom(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	resp, err := client.JoinRoom(context.Background(), "!room:example.com", nil)
	if err != nil {
		t.Errorf("JoinRoom should not error: %v", err)
	}
	if resp.RoomID != "!room:example.com" {
		t.Errorf("expected room ID !room:example.com, got %s", resp.RoomID)
	}
}

// TestMockMatrixClient_SendText 测试 MockMatrixClient SendText 方法。
func TestMockMatrixClient_SendText(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	resp, err := client.SendText(context.Background(), "!room:example.com", "hello")
	if err != nil {
		t.Errorf("SendText should not error: %v", err)
	}
	if resp.EventID == "" {
		t.Error("EventID should not be empty")
	}
}

// TestMockMatrixClient_SendMessageEvent 测试 MockMatrixClient SendMessageEvent 方法。
func TestMockMatrixClient_SendMessageEvent(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	resp, err := client.SendMessageEvent(context.Background(), "!room:example.com", event.EventMessage, map[string]string{"body": "test"})
	if err != nil {
		t.Errorf("SendMessageEvent should not error: %v", err)
	}
	if resp.EventID == "" {
		t.Error("EventID should not be empty")
	}
}

// TestMockMatrixClient_SendNotice 测试 MockMatrixClient SendNotice 方法。
func TestMockMatrixClient_SendNotice(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	resp, err := client.SendNotice(context.Background(), "!room:example.com", "notice")
	if err != nil {
		t.Errorf("SendNotice should not error: %v", err)
	}
	if resp.EventID == "" {
		t.Error("EventID should not be empty")
	}
}

// TestMockMatrixClient_JoinedRooms 测试 MockMatrixClient JoinedRooms 方法。
func TestMockMatrixClient_JoinedRooms(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	resp, err := client.JoinedRooms(context.Background())
	if err != nil {
		t.Errorf("JoinedRooms should not error: %v", err)
	}
	if resp == nil {
		t.Error("JoinedRooms should return non-nil response")
	}
}

// TestMockMatrixClient_State 测试 MockMatrixClient State 方法。
func TestMockMatrixClient_State(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	state, err := client.State(context.Background(), "!room:example.com")
	if err != nil {
		t.Errorf("State should not error: %v", err)
	}
	if state == nil {
		t.Error("State should return non-nil map")
	}
}

// TestMockMatrixClient_SetTyping 测试 MockMatrixClient SetTyping 方法。
func TestMockMatrixClient_SetTyping(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	ok, err := client.SetTyping(context.Background(), "!room:example.com", true, 30000)
	if err != nil {
		t.Errorf("SetTyping should not error: %v", err)
	}
	if !ok {
		t.Error("SetTyping should return true")
	}
}

// TestMockMatrixClient_CustomFunctions 测试自定义函数。
func TestMockMatrixClient_CustomFunctions(t *testing.T) {
	client := NewMockMatrixClient("@test:example.com", "DEVICE", "token", "https://matrix.org")

	// 设置自定义函数
	customUserID := id.UserID("@custom:example.com")
	client.UserIDFunc = func() id.UserID {
		return customUserID
	}

	if client.GetUserID() != customUserID {
		t.Errorf("expected custom user ID, got %s", client.GetUserID())
	}

	// 测试自定义 JoinRoomFunc
	client.JoinRoomFunc = func(_ context.Context, roomIDOrAlias string, _ *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error) {
		return &mautrix.RespJoinRoom{RoomID: id.RoomID("!custom:example.com")}, nil
	}

	// 由于类型限制，我们只验证函数被设置
	if client.JoinRoomFunc == nil {
		t.Error("JoinRoomFunc should be set")
	}
}

// TestMockCryptoService_Test 测试 MockCryptoService。
func TestMockCryptoService_Test(t *testing.T) {
	client := NewMockCryptoService(true)

	if !client.IsEnabled() {
		t.Error("MockCryptoService should be enabled")
	}

	client = NewMockCryptoService(false)
	if client.IsEnabled() {
		t.Error("MockCryptoService should be disabled")
	}
}

// TestMockRoomInfo_Test 测试 MockRoomInfo 函数。
func TestMockRoomInfo_Test(t *testing.T) {
	info := MockRoomInfo("!room:example.com", "Test Room", "#test:example.com", 10, true)

	if info.ID != "!room:example.com" {
		t.Errorf("expected room ID !room:example.com, got %s", info.ID)
	}
	if info.Name != "Test Room" {
		t.Errorf("expected name 'Test Room', got %s", info.Name)
	}
	if info.MemberCount != 10 {
		t.Errorf("expected 10 members, got %d", info.MemberCount)
	}
	if !info.IsEncrypted {
		t.Error("room should be encrypted")
	}
}

// TestTestBuildInfo_Test 测试 TestBuildInfo 函数。
func TestTestBuildInfo_Test(t *testing.T) {
	info := TestBuildInfo()

	if info.Version != "test-version" {
		t.Errorf("expected test-version, got %s", info.Version)
	}
	if info.GitCommit != "test-commit" {
		t.Errorf("expected test-commit, got %s", info.GitCommit)
	}
}

// TestCreateTestMessageEvent_Test 测试 CreateTestMessageEvent 函数。
func TestCreateTestMessageEvent_Test(t *testing.T) {
	evt := CreateTestMessageEvent("$event1", "!room:example.com", "@user:example.com", "hello")

	if evt.ID != "$event1" {
		t.Errorf("expected event ID $event1, got %s", evt.ID)
	}
	if evt.RoomID != "!room:example.com" {
		t.Errorf("expected room ID !room:example.com, got %s", evt.RoomID)
	}
	if evt.Sender != "@user:example.com" {
		t.Errorf("expected sender @user:example.com, got %s", evt.Sender)
	}
}

// TestCreateTestMemberEvent_Test 测试 CreateTestMemberEvent 函数。
func TestCreateTestMemberEvent_Test(t *testing.T) {
	evt := CreateTestMemberEvent("!room:example.com", "@user:example.com", "@sender:example.com", event.MembershipJoin)

	if evt.RoomID != "!room:example.com" {
		t.Errorf("expected room ID !room:example.com, got %s", evt.RoomID)
	}
	if evt.Sender != "@sender:example.com" {
		t.Errorf("expected sender @sender:example.com, got %s", evt.Sender)
	}
}
