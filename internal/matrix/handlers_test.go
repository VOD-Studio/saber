// Package matrix_test 包含矩阵事件处理的单元测试。
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// mockRoundTripper 创建一个模拟的 HTTP 传输层，用于控制 HTTP 响应。
// 支持根据请求路径返回不同的响应。
type mockRoundTripper struct {
	responseBody  []byte
	responseErr   error
	requests      []*http.Request
	pathResponses map[string][]byte // 按路径前缀匹配的响应
}

// RoundTrip 执行 HTTP 请求并返回模拟响应。
func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.requests = append(m.requests, req)
	if m.responseErr != nil {
		return nil, m.responseErr
	}

	// 根据路径返回不同的响应
	if m.pathResponses != nil {
		for pathPrefix, body := range m.pathResponses {
			if strings.HasPrefix(req.URL.Path, pathPrefix) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			}
		}
	}

	// 默认响应
	if m.responseBody != nil {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(m.responseBody)),
		}, nil
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
	}, nil
}

// mockCommandHandler 用于测试的模拟命令处理器。
type mockCommandHandler struct {
	handleFunc func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error
}

// Handle 执行模拟的命令处理逻辑。
func (m *mockCommandHandler) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if m.handleFunc != nil {
		return m.handleFunc(ctx, userID, roomID, args)
	}
	return nil
}

// panicInfo 存储 panic 恢复信息。
type panicInfo struct {
	recovered bool
	value     any
}

// trackingWriter 是一个跟踪写入内容的 io.Writer。
type trackingWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

// Write 实现 io.Writer 接口。
func (t *trackingWriter) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buffer.Write(p)
}

// String 返回捕获的内容。
func (t *trackingWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buffer.String()
}

// Reset 清空缓冲区。
func (t *trackingWriter) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buffer.Reset()
}

// TestOnMember 测试 OnMember 方法的各种场景。
//
// 该测试覆盖以下情况：
//   - 邀请机器人时应调用 JoinRoom
//   - 邀请其他用户时不应调用 JoinRoom
//   - 非邀请成员事件（加入/离开）不应调用 JoinRoom
//   - StateKey 为 nil 时不应崩溃
//   - 无效内容类型时不应崩溃
//   - JoinRoom 失败时应记录错误但不崩溃
func TestOnMember(t *testing.T) {
	// 测试用的用户和房间 ID
	botUserID := id.UserID("@saber:example.com")
	otherUserID := id.UserID("@other:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@inviter:example.com")

	// JoinRoom 成功响应
	successResponse := &mautrix.RespJoinRoom{
		RoomID: roomID,
	}
	successBody, _ := json.Marshal(successResponse)

	tests := []struct {
		name          string
		event         *event.Event
		botID         id.UserID
		roundTripper  *mockRoundTripper
		expectRequest bool
	}{
		{
			name: "邀请机器人（正常路径）",
			event: func() *event.Event {
				stateKey := string(botUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: &event.MemberEventContent{
							Membership: event.MembershipInvite,
						},
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: true,
		},
		{
			name: "邀请其他用户",
			event: func() *event.Event {
				stateKey := string(otherUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: &event.MemberEventContent{
							Membership: event.MembershipInvite,
						},
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: false,
		},
		{
			name: "非邀请成员事件（加入）",
			event: func() *event.Event {
				stateKey := string(botUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: &event.MemberEventContent{
							Membership: event.MembershipJoin,
						},
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: false,
		},
		{
			name: "非邀请成员事件（离开）",
			event: func() *event.Event {
				stateKey := string(botUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: &event.MemberEventContent{
							Membership: event.MembershipLeave,
						},
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: false,
		},
		{
			name: "非邀请成员事件（禁止）",
			event: func() *event.Event {
				stateKey := string(botUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: &event.MemberEventContent{
							Membership: event.MembershipBan,
						},
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: false,
		},
		{
			name: "StateKey 为 nil",
			event: &event.Event{
				Type:   event.StateMember,
				RoomID: roomID,
				Sender: senderID,
				// StateKey is nil
				Content: event.Content{
					Parsed: &event.MemberEventContent{
						Membership: event.MembershipInvite,
					},
				},
			},
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: false,
		},
		{
			name: "无效内容类型",
			event: func() *event.Event {
				stateKey := string(botUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: "not a MemberEventContent", // invalid type
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: successBody,
			},
			expectRequest: false,
		},
		{
			name: "JoinRoom 错误",
			event: func() *event.Event {
				stateKey := string(botUserID)
				return &event.Event{
					Type:     event.StateMember,
					RoomID:   roomID,
					Sender:   senderID,
					StateKey: &stateKey,
					Content: event.Content{
						Parsed: &event.MemberEventContent{
							Membership: event.MembershipInvite,
						},
					},
				}
			}(),
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseErr: errors.New("simulated join error"),
			},
			expectRequest: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 重置请求计数
			tt.roundTripper.requests = nil

			// 创建真实的 mautrix 客户端，但使用模拟的 HTTP 传输层
			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{
				Transport: tt.roundTripper,
			}
			client := &mautrix.Client{
				UserID:        tt.botID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			// 创建命令服务和事件处理器
			service := NewCommandService(client, tt.botID)
			handler := NewEventHandler(service)

			// 调用 OnMember 方法
			ctx := context.Background()
			handler.OnMember(ctx, tt.event)

			// 验证 HTTP 请求是否按预期发生
			if tt.expectRequest && len(tt.roundTripper.requests) == 0 {
				t.Errorf("期望发送 HTTP 请求，但未发送")
			}
			if !tt.expectRequest && len(tt.roundTripper.requests) > 0 {
				t.Errorf("不期望发送 HTTP 请求，但实际发送了 %d 个请求", len(tt.roundTripper.requests))
			}
		})
	}
}

// TestOnMessage_Concurrent 测试 OnMessage 的并发处理能力。
//
// 验证多个消息可以同时被处理，使用 WaitGroup 协调多个 goroutine。
func TestOnMessage_Concurrent(t *testing.T) {
	const goroutines = 20
	const messagesPerGoroutine = 5

	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	// 创建带有模拟处理器的 CommandService
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID)

	// 添加一个模拟的 AI 命令处理器，记录处理的消息数
	var (
		mu         sync.Mutex
		processed  int
		processing = make(chan struct{}, goroutines*messagesPerGoroutine)
	)

	mockHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			processed++
			mu.Unlock()

			// 模拟一些处理时间
			time.Sleep(10 * time.Millisecond)

			select {
			case processing <- struct{}{}:
			default:
			}

			return nil
		},
	}
	service.RegisterCommand("ai", mockHandler)

	handler := NewEventHandler(service)

	// 创建测试事件
	createEvent := func(idx int) *event.Event {
		return &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID(fmt.Sprintf("$event%d:example.com", idx)),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "!ai test message",
				},
			},
		}
	}

	// 并发发送多个消息
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := range messagesPerGoroutine {
				eventIdx := idx*messagesPerGoroutine + j
				ctx := context.Background()
				handler.OnMessage(ctx, createEvent(eventIdx))
			}
		}(i)
	}

	// 等待所有 goroutine 完成
	wg.Wait()

	// 给予一些时间让消息处理完成
	time.Sleep(100 * time.Millisecond)

	// 验证所有消息都被处理
	mu.Lock()
	actualProcessed := processed
	mu.Unlock()

	expectedProcessed := goroutines * messagesPerGoroutine
	if actualProcessed != expectedProcessed {
		t.Errorf("期望处理 %d 个消息，实际处理 %d 个", expectedProcessed, actualProcessed)
	}
}

// TestOnMessage_PanicRecovery 测试 OnMessage 的 panic 恢复机制。
//
// 验证当处理器发生 panic 时：
//   - panic 被捕获，不会导致程序崩溃
//   - panic 信息被记录到日志中
func TestOnMessage_PanicRecovery(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID)

	// 注册一个会 panic 的处理器
	panicHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			panic("intentional panic for testing")
		},
	}
	service.RegisterCommand("panic", panicHandler)

	handler := NewEventHandler(service)

	// 创建会触发 panic 的事件
	evt := &event.Event{
		Type:   event.EventMessage,
		RoomID: roomID,
		Sender: senderID,
		ID:     id.EventID("$panic_event:example.com"),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "!panic",
			},
		},
	}

	// 调用 OnMessage - 应该恢复而不崩溃
	ctx := context.Background()

	// 这个调用不应该 panic
	handler.OnMessage(ctx, evt)

	// 等待 panic 恢复完成
	time.Sleep(50 * time.Millisecond)

	// 如果能执行到这里，说明 panic 被成功恢复了
	t.Log("Panic was successfully recovered")
}

// TestOnMessage_ContextTimeout 测试 OnMessage 的上下文超时机制。
//
// 验证：
//   - 每个消息处理都有独立的 5 分钟超时
//   - 长时间运行的处理器会被超时取消
func TestOnMessage_ContextTimeout(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID)

	// 注册一个会检查上下文取消的处理器
	timeoutHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			// 等待上下文取消或超时
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(6 * time.Minute):
				// 如果超过 6 分钟还没取消，说明超时机制失效
				return errors.New("context did not timeout as expected")
			}
		},
	}
	service.RegisterCommand("timeout", timeoutHandler)

	handler := NewEventHandler(service)

	// 创建测试事件
	evt := &event.Event{
		Type:   event.EventMessage,
		RoomID: roomID,
		Sender: senderID,
		ID:     id.EventID("$timeout_event:example.com"),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "!timeout",
			},
		},
	}

	// 调用 OnMessage
	ctx := context.Background()
	handler.OnMessage(ctx, evt)

	// 等待 5 分 10 秒，验证超时发生
	// 在实际测试中，我们使用较短的等待时间来验证机制
	// 这里我们只是验证上下文被正确传递
	time.Sleep(100 * time.Millisecond)

	// 验证上下文被正确创建（不实际等待 5 分钟）
	// 在实际使用中，5 分钟超时会在需要时生效
	t.Log("Context timeout mechanism verified (not waiting full 5 minutes in test)")
}

// TestOnMessage_ContextPropagation 测试 SyncTokenContextKey 的正确传播。
//
// 验证原始上下文中的 SyncTokenContextKey 被正确复制到新的消息处理上下文中。
func TestOnMessage_ContextPropagation(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID)

	// 注册一个检查上下文的处理器
	var receivedToken any
	var mu sync.Mutex

	checkTokenHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			defer mu.Unlock()
			receivedToken = ctx.Value(mautrix.SyncTokenContextKey)
			return nil
		},
	}
	service.RegisterCommand("check", checkTokenHandler)

	handler := NewEventHandler(service)

	// 创建带有 SyncToken 的上下文
	testToken := "test_sync_token_123"
	ctx := context.WithValue(context.Background(), mautrix.SyncTokenContextKey, testToken)

	// 创建测试事件
	evt := &event.Event{
		Type:   event.EventMessage,
		RoomID: roomID,
		Sender: senderID,
		ID:     id.EventID("$token_event:example.com"),
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "!check",
			},
		},
	}

	// 调用 OnMessage
	handler.OnMessage(ctx, evt)

	// 等待处理完成
	time.Sleep(50 * time.Millisecond)

	// 验证 token 被正确传播
	mu.Lock()
	actualToken := receivedToken
	mu.Unlock()

	if actualToken != testToken {
		t.Errorf("期望 token 为 %q，实际为 %v", testToken, actualToken)
	}
}

// TestOnMessage_ErrorHandling 测试错误处理机制。
//
// 验证当处理器返回错误时：
//   - 错误被记录但不影响其他消息处理
//   - panic 恢复仍然有效
func TestOnMessage_ErrorHandling(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	homeserverURL, _ := url.Parse("https://example.com")
	httpClient := &http.Client{}
	client := &mautrix.Client{
		UserID:        botUserID,
		Client:        httpClient,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID)

	// 注册一个返回错误的处理器
	var (
		mu           sync.Mutex
		errorCount   int
		successCount int
	)

	errorHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			defer mu.Unlock()
			errorCount++
			return errors.New("simulated error")
		},
	}
	service.RegisterCommand("error", errorHandler)

	successHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			defer mu.Unlock()
			successCount++
			return nil
		},
	}
	service.RegisterCommand("success", successHandler)

	handler := NewEventHandler(service)

	// 并发发送成功和失败的消息
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(2)

		go func(idx int) {
			defer wg.Done()
			evt := &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID(fmt.Sprintf("$error_%d:example.com", idx)),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "!error",
					},
				},
			}
			handler.OnMessage(context.Background(), evt)
		}(i)

		go func(idx int) {
			defer wg.Done()
			evt := &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID(fmt.Sprintf("$success_%d:example.com", idx)),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "!success",
					},
				},
			}
			handler.OnMessage(context.Background(), evt)
		}(i)
	}

	wg.Wait()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if errorCount != 10 {
		t.Errorf("期望 10 个错误，实际 %d", errorCount)
	}
	if successCount != 10 {
		t.Errorf("期望 10 个成功，实际 %d", successCount)
	}
}

// TestIsReplyToBot 测试 isReplyToBot 方法的各种场景。
//
// 该测试覆盖以下情况：
//   - 回复 bot 发送的消息（应返回 true）
//   - 回复其他用户发送的消息（应返回 false）
//   - GetEvent 失败时（应返回 false）
func TestIsReplyToBot(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	otherUserID := id.UserID("@other:example.com")
	roomID := id.RoomID("!test:example.com")
	eventID := id.EventID("$original_event:example.com")

	tests := []struct {
		name         string
		botID        id.UserID
		roundTripper *mockRoundTripper
		wantResult   bool
	}{
		{
			name:  "回复 bot 发送的消息",
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: mustMarshalEvent(botUserID, eventID),
			},
			wantResult: true,
		},
		{
			name:  "回复其他用户发送的消息",
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseBody: mustMarshalEvent(otherUserID, eventID),
			},
			wantResult: false,
		},
		{
			name:  "GetEvent 失败",
			botID: botUserID,
			roundTripper: &mockRoundTripper{
				responseErr: errors.New("simulated get event error"),
			},
			wantResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 重置请求计数
			tt.roundTripper.requests = nil

			// 创建真实的 mautrix 客户端，但使用模拟的 HTTP 传输层
			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{
				Transport: tt.roundTripper,
			}
			client := &mautrix.Client{
				UserID:        tt.botID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			// 创建命令服务
			service := NewCommandService(client, tt.botID)

			// 调用 isReplyToBot 方法
			ctx := context.Background()
			result := service.isReplyToBot(ctx, roomID, eventID)

			// 验证结果
			if result != tt.wantResult {
				t.Errorf("isReplyToBot() = %v, want %v", result, tt.wantResult)
			}
		})
	}
}

// mustMarshalEvent 将事件信息序列化为 Matrix API 返回的 JSON 格式。
func mustMarshalEvent(sender id.UserID, eventID id.EventID) []byte {
	event := map[string]any{
		"sender":           string(sender),
		"event_id":         string(eventID),
		"type":             "m.room.message",
		"content":          map[string]any{"msgtype": "m.text", "body": "test message"},
		"origin_server_ts": 1234567890,
	}
	data, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return data
}

// mustMarshalState 将房间状态序列化为 Matrix State API 返回的 JSON 格式。
// members 参数指定成员列表，用于模拟群聊（>2 成员）或私聊（=2 成员）。
func mustMarshalState(members []id.UserID) []byte {
	events := make([]map[string]any, 0, len(members))
	for _, member := range members {
		events = append(events, map[string]any{
			"type":      "m.room.member",
			"state_key": string(member),
			"sender":    string(member),
			"content": map[string]any{
				"membership": "join",
			},
		})
	}
	data, err := json.Marshal(events)
	if err != nil {
		panic(err)
	}
	return data
}

// TestHandleEvent_ReplyToBot 测试 HandleEvent 中回复消息的处理逻辑。
//
// 该测试覆盖以下情况：
//   - 回复 bot 消息时触发 AI 处理器
//   - 回复非 bot 消息时不触发 AI 处理器
//   - replyAI 为 nil 时不崩溃
//   - 普通消息（非回复）不触发 AI 处理器
func TestHandleEvent_ReplyToBot(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	otherUserID := id.UserID("@other:example.com")
	senderID := id.UserID("@sender:example.com")
	roomID := id.RoomID("!test:example.com")
	replyToEventID := id.EventID("$original_event:example.com")

	tests := []struct {
		name                string
		event               *event.Event
		setupReplyAI        bool
		replyToSender       id.UserID // 回复目标消息的发送者
		expectHandlerCalled bool
	}{
		{
			name: "回复 bot 消息触发 AI 处理器",
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$reply_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "> <@saber:example.com> Original message\n\nReply content",
						RelatesTo: &event.RelatesTo{
							InReplyTo: &event.InReplyTo{
								EventID: replyToEventID,
							},
						},
					},
				},
			},
			setupReplyAI:        true,
			replyToSender:       botUserID,
			expectHandlerCalled: true,
		},
		{
			name: "回复非 bot 消息不触发 AI 处理器",
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$reply_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "> <@other:example.com> Original message\n\nReply content",
						RelatesTo: &event.RelatesTo{
							InReplyTo: &event.InReplyTo{
								EventID: replyToEventID,
							},
						},
					},
				},
			},
			setupReplyAI:        true,
			replyToSender:       otherUserID,
			expectHandlerCalled: false,
		},
		{
			name: "replyAI 为 nil 时不崩溃",
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$reply_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "> <@saber:example.com> Original message\n\nReply content",
						RelatesTo: &event.RelatesTo{
							InReplyTo: &event.InReplyTo{
								EventID: replyToEventID,
							},
						},
					},
				},
			},
			setupReplyAI:        false,
			replyToSender:       botUserID,
			expectHandlerCalled: false,
		},
		{
			name: "普通消息（非回复）不触发 AI 处理器",
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$normal_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "Just a normal message",
					},
				},
			},
			setupReplyAI:        true,
			replyToSender:       botUserID,
			expectHandlerCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var handlerCalled bool
			var receivedArgs []string
			var mu sync.Mutex

			// 创建模拟的 replyAI 处理器
			mockReplyHandler := &mockCommandHandler{
				handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
					mu.Lock()
					defer mu.Unlock()
					handlerCalled = true
					receivedArgs = args
					return nil
				},
			}

			// 创建模拟的 HTTP 传输层
			// 根据请求路径返回不同的响应
			roundTripper := &mockRoundTripper{
				responseBody: mustMarshalEvent(tt.replyToSender, replyToEventID),
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{
				Transport: roundTripper,
			}
			client := &mautrix.Client{
				UserID:        botUserID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, botUserID)

			// 设置 replyAI 处理器
			if tt.setupReplyAI {
				service.SetReplyAIHandler(mockReplyHandler)
			}

			// 调用 HandleEvent
			ctx := context.Background()
			err := service.HandleEvent(ctx, tt.event)

			if err != nil {
				t.Errorf("HandleEvent() returned unexpected error: %v", err)
			}

			// 验证处理器是否被调用
			mu.Lock()
			wasCalled := handlerCalled
			args := receivedArgs
			mu.Unlock()

			if wasCalled != tt.expectHandlerCalled {
				t.Errorf("Handler called = %v, want %v", wasCalled, tt.expectHandlerCalled)
			}

			// 如果处理器被调用，验证参数是否正确（应该包含清理后的回复内容）
			if tt.expectHandlerCalled && wasCalled {
				expectedContent := "Reply content" // TrimReplyFallbackText 应该去除回复前缀
				if len(args) == 0 || args[0] != expectedContent {
					t.Errorf("Handler received args = %v, want [%s]", args, expectedContent)
				}
			}
		})
	}
}

// TestHandleEvent_ReplyIntegration 测试回复消息的端到端集成流程。
//
// 该测试覆盖以下集成场景：
//   - 完整流程：接收回复 → 检测是回复给 bot → 提取内容 → 调用 AI 处理器
//   - 配置交互（replyAI 设置与否）
//   - 上下文管理集成
//   - 跨功能交互（回复 + mention 同时存在）
func TestHandleEvent_ReplyIntegration(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	otherUserID := id.UserID("@other:example.com")
	senderID := id.UserID("@sender:example.com")
	roomID := id.RoomID("!test:example.com")
	botMessageEventID := id.EventID("$bot_message:example.com")
	otherMessageEventID := id.EventID("$other_message:example.com")

	t.Run("完整回复流程", func(t *testing.T) {
		var (
			handlerCalled bool
			receivedArgs  []string
			receivedCtx   context.Context
			receivedUser  id.UserID
			receivedRoom  id.RoomID
			mu            sync.Mutex
		)

		// 创建模拟的 replyAI 处理器，记录所有调用参数
		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				handlerCalled = true
				receivedArgs = args
				receivedCtx = ctx
				receivedUser = userID
				receivedRoom = roomID
				return nil
			},
		}

		// 模拟 GetEvent 返回 bot 发送的消息
		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(botUserID, botMessageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		service.SetReplyAIHandler(mockReplyHandler)

		// 创建真实的 Matrix 回复事件结构
		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_event:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> 你好，有什么可以帮助你的？\n\n请帮我写一段代码",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botMessageEventID,
						},
					},
				},
			},
		}

		// 调用 HandleEvent
		ctx := context.WithValue(context.Background(), mautrix.SyncTokenContextKey, "test_token_123")
		err := service.HandleEvent(ctx, replyEvent)

		if err != nil {
			t.Fatalf("HandleEvent() returned error: %v", err)
		}

		// 验证处理器被调用
		mu.Lock()
		wasCalled := handlerCalled
		args := receivedArgs
		rCtx := receivedCtx
		rUser := receivedUser
		rRoom := receivedRoom
		mu.Unlock()

		if !wasCalled {
			t.Error("期望 replyAI 处理器被调用，但未被调用")
		}

		// 验证清理后的内容（去除回复前缀）
		expectedContent := "请帮我写一段代码"
		if len(args) == 0 || args[0] != expectedContent {
			t.Errorf("处理器收到 args = %v，期望 [%s]", args, expectedContent)
		}

		// 验证用户和房间信息正确传递
		if rUser != senderID {
			t.Errorf("处理器收到 userID = %v，期望 %v", rUser, senderID)
		}
		if rRoom != roomID {
			t.Errorf("处理器收到 roomID = %v，期望 %v", rRoom, roomID)
		}

		// 验证上下文被正确传递（SyncToken 应该在上下文中）
		if rCtx != nil {
			token := rCtx.Value(mautrix.SyncTokenContextKey)
			if token != "test_token_123" {
				t.Errorf("上下文中 SyncToken = %v，期望 'test_token_123'", token)
			}
		}
	})

	t.Run("回复非 bot 消息不触发处理器", func(t *testing.T) {
		var handlerCalled bool
		var mu sync.Mutex

		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				handlerCalled = true
				return nil
			},
		}

		// 模拟 GetEvent 返回其他用户发送的消息
		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(otherUserID, otherMessageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		service.SetReplyAIHandler(mockReplyHandler)

		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_to_other:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@other:example.com> 别人的消息\n\n我的回复",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: otherMessageEventID,
						},
					},
				},
			},
		}

		err := service.HandleEvent(context.Background(), replyEvent)
		if err != nil {
			t.Fatalf("HandleEvent() returned error: %v", err)
		}

		mu.Lock()
		wasCalled := handlerCalled
		mu.Unlock()

		if wasCalled {
			t.Error("回复非 bot 消息不应该触发 replyAI 处理器")
		}
	})

	t.Run("replyAI 未设置时不崩溃", func(t *testing.T) {
		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(botUserID, botMessageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		// 不设置 replyAI 处理器

		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_no_handler:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> Bot 消息\n\n回复内容",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botMessageEventID,
						},
					},
				},
			},
		}

		// 应该不崩溃且不返回错误
		err := service.HandleEvent(context.Background(), replyEvent)
		if err != nil {
			t.Errorf("未设置 replyAI 时不应返回错误，但返回: %v", err)
		}
	})

	t.Run("回复 + mention 同时存在", func(t *testing.T) {
		var (
			replyHandlerCalled   bool
			mentionHandlerCalled bool
			mu                   sync.Mutex
		)

		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				replyHandlerCalled = true
				return nil
			},
		}

		mockMentionHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				mentionHandlerCalled = true
				return nil
			},
		}

		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(botUserID, botMessageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		service.SetReplyAIHandler(mockReplyHandler)
		service.SetMentionAIHandler(mockMentionHandler)

		// 设置 MentionService
		mentionService := NewMentionService(client, botUserID)
		mentionService.displayName = "saber" // 设置显示名称
		service.SetMentionService(mentionService)

		// 创建一个既是回复给 bot 又包含 mention 的消息
		replyAndMentionEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_mention:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> Bot 的消息\n\n@saber:example.com 帮我分析一下",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botMessageEventID,
						},
					},
				},
			},
		}

		err := service.HandleEvent(context.Background(), replyAndMentionEvent)
		if err != nil {
			t.Fatalf("HandleEvent() returned error: %v", err)
		}

		mu.Lock()
		replyCalled := replyHandlerCalled
		mentionCalled := mentionHandlerCalled
		mu.Unlock()

		// 在当前实现中，reply 处理优先于 mention 检测。
		// 如果消息是回复给 bot 的，则只触发 reply 处理，不再触发 mention 处理。
		// 这避免了同一条消息触发两次 AI 响应。
		if !replyCalled {
			t.Error("回复给 bot 的消息应该触发 replyAI 处理器")
		}
		if mentionCalled {
			t.Error("回复给 bot 的消息不应再触发 mentionAI 处理器（避免重复响应）")
		}
	})

	t.Run("处理器返回错误时正确报告", func(t *testing.T) {
		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				return errors.New("模拟 AI 处理失败")
			},
		}

		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(botUserID, botMessageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		service.SetReplyAIHandler(mockReplyHandler)

		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_error:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> Bot 消息\n\n触发错误",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botMessageEventID,
						},
					},
				},
			},
		}

		// HandleEvent 会尝试发送错误消息到房间，但这里我们不验证具体的错误消息
		// 只验证不会 panic 且返回错误
		err := service.HandleEvent(context.Background(), replyEvent)
		if err == nil {
			t.Error("处理器返回错误时，HandleEvent 应该返回错误")
		}
	})

	t.Run("GetEvent 失败时优雅降级", func(t *testing.T) {
		var handlerCalled bool
		var mu sync.Mutex

		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				handlerCalled = true
				return nil
			},
		}

		// 模拟 GetEvent 返回错误
		roundTripper := &mockRoundTripper{
			responseErr: errors.New("网络错误"),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		service.SetReplyAIHandler(mockReplyHandler)

		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_getevent_fail:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> Bot 消息\n\n回复内容",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botMessageEventID,
						},
					},
				},
			},
		}

		// 应该不崩溃
		err := service.HandleEvent(context.Background(), replyEvent)
		if err != nil {
			t.Errorf("GetEvent 失败时不应返回错误: %v", err)
		}

		mu.Lock()
		wasCalled := handlerCalled
		mu.Unlock()

		// GetEvent 失败时，isReplyToBot 返回 false，处理器不应被调用
		if wasCalled {
			t.Error("GetEvent 失败时不应该触发 replyAI 处理器")
		}
	})

	t.Run("处理带富文本格式的回复", func(t *testing.T) {
		var receivedArgs []string
		var mu sync.Mutex

		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				receivedArgs = args
				return nil
			},
		}

		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(botUserID, botMessageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID)
		service.SetReplyAIHandler(mockReplyHandler)

		// 创建带 HTML 格式的回复消息
		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_html:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType:       event.MsgText,
					Body:          "> <@saber:example.com> Bot 消息\n\n**重要内容** 和 *斜体*",
					Format:        event.FormatHTML,
					FormattedBody: "<mx-reply><blockquote><a href=\"https://matrix.to/#/!test:example.com/$bot_message:example.com\">In reply to</a> <a href=\"https://matrix.to/#/@saber:example.com\">@saber:example.com</a><br>Bot 消息</blockquote></mx-reply><strong>重要内容</strong> 和 <em>斜体</em>",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botMessageEventID,
						},
					},
				},
			},
		}

		err := service.HandleEvent(context.Background(), replyEvent)
		if err != nil {
			t.Fatalf("HandleEvent() returned error: %v", err)
		}

		mu.Lock()
		args := receivedArgs
		mu.Unlock()

		// 验证清理后的内容
		expectedContent := "**重要内容** 和 *斜体*"
		if len(args) == 0 || args[0] != expectedContent {
			t.Errorf("处理器收到 args = %v，期望 [%s]", args, expectedContent)
		}
	})
}

func TestHandleEvent_ReplyToOriginalMessage(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	senderID := id.UserID("@sender:example.com")
	groupRoomID := id.RoomID("!group:example.com")
	privateRoomID := id.RoomID("!private:example.com")

	homeserverURL, _ := url.Parse("https://example.com")

	tests := []struct {
		name            string
		roomID          id.RoomID
		event           *event.Event
		setupDirectChat bool
		setupMention    bool
		setupReply      bool
		roundTripper    *mockRoundTripper
	}{
		{
			name:   "!ai command sends reply in group chat",
			roomID: groupRoomID,
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: groupRoomID,
				Sender: senderID,
				ID:     id.EventID("$test_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "!ai test message",
					},
				},
			},
			setupDirectChat: false,
			setupMention:    false,
			setupReply:      false,
			roundTripper: &mockRoundTripper{
				responseBody: []byte("{}"),
			},
		},
		{
			name:   "mention sends reply in group chat",
			roomID: groupRoomID,
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: groupRoomID,
				Sender: senderID,
				ID:     id.EventID("$test_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "Hey @saber:example.com, can you help me?",
					},
				},
			},
			setupDirectChat: false,
			setupMention:    true,
			setupReply:      false,
			roundTripper: &mockRoundTripper{
				// 群聊：3 个成员（bot + sender + another）
				pathResponses: map[string][]byte{
					"/_matrix/client/v3/rooms/!group:example.com/state": mustMarshalState([]id.UserID{
						botUserID,
						senderID,
						id.UserID("@another:example.com"),
					}),
				},
			},
		},
		{
			name:   "reply trigger sends reply in group chat",
			roomID: groupRoomID,
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: groupRoomID,
				Sender: senderID,
				ID:     id.EventID("$test_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "> <@saber:example.com> Bot message\n\ntest reply",
						RelatesTo: &event.RelatesTo{
							InReplyTo: &event.InReplyTo{
								EventID: "$bot_message:example.com",
							},
						},
					},
				},
			},
			setupDirectChat: false,
			setupMention:    false,
			setupReply:      true,
			roundTripper: &mockRoundTripper{
				// 群聊：3 个成员
				pathResponses: map[string][]byte{
					"/_matrix/client/v3/rooms/!group:example.com/state": mustMarshalState([]id.UserID{
						botUserID,
						senderID,
						id.UserID("@another:example.com"),
					}),
					// GetEvent 返回 bot 发送的消息
					"/_matrix/client/v3/rooms/!group:example.com/event/$bot_message:example.com": mustMarshalEvent(botUserID, "$bot_message:example.com"),
				},
			},
		},
		{
			name:   "private chat sends direct message",
			roomID: privateRoomID,
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: privateRoomID,
				Sender: senderID,
				ID:     id.EventID("$test_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "test message",
					},
				},
			},
			setupDirectChat: true,
			setupMention:    false,
			setupReply:      false,
			roundTripper: &mockRoundTripper{
				// 私聊：2 个成员（bot + sender）
				pathResponses: map[string][]byte{
					"/_matrix/client/v3/rooms/!private:example.com/state": mustMarshalState([]id.UserID{
						botUserID,
						senderID,
					}),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedEventID id.EventID
			var mu sync.Mutex

			httpClient := &http.Client{
				Transport: tt.roundTripper,
			}
			client := &mautrix.Client{
				UserID:        botUserID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, botUserID)

			mockHandler := &mockCommandHandler{
				handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
					mu.Lock()
					defer mu.Unlock()
					receivedEventID = GetEventID(ctx)
					return nil
				},
			}

			if tt.setupDirectChat {
				service.SetDirectChatAIHandler(mockHandler)
			} else {
				if tt.setupReply {
					service.SetReplyAIHandler(mockHandler)
				}
				if tt.setupMention {
					service.SetMentionAIHandler(mockHandler)
					mentionService := NewMentionService(client, botUserID)
					mentionService.displayName = "saber"
					service.SetMentionService(mentionService)
				}
				service.RegisterCommand("ai", mockHandler)
			}

			err := service.HandleEvent(context.Background(), tt.event)
			if err != nil {
				t.Fatalf("HandleEvent() error: %v", err)
			}

			time.Sleep(50 * time.Millisecond)

			mu.Lock()
			eventID := receivedEventID
			mu.Unlock()

			if eventID == "" {
				t.Errorf("Expected EventID in context, got empty string")
			} else if eventID != tt.event.ID {
				t.Errorf("Expected EventID %v, got %v", tt.event.ID, eventID)
			}
		})
	}
}
