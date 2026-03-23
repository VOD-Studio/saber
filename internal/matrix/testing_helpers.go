// Package matrix 提供 Matrix 测试辅助工具。
package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// MockMatrixClient 提供一个模拟的 Matrix 客户端用于测试。
type MockMatrixClient struct {
	UserID      id.UserID
	DeviceID    id.DeviceID
	AccessToken string
	BaseURL     string

	// 可自定义的行为
	JoinRoomFunc         func(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error)
	LeaveRoomFunc        func(ctx context.Context, roomID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error)
	SendTextFunc         func(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error)
	SendMessageEventFunc func(ctx context.Context, roomID id.RoomID, eventType event.Type, content interface{}) (*mautrix.RespSendEvent, error)
	SendNoticeFunc       func(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error)
	JoinedRoomsFunc      func(ctx context.Context) (*mautrix.RespJoinedRooms, error)
	StateFunc            func(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error)
	FullStateEventFunc   func(ctx context.Context, roomID id.RoomID, eventType event.Type, stateKey string) (*event.Event, error)
	SetTypingFunc        func(ctx context.Context, roomID id.RoomID, typing bool, timeout int64) (bool, error)
	UserIDFunc           func() id.UserID
	DeviceIDFunc         func() id.DeviceID

	// 底层 mautrix.Client（可选）
	client *mautrix.Client
}

// NewMockMatrixClient 创建一个新的模拟 Matrix 客户端。
func NewMockMatrixClient(userID, deviceID, accessToken, homeserver string) *MockMatrixClient {
	return &MockMatrixClient{
		UserID:      id.UserID(userID),
		DeviceID:    id.DeviceID(deviceID),
		AccessToken: accessToken,
		BaseURL:     homeserver,
	}
}

// GetUserID 返回用户 ID。
func (m *MockMatrixClient) GetUserID() id.UserID {
	if m.UserIDFunc != nil {
		return m.UserIDFunc()
	}
	return m.UserID
}

// GetDeviceID 返回设备 ID。
func (m *MockMatrixClient) GetDeviceID() id.DeviceID {
	if m.DeviceIDFunc != nil {
		return m.DeviceIDFunc()
	}
	return m.DeviceID
}

// GetClient 返回底层的 mautrix.Client（如果设置）。
func (m *MockMatrixClient) GetClient() *mautrix.Client {
	return m.client
}

// SetClient 设置底层的 mautrix.Client。
func (m *MockMatrixClient) SetClient(c *mautrix.Client) {
	m.client = c
}

// JoinRoom 模拟加入房间。
func (m *MockMatrixClient) JoinRoom(ctx context.Context, roomIDOrAlias string, req *mautrix.ReqJoinRoom) (*mautrix.RespJoinRoom, error) {
	if m.JoinRoomFunc != nil {
		return m.JoinRoomFunc(ctx, roomIDOrAlias, req)
	}
	// 默认成功响应
	return &mautrix.RespJoinRoom{
		RoomID: id.RoomID(roomIDOrAlias),
	}, nil
}

// LeaveRoom 模拟离开房间。
func (m *MockMatrixClient) LeaveRoom(ctx context.Context, roomID id.RoomID, req *mautrix.ReqLeave) (*mautrix.RespLeaveRoom, error) {
	if m.LeaveRoomFunc != nil {
		return m.LeaveRoomFunc(ctx, roomID, req)
	}
	return &mautrix.RespLeaveRoom{}, nil
}

// SendText 模拟发送文本消息。
func (m *MockMatrixClient) SendText(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error) {
	if m.SendTextFunc != nil {
		return m.SendTextFunc(ctx, roomID, text)
	}
	return &mautrix.RespSendEvent{
		EventID: id.EventID("$test_event_id:" + m.UserID.Homeserver()),
	}, nil
}

// SendMessageEvent 模拟发送消息事件。
func (m *MockMatrixClient) SendMessageEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, content interface{}) (*mautrix.RespSendEvent, error) {
	if m.SendMessageEventFunc != nil {
		return m.SendMessageEventFunc(ctx, roomID, eventType, content)
	}
	return &mautrix.RespSendEvent{
		EventID: id.EventID("$test_event_id:" + m.UserID.Homeserver()),
	}, nil
}

// SendNotice 模拟发送通知消息。
func (m *MockMatrixClient) SendNotice(ctx context.Context, roomID id.RoomID, text string) (*mautrix.RespSendEvent, error) {
	if m.SendNoticeFunc != nil {
		return m.SendNoticeFunc(ctx, roomID, text)
	}
	return &mautrix.RespSendEvent{
		EventID: id.EventID("$test_notice_id:" + m.UserID.Homeserver()),
	}, nil
}

// JoinedRooms 模拟获取已加入的房间列表。
func (m *MockMatrixClient) JoinedRooms(ctx context.Context) (*mautrix.RespJoinedRooms, error) {
	if m.JoinedRoomsFunc != nil {
		return m.JoinedRoomsFunc(ctx)
	}
	return &mautrix.RespJoinedRooms{
		JoinedRooms: []id.RoomID{},
	}, nil
}

// State 模拟获取房间状态。
func (m *MockMatrixClient) State(ctx context.Context, roomID id.RoomID) (mautrix.RoomStateMap, error) {
	if m.StateFunc != nil {
		return m.StateFunc(ctx, roomID)
	}
	return make(mautrix.RoomStateMap), nil
}

// FullStateEvent 模拟获取完整状态事件。
func (m *MockMatrixClient) FullStateEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, stateKey string) (*event.Event, error) {
	if m.FullStateEventFunc != nil {
		return m.FullStateEventFunc(ctx, roomID, eventType, stateKey)
	}
	return nil, nil
}

// SetTyping 模拟设置打字状态。
func (m *MockMatrixClient) SetTyping(ctx context.Context, roomID id.RoomID, typing bool, timeout int64) (bool, error) {
	if m.SetTypingFunc != nil {
		return m.SetTypingFunc(ctx, roomID, typing, timeout)
	}
	return true, nil
}

// MockCryptoService 提供一个模拟的加密服务用于测试。
type MockCryptoService struct {
	Enabled      bool
	EncryptError error
	DecryptError error
}

// NewMockCryptoService 创建一个新的模拟加密服务。
func NewMockCryptoService(enabled bool) *MockCryptoService {
	return &MockCryptoService{Enabled: enabled}
}

// Init 模拟初始化加密服务。
func (m *MockCryptoService) Init(ctx context.Context) error {
	return nil
}

// Decrypt 模拟解密事件。
func (m *MockCryptoService) Decrypt(evt *event.Event) (*event.Event, error) {
	if m.DecryptError != nil {
		return nil, m.DecryptError
	}
	return evt, nil
}

// IsEnabled 返回加密是否启用。
func (m *MockCryptoService) IsEnabled() bool {
	return m.Enabled
}

// MockRoomInfo 创建一个测试用的房间信息。
func MockRoomInfo(roomID, name, alias string, memberCount int, encrypted bool) *RoomInfo {
	return &RoomInfo{
		ID:          id.RoomID(roomID),
		Name:        name,
		Alias:       alias,
		MemberCount: memberCount,
		IsEncrypted: encrypted,
	}
}

// TestBuildInfo 创建一个测试用的构建信息。
func TestBuildInfo() BuildInfo {
	return BuildInfo{
		Version:       "test-version",
		GitCommit:     "test-commit",
		GitBranch:     "test-branch",
		BuildTime:     "2024-01-01T00:00:00Z",
		GoVersion:     "go1.21.0",
		BuildPlatform: "test/platform",
	}
}

// mockHTTPClient 创建一个简单的 mock HTTP 客户端用于测试。
type mockHTTPClient struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewMockHTTPClient 创建一个 mock HTTP 客户端。
func NewMockHTTPClient(baseURL string) (*mockHTTPClient, error) {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	return &mockHTTPClient{
		baseURL:    parsedURL,
		httpClient: &http.Client{},
	}, nil
}

// createTestEvent 创建一个测试用的事件。
func createTestEvent(eventID, roomID, userID, msgType, body string) *event.Event {
	return &event.Event{
		ID:     id.EventID(eventID),
		RoomID: id.RoomID(roomID),
		Sender: id.UserID(userID),
		Type:   event.EventMessage,
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MessageType(msgType),
				Body:    body,
			},
		},
	}
}

// CreateTestMessageEvent 创建一个测试用的消息事件。
func CreateTestMessageEvent(eventID, roomID, userID, body string) *event.Event {
	return createTestEvent(eventID, roomID, userID, "m.text", body)
}

// CreateTestMemberEvent 创建一个测试用的成员事件。
func CreateTestMemberEvent(roomID, userID, senderID string, membership event.Membership) *event.Event {
	return &event.Event{
		ID:       id.EventID("$member_" + eventIDFromParts(roomID, userID)),
		RoomID:   id.RoomID(roomID),
		Sender:   id.UserID(senderID),
		Type:     event.StateMember,
		StateKey: ptr(userID),
		Content: event.Content{
			Parsed: &event.MemberEventContent{
				Membership: membership,
			},
		},
	}
}

func eventIDFromParts(parts ...string) string {
	data, _ := json.Marshal(parts)
	return string(data)
}

func ptr[T any](v T) *T {
	return &v
}

// GenerateTestEvents 生成测试事件列表。
//
// 用于基准测试时批量生成指定数量的测试事件。
//
// 参数:
//   - count: 事件数量
//   - roomID: 房间 ID
//   - senderID: 发送者 ID
//
// 返回值:
//   - []*event.Event: 生成的事件列表
func GenerateTestEvents(count int, roomID id.RoomID, senderID id.UserID) []*event.Event {
	events := make([]*event.Event, count)
	for i := 0; i < count; i++ {
		events[i] = &event.Event{
			ID:        id.EventID(fmt.Sprintf("$event%d", i)),
			RoomID:    roomID,
			Sender:    senderID,
			Type:      event.EventMessage,
			Timestamp: time.Now().UnixMilli(),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    fmt.Sprintf("Test message %d", i),
				},
			},
		}
	}
	return events
}

// TestRoomID 生成测试用房间 ID。
//
// 参数:
//   - n: 房间序号
//
// 返回值:
//   - id.RoomID: 格式为 !test{n}:example.com 的房间 ID
func TestRoomID(n int) id.RoomID {
	return id.RoomID(fmt.Sprintf("!test%d:example.com", n))
}

// TestUserID 生成测试用用户 ID。
//
// 参数:
//   - n: 用户序号
//
// 返回值:
//   - id.UserID: 格式为 @user{n}:example.com 的用户 ID
func TestUserID(n int) id.UserID {
	return id.UserID(fmt.Sprintf("@user%d:example.com", n))
}
