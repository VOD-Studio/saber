package matrix

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/id"
)

func TestCreateReplyFallback(t *testing.T) {
	tests := []struct {
		name        string
		senderID    id.UserID
		originalMsg string
		replyMsg    string
		want        string
	}{
		{
			name:        "single line message",
			senderID:    "@alice:matrix.org",
			originalMsg: "Hello, World!",
			replyMsg:    "Hi there!",
			want:        "> <@alice:matrix.org> Hello, World!\n\nHi there!",
		},
		{
			name:        "multiline message",
			senderID:    "@bob:example.com",
			originalMsg: "Line 1\nLine 2\nLine 3",
			replyMsg:    "Thanks for the info!",
			want:        "> <@bob:example.com> Line 1\n> <@bob:example.com> Line 2\n> <@bob:example.com> Line 3\n\nThanks for the info!",
		},
		{
			name:        "empty original message",
			senderID:    "@charlie:test.org",
			originalMsg: "",
			replyMsg:    "Just a reply",
			want:        "Just a reply",
		},
		{
			name:        "empty reply message",
			senderID:    "@dave:server.com",
			originalMsg: "Original text",
			replyMsg:    "",
			want:        "> <@dave:server.com> Original text",
		},
		{
			name:        "both messages empty",
			senderID:    "@eve:domain.com",
			originalMsg: "",
			replyMsg:    "",
			want:        "",
		},
		{
			name:        "message with leading/trailing whitespace",
			senderID:    "@frank:chat.io",
			originalMsg: "  Trimmed message  ",
			replyMsg:    "Got it",
			want:        "> <@frank:chat.io>   Trimmed message  \n\nGot it",
		},
		{
			name:        "message with special characters",
			senderID:    "@user:matrix.org",
			originalMsg: "Special: @#$%^&*()",
			replyMsg:    "Nice!",
			want:        "> <@user:matrix.org> Special: @#$%^&*()\n\nNice!",
		},
		{
			name:        "message with emojis",
			senderID:    "@emoji:react.com",
			originalMsg: "Hello 👋 World 🌍",
			replyMsg:    "Hey! 🎉",
			want:        "> <@emoji:react.com> Hello 👋 World 🌍\n\nHey! 🎉",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CreateReplyFallback(tt.senderID, tt.originalMsg, tt.replyMsg)
			if got != tt.want {
				t.Errorf("CreateReplyFallback() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateReplyFallbackWithDisplayName(t *testing.T) {
	tests := []struct {
		name        string
		senderID    id.UserID
		displayName string
		originalMsg string
		replyMsg    string
		want        string
	}{
		{
			name:        "with display name",
			senderID:    "@alice:matrix.org",
			displayName: "Alice Smith",
			originalMsg: "Hello!",
			replyMsg:    "Hi!",
			want:        "> <Alice Smith> Hello!\n\nHi!",
		},
		{
			name:        "empty display name falls back to user ID",
			senderID:    "@bob:example.com",
			displayName: "",
			originalMsg: "Test",
			replyMsg:    "Reply",
			want:        "> <@bob:example.com> Test\n\nReply",
		},
		{
			name:        "multiline with display name",
			senderID:    "@user:server.com",
			displayName: "John Doe",
			originalMsg: "First line\nSecond line",
			replyMsg:    "Understood",
			want:        "> <John Doe> First line\n> <John Doe> Second line\n\nUnderstood",
		},
		{
			name:        "display name with spaces",
			senderID:    "@test:chat.org",
			displayName: "User Name 123",
			originalMsg: "Message",
			replyMsg:    "Response",
			want:        "> <User Name 123> Message\n\nResponse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CreateReplyFallbackWithDisplayName(tt.senderID, tt.displayName, tt.originalMsg, tt.replyMsg)
			if got != tt.want {
				t.Errorf("CreateReplyFallbackWithDisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateReplyFallback_EmptyOriginalPreservesReply(t *testing.T) {
	// 当原始消息为空时，应该只返回回复消息
	senderID := id.UserID("@test:example.com")
	originalMsg := ""
	replyMsg := "This is the reply content"

	result := CreateReplyFallback(senderID, originalMsg, replyMsg)

	if result != replyMsg {
		t.Errorf("Expected reply message only, got %q", result)
	}

	// 不应该包含引用标记
	if strings.Contains(result, ">") {
		t.Errorf("Empty original message should not produce quote markers, got %q", result)
	}
}

func TestCreateReplyFallback_MultipleNewlines(t *testing.T) {
	senderID := id.UserID("@user:matrix.org")
	originalMsg := "Line 1\n\nLine 3"
	replyMsg := "Reply"

	result := CreateReplyFallback(senderID, originalMsg, replyMsg)

	// 空行也应该被引用
	// 预期格式："> <@user> Line 1\n> <@user> \n> <@user> Line 3\n\nReply"
	lines := strings.Split(result, "\n")
	expectedLines := 5 // 3 quoted lines (including empty) + 1 empty separator + 1 reply
	if len(lines) != expectedLines {
		t.Errorf("Expected %d lines, got %d: %q", expectedLines, len(lines), result)
	}

	// 前 3 行（引用部分）都应该以 "> " 开头
	for i, line := range lines[:3] {
		if !strings.HasPrefix(line, "> ") {
			t.Errorf("Line %d should start with '> ', got %q", i, line)
		}
	}

	// 第 4 行应该是空行（引用和回复之间的分隔）
	if lines[3] != "" {
		t.Errorf("Line 3 should be empty separator, got %q", lines[3])
	}

	// 最后一行应该是回复内容
	if lines[4] != replyMsg {
		t.Errorf("Line 4 should be reply %q, got %q", replyMsg, lines[4])
	}
}

func BenchmarkCreateReplyFallback(b *testing.B) {
	senderID := id.UserID("@alice:matrix.org")
	originalMsg := "Hello, World! This is a test message."
	replyMsg := "Thanks for your message!"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CreateReplyFallback(senderID, originalMsg, replyMsg)
	}
}

func BenchmarkCreateReplyFallback_Multiline(b *testing.B) {
	senderID := id.UserID("@bob:example.com")
	originalMsg := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	replyMsg := "Got it!"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CreateReplyFallback(senderID, originalMsg, replyMsg)
	}
}
