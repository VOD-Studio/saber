// Package matrix_test 包含 HandleEvent 拆分处理器的单元测试。
package matrix

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestHandleDirectChat 测试私聊消息处理逻辑。
//
// 该测试覆盖以下情况：
//   - 私聊消息触发 directChatAI 处理器
//   - 群聊消息不触发 directChatAI 处理器
//   - directChatAI 为 nil 时不崩溃
//   - 处理器返回错误时正确传播
func TestHandleDirectChat(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	senderID := id.UserID("@sender:example.com")
	privateRoomID := id.RoomID("!private:example.com")
	groupRoomID := id.RoomID("!group:example.com")

	tests := []struct {
		name                string
		roomID              id.RoomID
		setupDirectAI       bool
		members             []id.UserID
		expectHandlerCalled bool
		expectError         bool
	}{
		{
			name:                "私聊消息触发 AI 处理器",
			roomID:              privateRoomID,
			setupDirectAI:       true,
			members:             []id.UserID{botUserID, senderID},
			expectHandlerCalled: true,
		},
		{
			name:                "群聊消息不触发 directChatAI",
			roomID:              groupRoomID,
			setupDirectAI:       true,
			members:             []id.UserID{botUserID, senderID, id.UserID("@other:example.com")},
			expectHandlerCalled: false,
		},
		{
			name:                "directChatAI 为 nil 时不崩溃",
			roomID:              privateRoomID,
			setupDirectAI:       false,
			members:             []id.UserID{botUserID, senderID},
			expectHandlerCalled: false,
		},
		{
			name:                "处理器返回错误时传播错误",
			roomID:              privateRoomID,
			setupDirectAI:       true,
			members:             []id.UserID{botUserID, senderID},
			expectHandlerCalled: true,
			expectError:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var handlerCalled bool
			var mu sync.Mutex

			// 创建模拟的 directChatAI 处理器
			mockHandler := &mockCommandHandler{
				handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
					mu.Lock()
					handlerCalled = true
					mu.Unlock()
					if tt.expectError {
						return errors.New("模拟处理错误")
					}
					return nil
				},
			}

			// 创建模拟的 HTTP 传输层
			roundTripper := &mockRoundTripper{
				responseBody: mustMarshalState(tt.members),
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        botUserID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, botUserID, nil)
			if tt.setupDirectAI {
				service.SetDirectChatAIHandler(mockHandler)
			}

			// 创建消息事件
			evt := &event.Event{
				Type:   event.EventMessage,
				RoomID: tt.roomID,
				Sender: senderID,
				ID:     id.EventID("$test_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "测试消息",
					},
				},
			}

			// 调用 HandleEvent
			ctx := context.Background()
			err := service.HandleEvent(ctx, evt)

			// 验证错误
			if tt.expectError && err == nil {
				t.Error("期望返回错误，但返回 nil")
			}

			// 验证处理器是否被调用
			mu.Lock()
			wasCalled := handlerCalled
			mu.Unlock()

			if wasCalled != tt.expectHandlerCalled {
				t.Errorf("处理器被调用 = %v, 期望 %v", wasCalled, tt.expectHandlerCalled)
			}
		})
	}
}

// TestHandleReply 测试回复消息处理逻辑。
//
// 该测试覆盖以下情况：
//   - 回复 bot 消息触发 replyAI 处理器
//   - 回复非 bot 消息不触发 replyAI 处理器
//   - replyAI 为 nil 时不崩溃
//   - 非回复消息不触发 replyAI 处理器
//   - 回复内容被正确清理（去除回复前缀）
func TestHandleReply(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	otherUserID := id.UserID("@other:example.com")
	senderID := id.UserID("@sender:example.com")
	roomID := id.RoomID("!test:example.com")
	replyToEventID := id.EventID("$original_event:example.com")

	tests := []struct {
		name                string
		event               *event.Event
		setupReplyAI        bool
		replyToSender       id.UserID
		expectHandlerCalled bool
		expectedContent     string
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
						Body:    "> <@saber:example.com> 原始消息\n\n回复内容",
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
			expectedContent:     "回复内容",
		},
		{
			name: "回复非 bot 消息不触发处理器",
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$reply_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "> <@other:example.com> 原始消息\n\n回复内容",
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
						Body:    "> <@saber:example.com> 原始消息\n\n回复内容",
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
			name: "非回复消息不触发处理器",
			event: &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$normal_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    "普通消息",
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

			mockReplyHandler := &mockCommandHandler{
				handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
					mu.Lock()
					defer mu.Unlock()
					handlerCalled = true
					receivedArgs = args
					return nil
				},
			}

			roundTripper := &mockRoundTripper{
				responseBody: mustMarshalEvent(tt.replyToSender, replyToEventID),
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        botUserID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, botUserID, nil)
			if tt.setupReplyAI {
				service.SetReplyAIHandler(mockReplyHandler)
			}

			ctx := context.Background()
			err := service.HandleEvent(ctx, tt.event)

			if err != nil {
				t.Errorf("HandleEvent() 返回意外错误: %v", err)
			}

			mu.Lock()
			wasCalled := handlerCalled
			args := receivedArgs
			mu.Unlock()

			if wasCalled != tt.expectHandlerCalled {
				t.Errorf("处理器被调用 = %v, 期望 %v", wasCalled, tt.expectHandlerCalled)
			}

			if tt.expectHandlerCalled && wasCalled && tt.expectedContent != "" {
				if len(args) == 0 || args[0] != tt.expectedContent {
					t.Errorf("处理器收到 args = %v, 期望 [%s]", args, tt.expectedContent)
				}
			}
		})
	}
}

// TestHandleGroupMention 测试群聊提及处理逻辑。
//
// 该测试验证 handleGroupMention 函数的边界条件。
// 完整的集成测试在 handlers_test.go 的 TestHandleEvent_ReplyToOriginalMessage 中。
func TestHandleGroupMention(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	senderID := id.UserID("@sender:example.com")

	t.Run("mentionAI 为 nil 时不触发处理器", func(t *testing.T) {
		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        botUserID,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID, nil)
		// 不设置 mentionAI 处理器

		content := &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "@saber:example.com 帮我看看",
		}

		handled, err := service.handleGroupMention(context.Background(), senderID, "!room:example.com", content)
		if handled {
			t.Error("mentionAI 为 nil 时不应该处理")
		}
		if err != nil {
			t.Errorf("不应该返回错误: %v", err)
		}
	})

	t.Run("mentionService 为 nil 时不触发处理器", func(t *testing.T) {
		homeserverURL, _ := url.Parse("https://example.com")
		client := &mautrix.Client{
			UserID:        botUserID,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID, nil)
		service.SetMentionAIHandler(&mockCommandHandler{})
		// 不设置 mentionService

		content := &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "@saber:example.com 帮我看看",
		}

		handled, err := service.handleGroupMention(context.Background(), senderID, "!room:example.com", content)
		if handled {
			t.Error("mentionService 为 nil 时不应该处理")
		}
		if err != nil {
			t.Errorf("不应该返回错误: %v", err)
		}
	})

	t.Run("消息内容为空时不触发处理器", func(t *testing.T) {
		roundTripper := &mockRoundTripper{}
		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID, nil)
		mentionService := NewMentionService(client, botUserID)
		service.SetMentionService(mentionService)
		service.SetMentionAIHandler(&mockCommandHandler{})

		content := &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "",
		}

		handled, err := service.handleGroupMention(context.Background(), senderID, "!room:example.com", content)
		if handled {
			t.Error("空消息不应该触发处理器")
		}
		if err != nil {
			t.Errorf("不应该返回错误: %v", err)
		}
	})

	t.Run("消息不包含提及时不触发处理器", func(t *testing.T) {
		roundTripper := &mockRoundTripper{}
		homeserverURL, _ := url.Parse("https://example.com")
		httpClient := &http.Client{Transport: roundTripper}
		client := &mautrix.Client{
			UserID:        botUserID,
			Client:        httpClient,
			HomeserverURL: homeserverURL,
		}

		service := NewCommandService(client, botUserID, nil)
		mentionService := NewMentionService(client, botUserID)
		service.SetMentionService(mentionService)
		service.SetMentionAIHandler(&mockCommandHandler{})

		content := &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "这是一条普通消息",
		}

		handled, err := service.handleGroupMention(context.Background(), senderID, "!room:example.com", content)
		if handled {
			t.Error("不包含提及的消息不应该触发处理器")
		}
		if err != nil {
			t.Errorf("不应该返回错误: %v", err)
		}
	})
}

// TestHandleCommand 测试命令处理逻辑。
//
// 该测试覆盖以下情况：
//   - 已注册命令被正确执行
//   - 未注册命令被忽略
//   - 命令参数被正确传递
//   - 命令处理器错误被正确传播
func TestHandleCommand(t *testing.T) {
	botUserID := id.UserID("@saber:example.com")
	senderID := id.UserID("@sender:example.com")
	roomID := id.RoomID("!test:example.com")

	tests := []struct {
		name            string
		messageBody     string
		registerCommand bool
		expectExecuted  bool
		expectedArgs    []string
		expectError     bool
	}{
		{
			name:            "已注册命令被执行",
			messageBody:     "!testcmd arg1 arg2",
			registerCommand: true,
			expectExecuted:  true,
			expectedArgs:    []string{"arg1", "arg2"},
		},
		{
			name:            "未注册命令被忽略",
			messageBody:     "!unknowncmd arg1",
			registerCommand: false,
			expectExecuted:  false,
		},
		{
			name:            "命令处理器错误被传播",
			messageBody:     "!testcmd fail",
			registerCommand: true,
			expectExecuted:  true,
			expectedArgs:    []string{"fail"},
			expectError:     true,
		},
		{
			name:            "无参数命令",
			messageBody:     "!testcmd",
			registerCommand: true,
			expectExecuted:  true,
			expectedArgs:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var executed bool
			var receivedArgs []string
			var mu sync.Mutex

			roundTripper := &mockRoundTripper{
				responseBody: []byte(`{"event_id": "$sent:example.com"}`),
			}

			homeserverURL, _ := url.Parse("https://example.com")
			httpClient := &http.Client{Transport: roundTripper}
			client := &mautrix.Client{
				UserID:        botUserID,
				Client:        httpClient,
				HomeserverURL: homeserverURL,
			}

			service := NewCommandService(client, botUserID, nil)

			if tt.registerCommand {
				mockHandler := &mockCommandHandler{
					handleFunc: func(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
						mu.Lock()
						defer mu.Unlock()
						executed = true
						receivedArgs = args
						if tt.expectError && len(args) > 0 && args[0] == "fail" {
							return errors.New("模拟命令错误")
						}
						return nil
					},
				}
				service.RegisterCommand("testcmd", mockHandler)
			}

			evt := &event.Event{
				Type:   event.EventMessage,
				RoomID: roomID,
				Sender: senderID,
				ID:     id.EventID("$test_event:example.com"),
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						MsgType: event.MsgText,
						Body:    tt.messageBody,
					},
				},
			}

			ctx := context.Background()
			err := service.HandleEvent(ctx, evt)

			if tt.expectError && err == nil {
				t.Error("期望返回错误，但返回 nil")
			}

			mu.Lock()
			wasExecuted := executed
			args := receivedArgs
			mu.Unlock()

			if wasExecuted != tt.expectExecuted {
				t.Errorf("命令被执行 = %v, 期望 %v", wasExecuted, tt.expectExecuted)
			}

			if tt.expectExecuted && wasExecuted {
				if len(args) != len(tt.expectedArgs) {
					t.Errorf("参数数量 = %d, 期望 %d", len(args), len(tt.expectedArgs))
				}
				for i, arg := range args {
					if i < len(tt.expectedArgs) && arg != tt.expectedArgs[i] {
						t.Errorf("参数[%d] = %v, 期望 %v", i, arg, tt.expectedArgs[i])
					}
				}
			}
		})
	}
}
