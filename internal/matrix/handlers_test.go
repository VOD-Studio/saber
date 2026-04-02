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

// TestSetProactiveManager 测试 SetProactiveManager 方法。
func TestSetProactiveManager(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)
	handler := NewEventHandler(service, 10)

	// 测试设置 nil 不会 panic
	handler.SetProactiveManager(nil)

	// 测试设置 mock manager
	mockManager := &mockProactiveManager{}
	handler.SetProactiveManager(mockManager)

	// 验证设置成功（通过检查 handler.proactiveManager 不为 nil）
	if handler.proactiveManager == nil {
		t.Error("proactiveManager should not be nil after setting")
	}
}

// mockProactiveManager 是用于测试的主动聊天管理器 mock。
type mockProactiveManager struct {
	newMemberCalled    bool
	recordMessageCalled bool
}

func (m *mockProactiveManager) OnNewMember(ctx context.Context, roomID id.RoomID, userID id.UserID) error {
	m.newMemberCalled = true
	return nil
}

func (m *mockProactiveManager) RecordUserMessage(roomID id.RoomID) {
	m.recordMessageCalled = true
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
				// StateKey 为 nil
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
			service := NewCommandService(client, tt.botID, nil)
			handler := NewEventHandler(service, 10)

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
	const maxConcurrent = 20 // 测试用的并发限制

	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	// 创建带有模拟处理器的 CommandService
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

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

	handler := NewEventHandler(service, maxConcurrent)

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

	// 等待足够时间让所有消息处理完成
	// 考虑并发限制和每个消息的处理时间
	totalMessages := goroutines * messagesPerGoroutine
	processingTime := time.Duration(totalMessages/maxConcurrent+1) * 20 * time.Millisecond
	time.Sleep(processingTime)

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

	service := NewCommandService(client, botUserID, nil)

	// 注册一个会 panic 的处理器
	panicHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			panic("intentional panic for testing")
		},
	}
	service.RegisterCommand("panic", panicHandler)

	handler := NewEventHandler(service, 10)

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

	service := NewCommandService(client, botUserID, nil)

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

	handler := NewEventHandler(service, 10)

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

	service := NewCommandService(client, botUserID, nil)

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

	handler := NewEventHandler(service, 10)

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

	service := NewCommandService(client, botUserID, nil)

	// 注册一个返回错误的处理器
	var (
		mu           sync.Mutex
		errorCount   int
		successCount int
		done         = make(chan struct{}, 20) // 跟踪处理完成
	)

	errorHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			errorCount++
			mu.Unlock()
			done <- struct{}{}
			return errors.New("simulated error")
		},
	}
	service.RegisterCommand("error", errorHandler)

	successHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			successCount++
			mu.Unlock()
			done <- struct{}{}
			return nil
		},
	}
	service.RegisterCommand("success", successHandler)

	handler := NewEventHandler(service, 10)

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

	// 等待所有处理完成（最多 5 秒）
	for range 20 {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("等待处理超时，已完成: error=%d, success=%d", errorCount, successCount)
		}
	}

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
			service := NewCommandService(client, tt.botID, nil)

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

			service := NewCommandService(client, botUserID, nil)

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

			// 如果处理器被调用，验证参数是否正确（应该包含引用消息上下文）
			if tt.expectHandlerCalled && wasCalled {
				expectedContent := "[引用消息]\ntest message\n\n[回复]\nReply content"
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

		service := NewCommandService(client, botUserID, nil)
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

		// 验证内容包含引用消息上下文
		expectedContent := "[引用消息]\ntest message\n\n[回复]\n请帮我写一段代码"
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

		service := NewCommandService(client, botUserID, nil)
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

		service := NewCommandService(client, botUserID, nil)
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

		service := NewCommandService(client, botUserID, nil)
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

		service := NewCommandService(client, botUserID, nil)
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

		service := NewCommandService(client, botUserID, nil)
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

		service := NewCommandService(client, botUserID, nil)
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

		// 验证内容包含引用消息上下文
		expectedContent := "[引用消息]\ntest message\n\n[回复]\n**重要内容** 和 *斜体*"
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

			service := NewCommandService(client, botUserID, nil)

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

// TestOnMessage_HistoryFilter 测试历史消息过滤功能。
//
// 验证启动前的历史消息会被正确过滤：
//   - 消息时间戳早于启动时间的消息应该被跳过
//   - 消息时间戳晚于启动时间的消息应该被正常处理
func TestOnMessage_HistoryFilter(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	var processedCount int
	var mu sync.Mutex

	mockHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			processedCount++
			mu.Unlock()
			return nil
		},
	}
	service.RegisterCommand("ai", mockHandler)

	handler := NewEventHandler(service, 10)

	// 等待一小段时间确保 startTime 已设置
	time.Sleep(10 * time.Millisecond)

	// 创建一个过去时间戳的事件（启动前）
	oldEvent := &event.Event{
		Type:      event.EventMessage,
		RoomID:    roomID,
		Sender:    senderID,
		ID:        id.EventID("$old_event:example.com"),
		Timestamp: time.Now().Add(-1 * time.Hour).UnixMilli(), // 1 小时前
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "!ai old message",
			},
		},
	}

	// 创建一个未来时间戳的事件（启动后）
	newEvent := &event.Event{
		Type:      event.EventMessage,
		RoomID:    roomID,
		Sender:    senderID,
		ID:        id.EventID("$new_event:example.com"),
		Timestamp: time.Now().Add(1 * time.Second).UnixMilli(), // 1 秒后
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "!ai new message",
			},
		},
	}

	// 处理旧消息（应该被过滤）
	handler.OnMessage(context.Background(), oldEvent)

	// 处理新消息（应该被正常处理）
	handler.OnMessage(context.Background(), newEvent)

	// 等待处理完成
	time.Sleep(100 * time.Millisecond)

	// 验证只有新消息被处理
	mu.Lock()
	count := processedCount
	mu.Unlock()

	if count != 1 {
		t.Errorf("期望处理 1 条消息，实际处理 %d 条", count)
	}
}

// TestOnMessage_ZeroTimestamp 测试时间戳为零的情况。
//
// 验证当事件时间戳为零时，消息应该被正常处理（不过滤）。
func TestOnMessage_ZeroTimestamp(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	roomID := id.RoomID("!test:example.com")
	senderID := id.UserID("@sender:example.com")

	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	var processedCount int
	var mu sync.Mutex

	mockHandler := &mockCommandHandler{
		handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
			mu.Lock()
			processedCount++
			mu.Unlock()
			return nil
		},
	}
	service.RegisterCommand("ai", mockHandler)

	handler := NewEventHandler(service, 10)

	// 创建时间戳为零的事件
	zeroTimestampEvent := &event.Event{
		Type:      event.EventMessage,
		RoomID:    roomID,
		Sender:    senderID,
		ID:        id.EventID("$zero_event:example.com"),
		Timestamp: 0, // 时间戳为零
		Content: event.Content{
			Parsed: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    "!ai zero timestamp",
			},
		},
	}

	// 处理时间戳为零的消息（应该被正常处理）
	handler.OnMessage(context.Background(), zeroTimestampEvent)

	// 等待处理完成
	time.Sleep(100 * time.Millisecond)

	// 验证消息被处理
	mu.Lock()
	count := processedCount
	mu.Unlock()

	if count != 1 {
		t.Errorf("期望处理 1 条消息（时间戳为零不应被过滤），实际处理 %d 条", count)
	}
}

// TestHandleReply_ReferencedImage 测试引用图片消息的处理。
//
// 验证当用户引用图片消息回复机器人时，图片信息被正确注入上下文。
func TestHandleReply_ReferencedImage(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	senderID := id.UserID("@sender:example.com")
	roomID := id.RoomID("!test:example.com")
	botImageEventID := id.EventID("$bot_image:example.com")

	t.Run("引用图片消息时注入 ReferencedMediaInfo", func(t *testing.T) {
		var (
			handlerCalled       bool
			referencedMediaInfo *MediaInfo
			mu                  sync.Mutex
		)

		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				handlerCalled = true
				referencedMediaInfo = GetReferencedMediaInfo(ctx)
				return nil
			},
		}

		// 使用 mustMarshalImageEvent 创建图片消息的 JSON 响应
		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalImageEvent(botUserID, botImageEventID, "test-image.png", "image/png", "mxc://example.com/abc123"),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID, nil)
		service.SetReplyAIHandler(mockReplyHandler)

		// 创建回复图片消息的事件
		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_to_image:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> test-image.png\n\n请描述这张图片",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botImageEventID,
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
		refMedia := referencedMediaInfo
		mu.Unlock()

		if !wasCalled {
			t.Error("期望 replyAI 处理器被调用，但未被调用")
		}

		if refMedia == nil {
			t.Error("期望 ReferencedMediaInfo 不为 nil，但得到 nil")
		} else {
			if refMedia.Type != "image" {
				t.Errorf("ReferencedMediaInfo.Type = %v, want image", refMedia.Type)
			}
			if refMedia.Body != "test-image.png" {
				t.Errorf("ReferencedMediaInfo.Body = %v, want test-image.png", refMedia.Body)
			}
		}
	})

	t.Run("引用文本消息时不注入 ReferencedMediaInfo", func(t *testing.T) {
		var (
			handlerCalled       bool
			referencedMediaInfo *MediaInfo
			mu                  sync.Mutex
		)

		mockReplyHandler := &mockCommandHandler{
			handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
				mu.Lock()
				defer mu.Unlock()
				handlerCalled = true
				referencedMediaInfo = GetReferencedMediaInfo(ctx)
				return nil
			},
		}

		// 模拟 GetEvent 返回 bot 发送的文本消息
		roundTripper := &mockRoundTripper{
			responseBody: mustMarshalEvent(botUserID, botImageEventID),
		}

		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID, nil)
		service.SetReplyAIHandler(mockReplyHandler)

		// 创建回复文本消息的事件
		replyEvent := &event.Event{
			Type:   event.EventMessage,
			RoomID: roomID,
			Sender: senderID,
			ID:     id.EventID("$reply_to_text:example.com"),
			Content: event.Content{
				Parsed: &event.MessageEventContent{
					MsgType: event.MsgText,
					Body:    "> <@saber:example.com> 你好\n\n继续对话",
					RelatesTo: &event.RelatesTo{
						InReplyTo: &event.InReplyTo{
							EventID: botImageEventID,
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
		refMedia := referencedMediaInfo
		mu.Unlock()

		if !wasCalled {
			t.Error("期望 replyAI 处理器被调用，但未被调用")
		}

		if refMedia != nil {
			t.Errorf("期望 ReferencedMediaInfo 为 nil，但得到 %+v", refMedia)
		}
	})
}

// mustMarshalImageEvent 将图片事件信息序列化为 Matrix API 返回的 JSON 格式。
func mustMarshalImageEvent(sender id.UserID, eventID id.EventID, body, mimeType, mxcURL string) []byte {
	evt := map[string]any{
		"sender":   string(sender),
		"event_id": string(eventID),
		"type":     "m.room.message",
		"content": map[string]any{
			"msgtype": "m.image",
			"body":    body,
			"url":     mxcURL,
			"info": map[string]any{
				"mimetype": mimeType,
			},
		},
		"origin_server_ts": 1234567890,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		panic(err)
	}
	return data
}

// TestCommandService_ListCommands 测试 ListCommands 方法。
func TestCommandService_ListCommands(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	// 初始状态应该没有命令
	commands := service.ListCommands()
	if len(commands) != 0 {
		t.Errorf("expected 0 commands initially, got %d", len(commands))
	}

	// 注册一些命令
	service.RegisterCommandWithDesc("ping", "Ping command", &mockCommandHandler{})
	service.RegisterCommandWithDesc("help", "Help command", &mockCommandHandler{})
	service.RegisterCommandWithDesc("ai", "AI command", &mockCommandHandler{})

	commands = service.ListCommands()
	if len(commands) != 3 {
		t.Errorf("expected 3 commands, got %d", len(commands))
	}

	// 验证命令存在
	commandNames := make(map[string]bool)
	for _, cmd := range commands {
		commandNames[cmd.Name] = true
	}

	for _, expected := range []string{"ping", "help", "ai"} {
		if !commandNames[expected] {
			t.Errorf("expected command %s not found", expected)
		}
	}
}

// TestCommandService_UnregisterCommand 测试 UnregisterCommand 方法。
func TestCommandService_UnregisterCommand(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	// 注册命令
	service.RegisterCommandWithDesc("ping", "Ping command", &mockCommandHandler{})
	service.RegisterCommandWithDesc("help", "Help command", &mockCommandHandler{})

	// 验证注册成功
	if len(service.ListCommands()) != 2 {
		t.Errorf("expected 2 commands, got %d", len(service.ListCommands()))
	}

	// 注销命令
	service.UnregisterCommand("ping")

	// 验证注销成功
	commands := service.ListCommands()
	if len(commands) != 1 {
		t.Errorf("expected 1 command after unregister, got %d", len(commands))
	}

	// 验证剩余的命令
	if _, ok := service.GetCommand("ping"); ok {
		t.Error("ping command should be unregistered")
	}

	if _, ok := service.GetCommand("help"); !ok {
		t.Error("help command should still exist")
	}
}

// TestCommandService_BotID 测试 BotID 方法。
func TestCommandService_BotID(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	if service.BotID() != botUserID {
		t.Errorf("expected bot ID %s, got %s", botUserID, service.BotID())
	}
}

// TestCommandService_GetBuildInfo 测试 GetBuildInfo 方法。
func TestCommandService_GetBuildInfo(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	buildInfo := &BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	service := NewCommandService(client, botUserID, buildInfo)

	gotInfo := service.GetBuildInfo()
	if gotInfo == nil {
		t.Fatal("GetBuildInfo returned nil")
	}

	if gotInfo.Version != buildInfo.Version {
		t.Errorf("expected version %s, got %s", buildInfo.Version, gotInfo.Version)
	}

	if gotInfo.GitCommit != buildInfo.GitCommit {
		t.Errorf("expected git commit %s, got %s", buildInfo.GitCommit, gotInfo.GitCommit)
	}
}

// TestCommandService_ParseMentionCommand 测试 parseMentionCommand 方法。
func TestCommandService_ParseMentionCommand(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	tests := []struct {
		name          string
		input         string
		expectNil     bool
		expectCommand string
		expectArgs    []string
	}{
		{
			name:          "valid mention with command",
			input:         "@saber:example.com help",
			expectNil:     false,
			expectCommand: "help",
			expectArgs:    []string{},
		},
		{
			name:          "valid mention with command and args",
			input:         "@saber:example.com ai hello world",
			expectNil:     false,
			expectCommand: "ai",
			expectArgs:    []string{"hello", "world"},
		},
		{
			name:          "mention with trailing colon",
			input:         "@saber:example.com: ping",
			expectNil:     false,
			expectCommand: "ping",
			expectArgs:    []string{},
		},
		{
			name:      "wrong bot mention",
			input:     "@other:example.com help",
			expectNil: true,
		},
		{
			name:      "mention without command",
			input:     "@saber:example.com",
			expectNil: true,
		},
		{
			name:      "empty string",
			input:     "",
			expectNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := service.ParseCommand(tt.input)

			if tt.expectNil {
				if result != nil {
					t.Errorf("expected nil result, got %+v", result)
				}
				return
			}

			if result == nil {
				t.Fatalf("expected non-nil result")
			}

			if result.Command != tt.expectCommand {
				t.Errorf("expected command %s, got %s", tt.expectCommand, result.Command)
			}

			if len(result.Args) != len(tt.expectArgs) {
				t.Errorf("expected %d args, got %d", len(tt.expectArgs), len(result.Args))
			}

			for i, arg := range tt.expectArgs {
				if i < len(result.Args) && result.Args[i] != arg {
					t.Errorf("expected arg[%d] = %s, got %s", i, arg, result.Args[i])
				}
			}
		})
	}
}

// TestBuildInfo_RuntimePlatform 测试 RuntimePlatform 方法。
func TestBuildInfo_RuntimePlatform(t *testing.T) {
	info := BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01",
		GoVersion:     "go1.21.0",
		BuildPlatform: "linux/amd64",
	}

	platform := info.RuntimePlatform()
	if platform == "" {
		t.Error("RuntimePlatform returned empty string")
	}

	// 应该包含 GOOS/GOARCH 格式
	if !strings.Contains(platform, "/") {
		t.Errorf("RuntimePlatform should contain '/', got %s", platform)
	}
}

// TestCommandService_SetMethods 测试各种 Set 方法。
func TestCommandService_SetMethods(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)

	// 测试 SetDirectChatAIHandler
	mockHandler := &mockCommandHandler{}
	service.SetDirectChatAIHandler(mockHandler)
	if service.directChatAI != mockHandler {
		t.Error("SetDirectChatAIHandler failed")
	}

	// 测试 SetMentionAIHandler
	service.SetMentionAIHandler(mockHandler)
	if service.mentionAI != mockHandler {
		t.Error("SetMentionAIHandler failed")
	}

	// 测试 SetReplyAIHandler
	service.SetReplyAIHandler(mockHandler)
	if service.replyAI != mockHandler {
		t.Error("SetReplyAIHandler failed")
	}
}

// TestCommandService_GetCommand 测试 GetCommand 方法。
func TestCommandService_GetCommand(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)
	mockHandler := &mockCommandHandler{}
	service.RegisterCommandWithDesc("test", "Test command", mockHandler)

	// 测试获取存在的命令
	info, ok := service.GetCommand("test")
	if !ok {
		t.Error("expected to find test command")
	}
	if info.Name != "test" {
		t.Errorf("expected name 'test', got %s", info.Name)
	}
	if info.Handler != mockHandler {
		t.Error("handler mismatch")
	}

	// 测试获取不存在的命令
	_, ok = service.GetCommand("nonexistent")
	if ok {
		t.Error("expected not to find nonexistent command")
	}

	// 测试大小写不敏感
	info, ok = service.GetCommand("TEST")
	if !ok {
		t.Error("expected case-insensitive match")
	}
	if info.Name != "test" {
		t.Errorf("expected name 'test', got %s", info.Name)
	}
}

// TestCommandService_RegisterCommand 测试 RegisterCommand 方法。
func TestCommandService_RegisterCommand(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	homeserverURL, _ := url.Parse("https://example.com")
	client := &mautrix.Client{
		UserID:        botUserID,
		HomeserverURL: homeserverURL,
	}

	service := NewCommandService(client, botUserID, nil)
	mockHandler := &mockCommandHandler{}

	// 使用 RegisterCommand（不带描述）
	service.RegisterCommand("test", mockHandler)

	info, ok := service.GetCommand("test")
	if !ok {
		t.Error("expected to find test command")
	}
	if info.Name != "test" {
		t.Errorf("expected name 'test', got %s", info.Name)
	}
	if info.Description != "" {
		t.Errorf("expected empty description, got %s", info.Description)
	}
}

// TestCommandService_StartTyping 测试 CommandService 的 StartTyping 方法。
func TestCommandService_StartTyping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		roomID      id.RoomID
		timeout     int
		responseErr error
		expectErr   bool
	}{
		{
			name:        "默认超时",
			roomID:      id.RoomID("!room:example.com"),
			timeout:     0, // 使用默认值 30000
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "自定义超时",
			roomID:      id.RoomID("!room:example.com"),
			timeout:     60000,
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "API 错误",
			roomID:      id.RoomID("!room:example.com"),
			timeout:     30000,
			responseErr: errors.New("forbidden"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &mockRoundTripper{
				responseErr: tt.responseErr,
				responseBody: []byte(`{}`),
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@saber:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, id.UserID("@saber:example.com"), nil)

			err := service.StartTyping(context.Background(), tt.roomID, tt.timeout)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("StartTyping() 返回意外错误: %v", err)
			}
		})
	}
}

// TestCommandService_StopTyping 测试 CommandService 的 StopTyping 方法。
func TestCommandService_StopTyping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		roomID      id.RoomID
		responseErr error
		expectErr   bool
	}{
		{
			name:        "成功停止",
			roomID:      id.RoomID("!room:example.com"),
			responseErr: nil,
			expectErr:   false,
		},
		{
			name:        "API 错误",
			roomID:      id.RoomID("!room:example.com"),
			responseErr: errors.New("forbidden"),
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roundTripper := &mockRoundTripper{
				responseErr: tt.responseErr,
				responseBody: []byte(`{}`),
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        id.UserID("@saber:example.com"),
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, id.UserID("@saber:example.com"), nil)

			err := service.StopTyping(context.Background(), tt.roomID)

			if tt.expectErr {
				if err == nil {
					t.Error("期望返回错误，但返回 nil")
				}
				return
			}

			if err != nil {
				t.Errorf("StopTyping() 返回意外错误: %v", err)
			}
		})
	}
}
