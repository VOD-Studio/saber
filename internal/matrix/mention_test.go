// Package matrix_test 包含 mention 服务的单元测试。
package matrix

import (
	"fmt"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// TestNewMentionService 测试 NewMentionService 构造函数。
//
// 该测试覆盖以下场景：
//   - 创建有效的 MentionService 实例
//   - 参数正确设置
//   - 零值参数处理
func TestNewMentionService(t *testing.T) {
	botID := id.UserID("@bot:example.com")
	tests := []struct {
		name    string
		client  *mautrix.Client
		botID   id.UserID
		wantNil bool
	}{
		{"nil client", nil, botID, false},
		{"nil botID", &mautrix.Client{}, "", false},
		{"both nil", nil, "", false},
		{"valid params", &mautrix.Client{}, botID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewMentionService(tt.client, tt.botID)
			if (got == nil) != tt.wantNil {
				t.Errorf("NewMentionService() = %v, want nil: %v", got, tt.wantNil)
			}
			if got != nil {
				if got.client != tt.client {
					t.Errorf("client not set correctly")
				}
				if got.botID != tt.botID {
					t.Errorf("botID not set correctly")
				}
			}
		})
	}
}

// TestParseMention_MSC3952 测试 MSC 3952 结构化 mentions 解析。
//
// 该测试覆盖以下场景：
//   - 机器人被提及的情况
//   - 机器人未被提及的情况
//   - 空 mentions 字段
//   - 多个用户被提及（包括机器人）
func TestParseMention_MSC3952(t *testing.T) {
	botID := id.UserID("@bot:example.com")
	otherUserID := id.UserID("@other:example.com")

	tests := []struct {
		name        string
		body        string
		mentions    *event.Mentions
		wantMention bool
		wantMsg     string
	}{
		{
			name:        "mentioned",
			body:        "hello",
			mentions:    &event.Mentions{UserIDs: []id.UserID{botID}},
			wantMention: true,
			wantMsg:     "hello",
		},
		{
			name:        "not mentioned",
			body:        "hello",
			mentions:    &event.Mentions{UserIDs: []id.UserID{}},
			wantMention: false,
			wantMsg:     "hello",
		},
		{
			name:        "nil mentions",
			body:        "hello",
			mentions:    nil,
			wantMention: false,
			wantMsg:     "hello",
		},
		{
			name:        "multiple mentions including bot",
			body:        "@bot:example.com hello world",
			mentions:    &event.Mentions{UserIDs: []id.UserID{otherUserID, botID}},
			wantMention: true,
			wantMsg:     "hello world",
		},
		{
			name:        "multiple mentions without bot",
			body:        "@other:example.com hello world",
			mentions:    &event.Mentions{UserIDs: []id.UserID{otherUserID, "@user2:example.com"}},
			wantMention: false,
			wantMsg:     "@other:example.com hello world",
		},
		{
			name:        "empty body with mention",
			body:        "",
			mentions:    &event.Mentions{UserIDs: []id.UserID{botID}},
			wantMention: true,
			wantMsg:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			content := &event.MessageEventContent{
				Body:     tt.body,
				Mentions: tt.mentions,
			}

			cleanedMsg, isMentioned := svc.ParseMention(tt.body, content)
			if isMentioned != tt.wantMention {
				t.Errorf("ParseMention() isMentioned = %v, want %v", isMentioned, tt.wantMention)
			}
			if cleanedMsg != tt.wantMsg {
				t.Errorf("ParseMention() cleanedMsg = %q, want %q", cleanedMsg, tt.wantMsg)
			}
		})
	}
}

// TestParseMention_HTMLPills 测试 HTML pills 格式 mentions 解析。
//
// 该测试覆盖以下场景：
//   - HTML pills 中包含机器人提及
//   - HTML pills 中不包含机器人提及
//   - 无效 HTML 格式
//   - 非 HTML 格式消息
func TestParseMention_HTMLPills(t *testing.T) {
	botID := id.UserID("@bot:example.com")

	// 创建一个假的 HTML 转换函数来模拟 mautrix 的行为
	// 由于我们无法直接控制 mautrix 的 HTMLToMarkdownFull 函数，
	// 我们将依赖实际的 mautrix 行为进行测试
	tests := []struct {
		name        string
		body        string
		format      event.Format
		formatted   string
		wantMention bool
		wantMsg     string
	}{
		{
			name:        "HTML pill with bot mention",
			body:        "@bot hello",
			format:      event.FormatHTML,
			formatted:   `<a href="https://matrix.to/#/@bot:example.com">bot</a> hello`,
			wantMention: true,
			wantMsg:     "@bot hello", // StripMentionPrefix cannot remove "@bot" without display name or full user ID
		},
		{
			name:        "HTML pill without bot mention",
			body:        "@other hello",
			format:      event.FormatHTML,
			formatted:   `<a href="https://matrix.to/#/@other:example.com">other</a> hello`,
			wantMention: false,
			wantMsg:     "@other hello",
		},
		{
			name:        "invalid HTML format",
			body:        "hello",
			format:      event.FormatHTML,
			formatted:   "invalid <html>",
			wantMention: false,
			wantMsg:     "hello",
		},
		{
			name:        "non-HTML format",
			body:        "@bot:example.com hello",
			format:      event.FormatHTML,
			formatted:   "",
			wantMention: true, // This will be caught by user ID matching instead
			wantMsg:     "hello",
		},
		{
			name:        "empty formatted body",
			body:        "hello",
			format:      event.FormatHTML,
			formatted:   "",
			wantMention: false,
			wantMsg:     "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			content := &event.MessageEventContent{
				Body:          tt.body,
				Format:        tt.format,
				FormattedBody: tt.formatted,
			}

			cleanedMsg, isMentioned := svc.ParseMention(tt.body, content)
			if isMentioned != tt.wantMention {
				t.Errorf("ParseMention() isMentioned = %v, want %v", isMentioned, tt.wantMention)
			}
			if cleanedMsg != tt.wantMsg {
				t.Errorf("ParseMention() cleanedMsg = %q, want %q", cleanedMsg, tt.wantMsg)
			}
		})
	}
}

// TestParseMention_DisplayName 测试显示名称文本匹配。
//
// 该测试覆盖以下场景：
//   - 显示名称完全匹配
//   - 显示名称部分匹配（不区分大小写）
//   - 显示名称为空
//   - 特殊字符处理
//   - 空消息处理
func TestParseMention_DisplayName(t *testing.T) {
	botID := id.UserID("@bot:example.com")

	tests := []struct {
		name        string
		displayName string
		body        string
		wantMention bool
		wantMsg     string
	}{
		{
			name:        "exact match",
			displayName: "Saber",
			body:        "Saber hello world",
			wantMention: true,
			wantMsg:     "hello world",
		},
		{
			name:        "case insensitive match",
			displayName: "Saber",
			body:        "saber hello world",
			wantMention: true,
			wantMsg:     "hello world",
		},
		{
			name:        "partial match in middle",
			displayName: "Saber",
			body:        "Hello Saber! How are you?",
			wantMention: true,
			wantMsg:     "Hello ! How are you?",
		},
		{
			name:        "no match",
			displayName: "Saber",
			body:        "Hello Paimon! How are you?",
			wantMention: false,
			wantMsg:     "Hello Paimon! How are you?",
		},
		{
			name:        "empty display name",
			displayName: "",
			body:        "Saber hello",
			wantMention: false,
			wantMsg:     "Saber hello",
		},
		{
			name:        "special characters",
			displayName: "派蒙",
			body:        "派蒙：你好！",
			wantMention: true,
			wantMsg:     "：你好！",
		},
		{
			name:        "empty body",
			displayName: "Saber",
			body:        "",
			wantMention: false,
			wantMsg:     "",
		},
		{
			name:        "whitespace only",
			displayName: "Saber",
			body:        "   ",
			wantMention: false,
			wantMsg:     "   ",
		},
		{
			name:        "display name with spaces",
			displayName: "My Bot",
			body:        "My Bot hello world",
			wantMention: true,
			wantMsg:     "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			// Mock the display name by directly setting it (bypassing Init)
			svc.mu.Lock()
			svc.displayName = tt.displayName
			svc.mu.Unlock()

			content := &event.MessageEventContent{
				Body: tt.body,
			}

			cleanedMsg, isMentioned := svc.ParseMention(tt.body, content)
			if isMentioned != tt.wantMention {
				t.Errorf("ParseMention() isMentioned = %v, want %v", isMentioned, tt.wantMention)
			}
			if cleanedMsg != tt.wantMsg {
				t.Errorf("ParseMention() cleanedMsg = %q, want %q", cleanedMsg, tt.wantMsg)
			}
		})
	}
}

// TestParseMention_UserID 测试用户 ID 文本匹配。
//
// 该测试覆盖以下场景：
//   - 用户 ID 完全匹配
//   - 用户 ID 部分匹配
//   - 空消息处理
//   - 特殊格式用户 ID
func TestParseMention_UserID(t *testing.T) {
	botID := id.UserID("@my-bot:matrix.org")

	tests := []struct {
		name        string
		body        string
		wantMention bool
		wantMsg     string
	}{
		{
			name:        "exact user ID match",
			body:        "@my-bot:matrix.org hello world",
			wantMention: true,
			wantMsg:     "hello world",
		},
		{
			name:        "user ID in middle",
			body:        "Hello @my-bot:matrix.org! How are you?",
			wantMention: true,
			wantMsg:     "Hello ! How are you?",
		},
		{
			name:        "no user ID match",
			body:        "Hello @other:matrix.org! How are you?",
			wantMention: false,
			wantMsg:     "Hello @other:matrix.org! How are you?",
		},
		{
			name:        "empty body",
			body:        "",
			wantMention: false,
			wantMsg:     "",
		},
		{
			name:        "whitespace only",
			body:        "   ",
			wantMention: false,
			wantMsg:     "   ",
		},
		{
			name:        "complex user ID",
			body:        "@my-special-bot_123:subdomain.matrix.org hello",
			wantMention: false, // Different bot ID
			wantMsg:     "@my-special-bot_123:subdomain.matrix.org hello",
		},
		{
			name:        "same bot ID different case", // User IDs are case sensitive
			body:        "@MY-BOT:MATRIX.ORG hello",
			wantMention: false,
			wantMsg:     "@MY-BOT:MATRIX.ORG hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			content := &event.MessageEventContent{
				Body: tt.body,
			}

			cleanedMsg, isMentioned := svc.ParseMention(tt.body, content)
			if isMentioned != tt.wantMention {
				t.Errorf("ParseMention() isMentioned = %v, want %v", isMentioned, tt.wantMention)
			}
			if cleanedMsg != tt.wantMsg {
				t.Errorf("ParseMention() cleanedMsg = %q, want %q", cleanedMsg, tt.wantMsg)
			}
		})
	}
}

// TestMentionService_Concurrency 测试并发安全性。
//
// 该测试覆盖以下场景：
//   - 并发读取显示名称
//   - 并发检查提及
//   - 并发初始化和读取
func TestMentionService_Concurrency(t *testing.T) {
	botID := id.UserID("@bot:example.com")
	svc := NewMentionService(&mautrix.Client{}, botID)

	// 设置初始显示名称
	svc.mu.Lock()
	svc.displayName = "Saber"
	svc.mu.Unlock()

	const goroutines = 50
	errChan := make(chan error, goroutines*3)

	// 并发读取显示名称
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in GetDisplayName: %v", r)
				}
			}()
			_ = svc.GetDisplayName()
			errChan <- nil
		}()
	}

	// 并发检查提及
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in IsMentioned: %v", r)
				}
			}()
			_ = svc.IsMentioned("Hello Saber!")
			errChan <- nil
		}()
	}

	// 并发解析提及
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in ParseMention: %v", r)
				}
			}()
			content := &event.MessageEventContent{
				Body: "Hello Saber!",
			}
			_, _ = svc.ParseMention("Hello Saber!", content)
			errChan <- nil
		}()
	}

	// 收集所有结果
	totalTests := goroutines * 3
	for i := 0; i < totalTests; i++ {
		if err := <-errChan; err != nil {
			t.Errorf("并发测试失败: %v", err)
		}
	}
}

// TestMentionService_BoundaryCases 测试边界情况。
//
// 该测试覆盖以下场景：
//   - 空字符串输入
//   - nil 输入
//   - 超长字符串
//   - 特殊 Unicode 字符
//   - 初始化前后的状态差异
func TestMentionService_BoundaryCases(t *testing.T) {
	botID := id.UserID("@bot:example.com")

	tests := []struct {
		name     string
		setup    func(*MentionService)
		testFunc func(*MentionService) error
	}{
		{
			name: "empty string mention check",
			setup: func(svc *MentionService) {
				svc.mu.Lock()
				svc.displayName = "Saber"
				svc.mu.Unlock()
			},
			testFunc: func(svc *MentionService) error {
				if svc.IsMentioned("") {
					return fmt.Errorf("empty string should not be mentioned")
				}
				return nil
			},
		},
		{
			name:  "nil content in ParseMention",
			setup: func(svc *MentionService) {},
			testFunc: func(svc *MentionService) error {
				// This should not panic
				_, _ = svc.ParseMention("test", nil)
				return nil
			},
		},
		{
			name: "very long message",
			setup: func(svc *MentionService) {
				svc.mu.Lock()
				svc.displayName = "Saber"
				svc.mu.Unlock()
			},
			testFunc: func(svc *MentionService) error {
				longMsg := strings.Repeat("x", 10000) + "Saber" + strings.Repeat("y", 10000)
				if !svc.IsMentioned(longMsg) {
					return fmt.Errorf("long message with mention should return true")
				}
				return nil
			},
		},
		{
			name: "unicode special characters",
			setup: func(svc *MentionService) {
				svc.mu.Lock()
				svc.displayName = "派蒙⚡️"
				svc.mu.Unlock()
			},
			testFunc: func(svc *MentionService) error {
				if !svc.IsMentioned("Hello 派蒙⚡️!") {
					return fmt.Errorf("unicode mention should work")
				}
				return nil
			},
		},
		{
			name: "init before and after",
			setup: func(svc *MentionService) {
				// Initially no display name
				if svc.GetDisplayName() != "" {
					return
				}
				// Mock Init to set display name
				svc.mu.Lock()
				svc.displayName = "InitializedBot"
				svc.mu.Unlock()
			},
			testFunc: func(svc *MentionService) error {
				if svc.GetDisplayName() != "InitializedBot" {
					return fmt.Errorf("display name not set after init")
				}
				return nil
			},
		},
		{
			name: "strip prefix edge cases",
			setup: func(svc *MentionService) {
				svc.mu.Lock()
				svc.displayName = "Bot"
				svc.mu.Unlock()
			},
			testFunc: func(svc *MentionService) error {
				// Test empty message
				result := svc.StripMentionPrefix("")
				if result != "" {
					return fmt.Errorf("empty message should return empty")
				}

				// Test message with only mention
				result = svc.StripMentionPrefix("@Bot")
				if result != "@Bot" { // Should return original since no content after
					return fmt.Errorf("mention-only message should return original")
				}

				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			tt.setup(svc)
			if err := tt.testFunc(svc); err != nil {
				t.Errorf("Test failed: %v", err)
			}
		})
	}
}

// TestStripMentionPrefix 测试 StripMentionPrefix 方法。
//
// 该测试覆盖以下场景：
//   - 显示名称前缀移除
//   - 用户 ID 前缀移除
//   - 混合情况处理
//   - 边界情况
func TestStripMentionPrefix(t *testing.T) {
	botID := id.UserID("@test-bot:example.com")

	tests := []struct {
		name        string
		displayName string
		msg         string
		want        string
	}{
		{
			name:        "display name prefix",
			displayName: "TestBot",
			msg:         "@TestBot hello world",
			want:        "hello world",
		},
		{
			name:        "user ID prefix",
			displayName: "",
			msg:         "@test-bot:example.com hello world",
			want:        "hello world",
		},
		{
			name:        "no prefix match",
			displayName: "OtherBot",
			msg:         "@TestBot hello world",
			want:        "@TestBot hello world",
		},
		{
			name:        "empty message",
			displayName: "TestBot",
			msg:         "",
			want:        "",
		},
		{
			name:        "only whitespace",
			displayName: "TestBot",
			msg:         "   ",
			want:        "   ",
		},
		{
			name:        "mention with no following text",
			displayName: "TestBot",
			msg:         "@TestBot",
			want:        "@TestBot",
		},
		{
			name:        "mixed case display name",
			displayName: "TestBot",
			msg:         "@testbot hello", // lowercase
			want:        "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			svc.mu.Lock()
			svc.displayName = tt.displayName
			svc.mu.Unlock()

			got := svc.StripMentionPrefix(tt.msg)
			if got != tt.want {
				t.Errorf("StripMentionPrefix(%q) = %q, want %q", tt.msg, got, tt.want)
			}
		})
	}
}
