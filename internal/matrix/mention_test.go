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
			format:      "",
			formatted:   "",
			wantMention: true,
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
			name:        "special characters with colon separator",
			displayName: "派蒙",
			body:        "派蒙：你好！",
			wantMention: true,
			wantMsg:     "你好！",
		},
		{
			name:        "special characters with space separator",
			displayName: "派蒙",
			body:        "派蒙 你好！",
			wantMention: true,
			wantMsg:     "你好！",
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
		{
			name:        "english colon separator",
			displayName: "派蒙",
			body:        "派蒙: 123",
			wantMention: true,
			wantMsg:     "123",
		},
		{
			name:        "english comma separator",
			displayName: "Bot",
			body:        "Bot, hello world",
			wantMention: true,
			wantMsg:     "hello world",
		},
		{
			name:        "fullwidth comma separator",
			displayName: "机器人",
			body:        "机器人，你好",
			wantMention: true,
			wantMsg:     "你好",
		},
		{
			name:        "display name in middle with colon",
			displayName: "派蒙",
			body:        "你好 派蒙: 测试",
			wantMention: true,
			wantMsg:     "你好 : 测试",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			// 通过直接设置模拟显示名称（绕过 Init）
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
	for range goroutines {
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
	for range goroutines {
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
	for range goroutines {
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
	for range totalTests {
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
			name: "空字符串提及检查",
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
				// 这不应该 panic
				_, _ = svc.ParseMention("test", nil)
				return nil
			},
		},
		{
			name: "超长消息",
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
			name: "Unicode 特殊字符",
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
			name: "初始化前后",
			setup: func(svc *MentionService) {
				// 初始时没有显示名称
				if svc.GetDisplayName() != "" {
					return
				}
				// 模拟 Init 设置显示名称
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
			name: "去除前缀边界情况",
			setup: func(svc *MentionService) {
				svc.mu.Lock()
				svc.displayName = "Bot"
				svc.mu.Unlock()
			},
			testFunc: func(svc *MentionService) error {
				// 测试空消息
				result := svc.StripMentionPrefix("")
				if result != "" {
					return fmt.Errorf("empty message should return empty")
				}

				// 测试仅包含 mention 的消息
				result = svc.StripMentionPrefix("@Bot")
				if result != "@Bot" { // 应该返回原始内容，因为后面没有其他内容
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
		{
			name:        "display name without @ prefix with colon",
			displayName: "派蒙",
			msg:         "派蒙: 44",
			want:        "44",
		},
		{
			name:        "display name without @ prefix with fullwidth colon",
			displayName: "派蒙",
			msg:         "派蒙：hello",
			want:        "hello",
		},
		{
			name:        "display name without @ prefix with space",
			displayName: "TestBot",
			msg:         "TestBot hello world",
			want:        "hello world",
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

// TestParseMentions 测试 ParseMentions 方法。
//
// 该测试覆盖以下场景：
//   - nil 事件输入
//   - MSC 3952 结构化 mentions 提取（包含用户）
//   - MSC 3952 结构化 mentions 提取（包含 @room）
//   - HTML pills mentions 提取
//   - 两种格式的合并和去重
//   - 空 mentions 和边界情况
func TestParseMentions(t *testing.T) {
	botID := id.UserID("@bot:example.com")
	user1 := id.UserID("@user1:example.com")
	user2 := id.UserID("@user2:example.com")

	tests := []struct {
		name        string
		evt         *event.Event
		wantUserIDs []id.UserID
		wantRoom    bool
	}{
		{
			name:        "nil event",
			evt:         nil,
			wantUserIDs: []id.UserID{},
			wantRoom:    false,
		},
		{
			name: "MSC 3952 用户提及",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Mentions: &event.Mentions{
							UserIDs: []id.UserID{user1, user2},
						},
					},
				},
			},
			wantUserIDs: []id.UserID{user1, user2},
			wantRoom:    false,
		},
		{
			name: "MSC 3952 @room 提及",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Mentions: &event.Mentions{
							Room: true,
						},
					},
				},
			},
			wantUserIDs: []id.UserID{},
			wantRoom:    true,
		},
		{
			name: "MSC 3952 空提及",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Mentions: &event.Mentions{},
					},
				},
			},
			wantUserIDs: []id.UserID{},
			wantRoom:    false,
		},
		{
			name: "HTML Pills 提及",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Format:        event.FormatHTML,
						FormattedBody: `<a href="https://matrix.to/#/@user1:example.com">user1</a> hello <a href="https://matrix.to/#/@user2:example.com">user2</a>`,
					},
				},
			},
			wantUserIDs: []id.UserID{user1, user2},
			wantRoom:    false,
		},
		{
			name: "HTML Pills 空格式化正文",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Format:        event.FormatHTML,
						FormattedBody: "",
					},
				},
			},
			wantUserIDs: []id.UserID{},
			wantRoom:    false,
		},
		{
			name: "非 HTML 格式",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Format:        "",
						FormattedBody: `<a href="https://matrix.to/#/@user1:example.com">user1</a>`,
					},
				},
			},
			wantUserIDs: []id.UserID{},
			wantRoom:    false,
		},
		{
			name: "组合提及去重",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Mentions: &event.Mentions{
							UserIDs: []id.UserID{user1},
							Room:    true,
						},
						Format:        event.FormatHTML,
						FormattedBody: `<a href="https://matrix.to/#/@user1:example.com">user1</a> and <a href="https://matrix.to/#/@user2:example.com">user2</a>`,
					},
				},
			},
			wantUserIDs: []id.UserID{user1, user2},
			wantRoom:    true,
		},
		{
			name: "组合提及重复用户",
			evt: &event.Event{
				Content: event.Content{
					Parsed: &event.MessageEventContent{
						Mentions: &event.Mentions{
							UserIDs: []id.UserID{user1, user2},
						},
						Format:        event.FormatHTML,
						FormattedBody: `<a href="https://matrix.to/#/@user2:example.com">user2</a> and <a href="https://matrix.to/#/@user1:example.com">user1</a>`,
					},
				},
			},
			wantUserIDs: []id.UserID{user1, user2},
			wantRoom:    false,
		},
		{
			name: "无效消息内容类型",
			evt: &event.Event{
				Content: event.Content{
					Parsed: "not a message event content",
				},
			},
			wantUserIDs: []id.UserID{},
			wantRoom:    false,
		},
		{
			name: "空内容解析",
			evt: &event.Event{
				Content: event.Content{
					Parsed: nil,
				},
			},
			wantUserIDs: []id.UserID{},
			wantRoom:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewMentionService(&mautrix.Client{}, botID)
			got := svc.ParseMentions(tt.evt)

			// 检查 UserIDs
			if len(got.UserIDs) != len(tt.wantUserIDs) {
				t.Errorf("ParseMentions() UserIDs length = %d, want %d", len(got.UserIDs), len(tt.wantUserIDs))
			} else {
				// 检查每个 UserID 是否存在（顺序可能不同，因为 Merge 可能影响顺序）
				wantMap := make(map[id.UserID]bool)
				for _, uid := range tt.wantUserIDs {
					wantMap[uid] = true
				}
				for _, uid := range got.UserIDs {
					if !wantMap[uid] {
						t.Errorf("ParseMentions() unexpected UserID: %s", uid)
					}
					delete(wantMap, uid)
				}
				if len(wantMap) > 0 {
					t.Errorf("ParseMentions() missing UserIDs: %v", wantMap)
				}
			}

			// 检查 Room 标志
			if got.Room != tt.wantRoom {
				t.Errorf("ParseMentions() Room = %v, want %v", got.Room, tt.wantRoom)
			}
		})
	}
}
