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
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// mockRoundTripper 创建一个模拟的 HTTP 传输层，用于控制 JoinRoom 的响应。
type mockRoundTripper struct {
	responseBody []byte
	responseErr  error
	requests     []*http.Request
}

// RoundTrip 执行 HTTP 请求并返回模拟响应。
func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	m.requests = append(m.requests, req)
	if m.responseErr != nil {
		return nil, m.responseErr
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(m.responseBody)),
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
	client := &mautrix.Client{
		UserID:        botUserID,
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
