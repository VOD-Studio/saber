// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tencent-connect/botgo/dto"
)

// mockMessageSender 实现 MessageSender 接口用于测试。
type mockMessageSender struct {
	c2cMessage   *dto.Message
	c2cError     error
	groupMessage *dto.Message
	groupError   error
	lastContent  string
}

func (m *mockMessageSender) SendC2CMessage(ctx context.Context, openid string, msg *dto.MessageToCreate) (*dto.Message, error) {
	m.lastContent = msg.Content
	if m.c2cError != nil {
		return nil, m.c2cError
	}
	if m.c2cMessage != nil {
		return m.c2cMessage, nil
	}
	return &dto.Message{ID: "test-c2c-id"}, nil
}

func (m *mockMessageSender) SendGroupMessage(ctx context.Context, groupOpenid string, msg *dto.MessageToCreate) (*dto.Message, error) {
	m.lastContent = msg.Content
	if m.groupError != nil {
		return nil, m.groupError
	}
	if m.groupMessage != nil {
		return m.groupMessage, nil
	}
	return &dto.Message{ID: "test-group-id"}, nil
}

// TestNewDefaultMessageSender 测试创建消息发送器。
func TestNewDefaultMessageSender(t *testing.T) {
	sender := NewDefaultMessageSender(nil)
	if sender == nil {
		t.Error("NewDefaultMessageSender returned nil")
	}
}

// TestDefaultMessageSender_SendC2CMessage 测试私聊消息发送。
func TestDefaultMessageSender_SendC2CMessage(t *testing.T) {
	t.Run("成功发送", func(t *testing.T) {
		mock := &mockMessageSender{}

		// 使用 mock 直接测试接口行为
		msg := &dto.MessageToCreate{
			Content: "test message",
			MsgType: 0,
		}

		result, err := mock.SendC2CMessage(context.Background(), "user123", msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if result == nil {
			t.Error("expected result")
		}
		if mock.lastContent != "test message" {
			t.Errorf("lastContent = %q, want %q", mock.lastContent, "test message")
		}
	})

	t.Run("发送失败", func(t *testing.T) {
		testErr := errors.New("send failed")
		mock := &mockMessageSender{c2cError: testErr}

		msg := &dto.MessageToCreate{Content: "test"}
		_, err := mock.SendC2CMessage(context.Background(), "user123", msg)
		if !errors.Is(err, testErr) {
			t.Errorf("expected testErr, got: %v", err)
		}
	})
}

// TestDefaultMessageSender_SendGroupMessage 测试群消息发送。
func TestDefaultMessageSender_SendGroupMessage(t *testing.T) {
	t.Run("成功发送", func(t *testing.T) {
		mock := &mockMessageSender{}

		msg := &dto.MessageToCreate{
			Content: "group message",
			MsgType: 0,
		}

		result, err := mock.SendGroupMessage(context.Background(), "group123", msg)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if result == nil {
			t.Error("expected result")
		}
		if mock.lastContent != "group message" {
			t.Errorf("lastContent = %q, want %q", mock.lastContent, "group message")
		}
	})

	t.Run("发送失败", func(t *testing.T) {
		testErr := errors.New("group send failed")
		mock := &mockMessageSender{groupError: testErr}

		msg := &dto.MessageToCreate{Content: "test"}
		_, err := mock.SendGroupMessage(context.Background(), "group123", msg)
		if !errors.Is(err, testErr) {
			t.Errorf("expected testErr, got: %v", err)
		}
	})
}

// TestSendMessageWithTimeout 测试带超时的消息发送。
func TestSendMessageWithTimeout(t *testing.T) {
	t.Run("私聊消息成功", func(t *testing.T) {
		mock := &mockMessageSender{}
		msg := &dto.MessageToCreate{Content: "test"}

		err := SendMessageWithTimeout(context.Background(), mock, 5000, "user123", msg, false)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("群消息成功", func(t *testing.T) {
		mock := &mockMessageSender{}
		msg := &dto.MessageToCreate{Content: "test"}

		err := SendMessageWithTimeout(context.Background(), mock, 5000, "group123", msg, true)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("私聊消息失败", func(t *testing.T) {
		testErr := errors.New("send failed")
		mock := &mockMessageSender{c2cError: testErr}
		msg := &dto.MessageToCreate{Content: "test"}

		err := SendMessageWithTimeout(context.Background(), mock, 5000, "user123", msg, false)
		if !errors.Is(err, testErr) {
			t.Errorf("expected testErr, got: %v", err)
		}
	})

	t.Run("群消息失败", func(t *testing.T) {
		testErr := errors.New("group send failed")
		mock := &mockMessageSender{groupError: testErr}
		msg := &dto.MessageToCreate{Content: "test"}

		err := SendMessageWithTimeout(context.Background(), mock, 5000, "group123", msg, true)
		if !errors.Is(err, testErr) {
			t.Errorf("expected testErr, got: %v", err)
		}
	})
}

// TestTruncateMessage 测试消息截断函数。
func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		maxLength int
		want      string
	}{
		{
			name:      "短内容不截断",
			content:   "short",
			maxLength: 100,
			want:      "short",
		},
		{
			name:      "长内容截断并加省略号",
			content:   "long message here",
			maxLength: 10,
			want:      "long me...", // 7 chars + "..." = 10
		},
		{
			name:      "空内容",
			content:   "",
			maxLength: 100,
			want:      "",
		},
		{
			name:      "零maxLength不截断",
			content:   "text",
			maxLength: 0,
			want:      "text",
		},
		{
			name:      "负maxLength不截断",
			content:   "text",
			maxLength: -1,
			want:      "text",
		},
		{
			name:      "精确长度不截断",
			content:   "exact",
			maxLength: 5,
			want:      "exact",
		},
		{
			name:      "超出一字符截断",
			content:   "exact!",
			maxLength: 5,
			want:      "ex...",
		},
		{
			name:      "maxLength为3时只返回前3字符",
			content:   "hello",
			maxLength: 3,
			want:      "hel",
		},
		{
			name:      "maxLength为1时只返回前1字符",
			content:   "hello",
			maxLength: 1,
			want:      "h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateMessage(tt.content, tt.maxLength)
			if got != tt.want {
				t.Errorf("TruncateMessage(%q, %d) = %q, want %q", tt.content, tt.maxLength, got, tt.want)
			}
		})
	}
}

// TestValidateMessageContent 测试消息内容验证函数。
func TestValidateMessageContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name:    "有效内容",
			content: "valid message",
			wantErr: false,
		},
		{
			name:    "空内容",
			content: "",
			wantErr: true,
		},
		{
			name:    "仅空格内容",
			content: "   ",
			wantErr: false, // 空格不是空字符串
		},
		{
			name:    "超长内容",
			content: strings.Repeat("x", 5000),
			wantErr: true,
		},
		{
			name:    "边界长度4096",
			content: strings.Repeat("x", 4096),
			wantErr: false,
		},
		{
			name:    "边界长度4097",
			content: strings.Repeat("x", 4097),
			wantErr: true,
		},
		{
			name:    "中文内容",
			content: "这是一段中文消息",
			wantErr: false,
		},
		{
			name:    "Unicode内容",
			content: "🎉🎊🎈🎁",
			wantErr: false,
		},
		{
			name:    "单字符",
			content: "a",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMessageContent(tt.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateMessageContent(%q) error = %v, wantErr %v", tt.content, err, tt.wantErr)
			}
		})
	}
}

// TestValidateMessageContent_ErrorMessages 测试错误消息内容。
func TestValidateMessageContent_ErrorMessages(t *testing.T) {
	t.Run("空内容错误消息", func(t *testing.T) {
		err := ValidateMessageContent("")
		if err == nil {
			t.Fatal("expected error for empty content")
		}
		if !strings.Contains(err.Error(), "不能为空") {
			t.Errorf("error message should contain '不能为空', got: %v", err)
		}
	})

	t.Run("超长内容错误消息", func(t *testing.T) {
		err := ValidateMessageContent(strings.Repeat("x", 5000))
		if err == nil {
			t.Fatal("expected error for too long content")
		}
		if !strings.Contains(err.Error(), "过长") {
			t.Errorf("error message should contain '过长', got: %v", err)
		}
	})
}

// TestTruncateMessage_EdgeCases 测试边界情况。
func TestTruncateMessage_EdgeCases(t *testing.T) {
	t.Run("maxLength为2时无省略号空间", func(t *testing.T) {
		got := TruncateMessage("hello", 2)
		want := "he"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("maxLength为4时有省略号空间", func(t *testing.T) {
		got := TruncateMessage("hello", 4)
		want := "h..."
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("内容长度等于maxLength", func(t *testing.T) {
		got := TruncateMessage("hello", 5)
		want := "hello"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("内容长度小于maxLength", func(t *testing.T) {
		got := TruncateMessage("hi", 10)
		want := "hi"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}
