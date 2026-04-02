//go:build goolm

package matrix

import (
	"context"
	"testing"
)

// TestNewRoomService 测试创建房间服务。
func TestNewRoomService(t *testing.T) {
	matrixClient := &MatrixClient{}
	svc := NewRoomService(matrixClient)
	if svc == nil {
		t.Error("NewRoomService returned nil")
	}
}

// TestRoomInfo 测试 RoomInfo 结构体。
func TestRoomInfo(t *testing.T) {
	info := MockRoomInfo("!room:example.com", "Test Room", "#test:example.com", 10, true)

	if info.ID != "!room:example.com" {
		t.Errorf("ID = %q, want !room:example.com", info.ID)
	}
	if info.Name != "Test Room" {
		t.Errorf("Name = %q, want Test Room", info.Name)
	}
	if info.Alias != "#test:example.com" {
		t.Errorf("Alias = %q, want #test:example.com", info.Alias)
	}
	if info.MemberCount != 10 {
		t.Errorf("MemberCount = %d, want 10", info.MemberCount)
	}
	if !info.IsEncrypted {
		t.Error("IsEncrypted should be true")
	}
}

// TestRoomService_JoinRoom_InvalidID 测试无效房间 ID。
func TestRoomService_JoinRoom_InvalidID(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	tests := []struct {
		name          string
		roomIDOrAlias string
		wantErr       bool
	}{
		{"empty string", "", true},
		{"invalid format", "invalid-room-id", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rs.JoinRoom(context.Background(), tt.roomIDOrAlias)
			if (err != nil) != tt.wantErr {
				t.Errorf("JoinRoom(%q) error = %v, wantErr %v", tt.roomIDOrAlias, err, tt.wantErr)
			}
		})
	}
}

// TestRoomService_LeaveRoom_EmptyID 测试空房间 ID。
func TestRoomService_LeaveRoom_EmptyID(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	err := rs.LeaveRoom(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty room ID")
	}
}

// TestRoomService_SendMessage_EmptyInputs 测试空输入。
func TestRoomService_SendMessage_EmptyInputs(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	tests := []struct {
		name   string
		roomID string
		text   string
	}{
		{"empty room ID", "", "hello"},
		{"empty text", "!room:example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rs.SendMessage(context.Background(), tt.roomID, tt.text)
			if err == nil {
				t.Error("expected error for empty input")
			}
		})
	}
}

// TestRoomService_SendFormattedMessage_EmptyInputs 测试空输入。
func TestRoomService_SendFormattedMessage_EmptyInputs(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	tests := []struct {
		name   string
		roomID string
		html   string
		plain  string
	}{
		{"empty room ID", "", "<b>hi</b>", "hi"},
		{"empty html", "!room:example.com", "", "hi"},
		{"empty plain", "!room:example.com", "<b>hi</b>", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rs.SendFormattedMessage(context.Background(), tt.roomID, tt.html, tt.plain)
			if err == nil {
				t.Error("expected error for empty input")
			}
		})
	}
}

// TestRoomService_SendNotice_EmptyInputs 测试空输入。
func TestRoomService_SendNotice_EmptyInputs(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	tests := []struct {
		name   string
		roomID string
		text   string
	}{
		{"empty room ID", "", "notice"},
		{"empty text", "!room:example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rs.SendNotice(context.Background(), tt.roomID, tt.text)
			if err == nil {
				t.Error("expected error for empty input")
			}
		})
	}
}

// TestRoomService_GetRoomInfo_EmptyID 测试空房间 ID。
func TestRoomService_GetRoomInfo_EmptyID(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	_, err := rs.GetRoomInfo(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty room ID")
	}
}

// TestRoomService_SetLogger 测试设置日志器。
func TestRoomService_SetLogger(t *testing.T) {
	matrixClient := &MatrixClient{}
	rs := NewRoomService(matrixClient)

	// SetLogger 不应该 panic
	rs.SetLogger(nil)
}
