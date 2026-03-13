// Package matrix_test 包含矩阵事件处理的单元测试。
package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"

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
