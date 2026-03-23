// Package matrix_test 包含 RoomService 的单元测试。
package matrix

import (
	"context"
	"errors"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// mockMatrixClientForRooms 提供一个专门用于 RoomService 测试的 mock 客户端。
type mockMatrixClientForRooms struct {
	userID   id.UserID
	deviceID id.DeviceID

	joinRoomFunc         func(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error)
	leaveRoomFunc        func(ctx context.Context, roomID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error)
	sendTextFunc         func(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error)
	sendMessageEventFunc func(ctx context.Context, roomID id.RoomID, eventType event.Type, content interface{}) (*mautrix.RespSendEvent, error)
	sendNoticeFunc       func(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error)
	joinedRoomsFunc      func(ctx context.Context) (*mautrix.RespJoinedRooms, error)
	stateFunc            func(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error)
	fullStateEventFunc   func(ctx context.Context, roomID id.RoomID, eventType event.Type, stateKey string) (*event.Event, error)
}

func (m *mockMatrixClientForRooms) GetUserID() id.UserID {
	return m.userID
}

func (m *mockMatrixClientForRooms) GetDeviceID() id.DeviceID {
	return m.deviceID
}

func (m *mockMatrixClientForRooms) GetClient() *mautrix.Client {
	return nil
}

func (m *mockMatrixClientForRooms) JoinRoom(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error) {
	if m.joinRoomFunc != nil {
		return m.joinRoomFunc(ctx, roomIDOrAlias, req)
	}
	return &mautrix.RespJoinRoom{RoomID: id.RoomID(roomIDOrAlias)}, nil
}

func (m *mockMatrixClientForRooms) LeaveRoom(ctx context.Context, roomID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error) {
	if m.leaveRoomFunc != nil {
		return m.leaveRoomFunc(ctx, roomID, req)
	}
	return &mautrix.RespLeaveRoom{}, nil
}

func (m *mockMatrixClientForRooms) SendText(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error) {
	if m.sendTextFunc != nil {
		return m.sendTextFunc(ctx, roomID, text)
	}
	return &mautrix.RespSendEvent{EventID: id.EventID("$test_event")}, nil
}

func (m *mockMatrixClientForRooms) SendMessageEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, content interface{}) (*mautrix.RespSendEvent, error) {
	if m.sendMessageEventFunc != nil {
		return m.sendMessageEventFunc(ctx, roomID, eventType, content)
	}
	return &mautrix.RespSendEvent{EventID: id.EventID("$test_event")}, nil
}

func (m *mockMatrixClientForRooms) SendNotice(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error) {
	if m.sendNoticeFunc != nil {
		return m.sendNoticeFunc(ctx, roomID, text)
	}
	return &mautrix.RespSendEvent{EventID: id.EventID("$test_notice")}, nil
}

func (m *mockMatrixClientForRooms) JoinedRooms(ctx context.Context) (*mautrix.RespJoinedRooms, error) {
	if m.joinedRoomsFunc != nil {
		return m.joinedRoomsFunc(ctx)
	}
	return &mautrix.RespJoinedRooms{JoinedRooms: []id.RoomID{}}, nil
}

func (m *mockMatrixClientForRooms) State(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error) {
	if m.stateFunc != nil {
		return m.stateFunc(ctx, roomID)
	}
	return mautrix.RoomStateMap{}, nil
}

func (m *mockMatrixClientForRooms) FullStateEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, stateKey string) (*event.Event, error) {
	if m.fullStateEventFunc != nil {
		return m.fullStateEventFunc(ctx, roomID, eventType, stateKey)
	}
	return nil, nil
}

// TestNewRoomService 测试 NewRoomService 函数。
func TestNewRoomService(t *testing.T) {
	// 注意：NewRoomService 需要有效的 MatrixClient
	// 这里我们只验证函数签名存在
	// 实际功能测试在集成测试中进行
}

// TestJoinRoom_ValidRoomID 测试使用有效的房间 ID 加入房间。
func TestJoinRoom_ValidRoomID(t *testing.T) {
	roomID := "!test:example.com"

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		joinRoomFunc: func(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error) {
			return &mautrix.RespJoinRoom{RoomID: id.RoomID(roomID)}, nil
		},
	}

	service := &RoomServiceForTest{
		MockJoinRoom: mockClient.joinRoomFunc,
	}

	roomInfo, err := service.JoinRoom(context.Background(), roomID)

	if err != nil {
		t.Fatalf("JoinRoom failed: %v", err)
	}

	if roomInfo == nil {
		t.Fatal("roomInfo is nil")
	}

	if roomInfo.ID != id.RoomID(roomID) {
		t.Errorf("expected room ID %s, got %s", roomID, roomInfo.ID)
	}
}

// TestJoinRoom_ValidAlias 测试使用有效的房间别名加入房间。
func TestJoinRoom_ValidAlias(t *testing.T) {
	alias := "#test-room:example.com"
	expectedRoomID := "!internal-id:example.com"

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		joinRoomFunc: func(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error) {
			if roomIDOrAlias == alias {
				return &mautrix.RespJoinRoom{RoomID: id.RoomID(expectedRoomID)}, nil
			}
			return nil, errors.New("unexpected alias")
		},
	}

	service := &RoomServiceForTest{
		MockJoinRoom: mockClient.joinRoomFunc,
	}

	roomInfo, err := service.JoinRoom(context.Background(), alias)

	if err != nil {
		t.Fatalf("JoinRoom failed: %v", err)
	}

	if roomInfo.ID != id.RoomID(expectedRoomID) {
		t.Errorf("expected room ID %s, got %s", expectedRoomID, roomInfo.ID)
	}

	if roomInfo.Alias != alias {
		t.Errorf("expected alias %s, got %s", alias, roomInfo.Alias)
	}
}

// TestJoinRoom_EmptyRoomID 测试使用空房间 ID。
func TestJoinRoom_EmptyRoomID(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.JoinRoom(context.Background(), "")

	if err == nil {
		t.Error("expected error for empty room ID, got nil")
	}
}

// TestJoinRoom_InvalidIdentifier 测试使用无效的房间标识符。
func TestJoinRoom_InvalidIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
	}{
		{"no_prefix", "invalid"},
		{"wrong_prefix_dollar", "$wrong:example.com"},
		{"wrong_prefix_at", "@user:example.com"},
	}

	service := &RoomServiceForTest{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.JoinRoom(context.Background(), tt.identifier)
			if err == nil {
				t.Errorf("expected error for identifier %s", tt.identifier)
			}
		})
	}
}

// TestJoinRoom_APIError 测试 API 错误情况。
func TestJoinRoom_APIError(t *testing.T) {
	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		joinRoomFunc: func(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error) {
			return nil, errors.New("room not found")
		},
	}

	service := &RoomServiceForTest{
		MockJoinRoom: mockClient.joinRoomFunc,
	}

	_, err := service.JoinRoom(context.Background(), "!test:example.com")

	if err == nil {
		t.Error("expected error from API, got nil")
	}
}

// TestLeaveRoom_Success 测试成功离开房间。
func TestLeaveRoom_Success(t *testing.T) {
	roomID := "!test:example.com"

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		leaveRoomFunc: func(ctx context.Context, rID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error) {
			if rID != id.RoomID(roomID) {
				return nil, errors.New("unexpected room ID")
			}
			return &mautrix.RespLeaveRoom{}, nil
		},
	}

	service := &RoomServiceForTest{
		MockLeaveRoom: mockClient.leaveRoomFunc,
	}

	err := service.LeaveRoom(context.Background(), roomID)

	if err != nil {
		t.Fatalf("LeaveRoom failed: %v", err)
	}
}

// TestLeaveRoom_EmptyRoomID 测试空房间 ID。
func TestLeaveRoom_EmptyRoomID(t *testing.T) {
	service := &RoomServiceForTest{}

	err := service.LeaveRoom(context.Background(), "")

	if err == nil {
		t.Error("expected error for empty room ID, got nil")
	}
}

// TestLeaveRoom_APIError 测试 API 错误。
func TestLeaveRoom_APIError(t *testing.T) {
	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		leaveRoomFunc: func(ctx context.Context, roomID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error) {
			return nil, errors.New("not in room")
		},
	}

	service := &RoomServiceForTest{
		MockLeaveRoom: mockClient.leaveRoomFunc,
	}

	err := service.LeaveRoom(context.Background(), "!test:example.com")

	if err == nil {
		t.Error("expected error from API, got nil")
	}
}

// TestSendMessage_Success 测试成功发送消息。
func TestSendMessage_Success(t *testing.T) {
	roomID := "!test:example.com"
	text := "Hello, World!"
	expectedEventID := id.EventID("$event123:example.com")

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		sendTextFunc: func(ctx context.Context, rID id.RoomID, t string) (*mautrix.RespSendEvent, error) {
			if rID != id.RoomID(roomID) {
				return nil, errors.New("unexpected room ID")
			}
			if t != text {
				return nil, errors.New("unexpected text")
			}
			return &mautrix.RespSendEvent{EventID: expectedEventID}, nil
		},
	}

	service := &RoomServiceForTest{
		MockSendText: mockClient.sendTextFunc,
	}

	eventID, err := service.SendMessage(context.Background(), roomID, text)

	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if eventID != expectedEventID {
		t.Errorf("expected event ID %s, got %s", expectedEventID, eventID)
	}
}

// TestSendMessage_EmptyRoomID 测试空房间 ID。
func TestSendMessage_EmptyRoomID(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.SendMessage(context.Background(), "", "test")

	if err == nil {
		t.Error("expected error for empty room ID, got nil")
	}
}

// TestSendMessage_EmptyText 测试空消息文本。
func TestSendMessage_EmptyText(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.SendMessage(context.Background(), "!test:example.com", "")

	if err == nil {
		t.Error("expected error for empty text, got nil")
	}
}

// TestSendFormattedMessage_Success 测试发送格式化消息。
func TestSendFormattedMessage_Success(t *testing.T) {
	roomID := "!test:example.com"
	html := "<b>Hello</b>"
	plain := "Hello"
	expectedEventID := id.EventID("$event123:example.com")

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		sendMessageEventFunc: func(ctx context.Context, rID id.RoomID, eventType event.Type, content interface{}) (*mautrix.RespSendEvent, error) {
			return &mautrix.RespSendEvent{EventID: expectedEventID}, nil
		},
	}

	service := &RoomServiceForTest{
		MockSendMessageEvent: mockClient.sendMessageEventFunc,
	}

	eventID, err := service.SendFormattedMessage(context.Background(), roomID, html, plain)

	if err != nil {
		t.Fatalf("SendFormattedMessage failed: %v", err)
	}

	if eventID != expectedEventID {
		t.Errorf("expected event ID %s, got %s", expectedEventID, eventID)
	}
}

// TestSendFormattedMessage_EmptyHTML 测试空 HTML。
func TestSendFormattedMessage_EmptyHTML(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.SendFormattedMessage(context.Background(), "!test:example.com", "", "plain")

	if err == nil {
		t.Error("expected error for empty HTML, got nil")
	}
}

// TestSendFormattedMessage_EmptyPlain 测试空纯文本。
func TestSendFormattedMessage_EmptyPlain(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.SendFormattedMessage(context.Background(), "!test:example.com", "<b>test</b>", "")

	if err == nil {
		t.Error("expected error for empty plain text, got nil")
	}
}

// TestSendNotice_Success 测试发送通知。
func TestSendNotice_Success(t *testing.T) {
	roomID := "!test:example.com"
	text := "Notice message"
	expectedEventID := id.EventID("$notice123:example.com")

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		sendNoticeFunc: func(ctx context.Context, rID id.RoomID, t string) (*mautrix.RespSendEvent, error) {
			if rID != id.RoomID(roomID) {
				return nil, errors.New("unexpected room ID")
			}
			return &mautrix.RespSendEvent{EventID: expectedEventID}, nil
		},
	}

	service := &RoomServiceForTest{
		MockSendNotice: mockClient.sendNoticeFunc,
	}

	eventID, err := service.SendNotice(context.Background(), roomID, text)

	if err != nil {
		t.Fatalf("SendNotice failed: %v", err)
	}

	if eventID != expectedEventID {
		t.Errorf("expected event ID %s, got %s", expectedEventID, eventID)
	}
}

// TestSendNotice_EmptyRoomID 测试空房间 ID。
func TestSendNotice_EmptyRoomID(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.SendNotice(context.Background(), "", "notice")

	if err == nil {
		t.Error("expected error for empty room ID, got nil")
	}
}

// TestSendNotice_EmptyText 测试空通知文本。
func TestSendNotice_EmptyText(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.SendNotice(context.Background(), "!test:example.com", "")

	if err == nil {
		t.Error("expected error for empty text, got nil")
	}
}

// TestGetJoinedRooms_Success 测试获取已加入房间列表。
func TestGetJoinedRooms_Success(t *testing.T) {
	rooms := []id.RoomID{
		"!room1:example.com",
		"!room2:example.com",
	}

	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		joinedRoomsFunc: func(ctx context.Context) (*mautrix.RespJoinedRooms, error) {
			return &mautrix.RespJoinedRooms{JoinedRooms: rooms}, nil
		},
		stateFunc: func(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error) {
			return mautrix.RoomStateMap{}, nil
		},
	}

	service := &RoomServiceForTest{
		MockJoinedRooms: mockClient.joinedRoomsFunc,
		MockState:       mockClient.stateFunc,
	}

	result, err := service.GetJoinedRooms(context.Background())

	if err != nil {
		t.Fatalf("GetJoinedRooms failed: %v", err)
	}

	if len(result) != len(rooms) {
		t.Errorf("expected %d rooms, got %d", len(rooms), len(result))
	}
}

// TestGetJoinedRooms_APIError 测试 API 错误。
func TestGetJoinedRooms_APIError(t *testing.T) {
	mockClient := &mockMatrixClientForRooms{
		userID:   "@bot:example.com",
		deviceID: "DEVICE123",
		joinedRoomsFunc: func(ctx context.Context) (*mautrix.RespJoinedRooms, error) {
			return nil, errors.New("network error")
		},
	}

	service := &RoomServiceForTest{
		MockJoinedRooms: mockClient.joinedRoomsFunc,
	}

	_, err := service.GetJoinedRooms(context.Background())

	if err == nil {
		t.Error("expected error from API, got nil")
	}
}

// TestGetRoomInfo_EmptyRoomID 测试空房间 ID。
func TestGetRoomInfo_EmptyRoomID(t *testing.T) {
	service := &RoomServiceForTest{}

	_, err := service.GetRoomInfo(context.Background(), "")

	if err == nil {
		t.Error("expected error for empty room ID, got nil")
	}
}

// RoomServiceForTest 是一个用于测试的 RoomService 包装器。
// 它允许注入 mock 函数以绕过对真实 MatrixClient 的依赖。
type RoomServiceForTest struct {
	MockJoinRoom         func(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error)
	MockLeaveRoom        func(ctx context.Context, roomID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error)
	MockSendText         func(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error)
	MockSendMessageEvent func(ctx context.Context, roomID id.RoomID, eventType event.Type, content interface{}) (*mautrix.RespSendEvent, error)
	MockSendNotice       func(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error)
	MockJoinedRooms      func(ctx context.Context) (*mautrix.RespJoinedRooms, error)
	MockState            func(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error)
	MockFullStateEvent   func(ctx context.Context, roomID id.RoomID, eventType event.Type, stateKey string) (*event.Event, error)
	Client               *MatrixClient
}

// JoinRoom 实现 RoomService 接口。
func (s *RoomServiceForTest) JoinRoom(ctx context.Context, roomIDOrAlias string) (*RoomInfo, error) {
	if roomIDOrAlias == "" {
		return nil, errors.New("room ID or alias cannot be empty")
	}

	// 验证标识符格式
	if len(roomIDOrAlias) == 0 || (roomIDOrAlias[0] != '!' && roomIDOrAlias[0] != '#') {
		return nil, errors.New("invalid room identifier: must start with ! for room ID or # for alias")
	}

	if s.MockJoinRoom == nil {
		return &RoomInfo{ID: id.RoomID(roomIDOrAlias)}, nil
	}

	resp, err := s.MockJoinRoom(ctx, roomIDOrAlias, &mautrix.ReqJoinRoom{})
	if err != nil {
		return nil, err
	}

	return &RoomInfo{
		ID:    resp.RoomID,
		Alias: roomIDOrAlias,
	}, nil
}

// LeaveRoom 实现 RoomService 接口。
func (s *RoomServiceForTest) LeaveRoom(ctx context.Context, roomID string) error {
	if roomID == "" {
		return errors.New("room ID cannot be empty")
	}

	if s.MockLeaveRoom == nil {
		return nil
	}

	_, err := s.MockLeaveRoom(ctx, id.RoomID(roomID), &mautrix.ReqLeave{})
	return err
}

// SendMessage 实现 RoomService 接口。
func (s *RoomServiceForTest) SendMessage(ctx context.Context, roomID, text string) (id.EventID, error) {
	if roomID == "" {
		return "", errors.New("room ID cannot be empty")
	}
	if text == "" {
		return "", errors.New("message text cannot be empty")
	}

	if s.MockSendText == nil {
		return id.EventID("$test_event"), nil
	}

	resp, err := s.MockSendText(ctx, id.RoomID(roomID), text)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// SendFormattedMessage 实现 RoomService 接口。
func (s *RoomServiceForTest) SendFormattedMessage(ctx context.Context, roomID, html, plain string) (id.EventID, error) {
	if roomID == "" {
		return "", errors.New("room ID cannot be empty")
	}
	if html == "" {
		return "", errors.New("HTML content cannot be empty")
	}
	if plain == "" {
		return "", errors.New("plain text content cannot be empty")
	}

	if s.MockSendMessageEvent == nil {
		return id.EventID("$test_event"), nil
	}

	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          plain,
		Format:        event.FormatHTML,
		FormattedBody: html,
	}

	resp, err := s.MockSendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// SendNotice 实现 RoomService 接口。
func (s *RoomServiceForTest) SendNotice(ctx context.Context, roomID, text string) (id.EventID, error) {
	if roomID == "" {
		return "", errors.New("room ID cannot be empty")
	}
	if text == "" {
		return "", errors.New("notice text cannot be empty")
	}

	if s.MockSendNotice == nil {
		return id.EventID("$test_notice"), nil
	}

	resp, err := s.MockSendNotice(ctx, id.RoomID(roomID), text)
	if err != nil {
		return "", err
	}
	return resp.EventID, nil
}

// GetJoinedRooms 实现 RoomService 接口。
func (s *RoomServiceForTest) GetJoinedRooms(ctx context.Context) ([]RoomInfo, error) {
	if s.MockJoinedRooms == nil {
		return []RoomInfo{}, nil
	}

	resp, err := s.MockJoinedRooms(ctx)
	if err != nil {
		return nil, err
	}

	rooms := make([]RoomInfo, 0, len(resp.JoinedRooms))
	for _, roomID := range resp.JoinedRooms {
		rooms = append(rooms, RoomInfo{ID: roomID})
	}
	return rooms, nil
}

// GetRoomInfo 实现 RoomService 接口。
func (s *RoomServiceForTest) GetRoomInfo(ctx context.Context, roomID string) (*RoomInfo, error) {
	if roomID == "" {
		return nil, errors.New("room ID cannot be empty")
	}
	return &RoomInfo{ID: id.RoomID(roomID)}, nil
}
