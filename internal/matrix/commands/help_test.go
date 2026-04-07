// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/id"
)

// mockLister 是测试用的 Mock CommandLister。
type mockLister struct {
	commands []CommandInfo
}

func (m *mockLister) List() []CommandInfo {
	return m.commands
}

// TestNewHelpCommand 测试 HelpCommand 构造函数。
func TestNewHelpCommand(t *testing.T) {
	sender := &mockSender{}
	lister := &mockLister{}

	cmd := NewHelpCommand(sender, lister)

	if cmd == nil {
		t.Fatal("NewHelpCommand() returned nil")
	}
	if cmd.sender != sender {
		t.Error("sender mismatch")
	}
	if cmd.lister != lister {
		t.Error("lister mismatch")
	}
}

// TestHelpCommand_Handle 测试 HelpCommand.Handle 正常场景。
func TestHelpCommand_Handle(t *testing.T) {
	sender := &mockSender{}
	lister := &mockLister{
		commands: []CommandInfo{
			{Name: "ping", Description: "测试在线状态"},
			{Name: "help", Description: "显示帮助信息"},
		},
	}
	cmd := NewHelpCommand(sender, lister)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证 HTML 包含表格结构
	if !strings.Contains(sender.lastHTML, "<table>") {
		t.Error("HTML should contain <table>")
	}
	if !strings.Contains(sender.lastHTML, "<thead>") {
		t.Error("HTML should contain <thead>")
	}
	if !strings.Contains(sender.lastHTML, "<tbody>") {
		t.Error("HTML should contain <tbody>")
	}

	// 验证 HTML 包含命令
	if !strings.Contains(sender.lastHTML, "!ping") {
		t.Error("HTML should contain !ping")
	}
	if !strings.Contains(sender.lastHTML, "!help") {
		t.Error("HTML should contain !help")
	}

	// 验证纯文本包含命令
	if !strings.Contains(sender.lastPlain, "ping") {
		t.Error("plain text should contain ping")
	}
	if !strings.Contains(sender.lastPlain, "测试在线状态") {
		t.Error("plain text should contain description")
	}
}

// TestHelpCommand_Handle_EmptyList 测试空命令列表。
func TestHelpCommand_Handle_EmptyList(t *testing.T) {
	sender := &mockSender{}
	lister := &mockLister{commands: []CommandInfo{}}
	cmd := NewHelpCommand(sender, lister)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证返回空命令提示
	if sender.lastPlain != "暂无可用命令" {
		t.Errorf("plain = %q, want %q", sender.lastPlain, "暂无可用命令")
	}
}

// TestHelpCommand_Handle_NoDescription 测试无描述的命令。
func TestHelpCommand_Handle_NoDescription(t *testing.T) {
	sender := &mockSender{}
	lister := &mockLister{
		commands: []CommandInfo{
			{Name: "test", Description: ""},
		},
	}
	cmd := NewHelpCommand(sender, lister)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证 HTML 中无描述时显示 "-"
	if !strings.Contains(sender.lastHTML, "<td>-</td>") {
		t.Error("HTML should show '-' for empty description")
	}
}

// TestHelpCommand_Handle_WithError 测试发送失败场景。
func TestHelpCommand_Handle_WithError(t *testing.T) {
	expectedErr := context.Canceled
	sender := &mockSender{err: expectedErr}
	lister := &mockLister{
		commands: []CommandInfo{{Name: "ping", Description: "test"}},
	}
	cmd := NewHelpCommand(sender, lister)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != expectedErr {
		t.Errorf("Handle() error = %v, want %v", err, expectedErr)
	}
}

// TestSanitizeHTML 测试 HTML 净化函数。
func TestSanitizeHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "普通文本",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "转义小于号",
			input:    "<script>",
			expected: "&lt;script&gt;",
		},
		{
			name:     "转义大于号",
			input:    "a > b",
			expected: "a &gt; b",
		},
		{
			name:     "转义和号",
			input:    "a & b",
			expected: "a &amp; b",
		},
		{
			name:     "转义引号",
			input:    `say "hello"`,
			expected: "say &quot;hello&quot;",
		},
		{
			name:     "混合转义",
			input:    `<div class="test">a & b</div>`,
			expected: "&lt;div class=&quot;test&quot;&gt;a &amp; b&lt;/div&gt;",
		},
		{
			name:     "空字符串",
			input:    "",
			expected: "",
		},
		{
			name:     "中文内容",
			input:    "测试中文",
			expected: "测试中文",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeHTML(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeHTML(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestSanitizeHTML_InHelpCommand 测试 HTML 净化在 HelpCommand 中的应用。
func TestSanitizeHTML_InHelpCommand(t *testing.T) {
	sender := &mockSender{}
	lister := &mockLister{
		commands: []CommandInfo{
			{Name: "test<script>", Description: "<b>bold</b>"},
		},
	}
	cmd := NewHelpCommand(sender, lister)

	ctx := context.Background()
	userID := id.UserID("@user:server.com")
	roomID := id.RoomID("!room:server.com")

	err := cmd.Handle(ctx, userID, roomID, nil)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}

	// 验证恶意内容被转义
	if strings.Contains(sender.lastHTML, "<script>") {
		t.Error("HTML should not contain unescaped <script>")
	}
	if strings.Contains(sender.lastHTML, "<b>bold</b>") {
		t.Error("HTML should not contain unescaped <b>")
	}

	// 验证转义后的内容存在
	if !strings.Contains(sender.lastHTML, "&lt;script&gt;") {
		t.Error("HTML should contain escaped <script>")
	}
	if !strings.Contains(sender.lastHTML, "&lt;b&gt;bold&lt;/b&gt;") {
		t.Error("HTML should contain escaped <b>")
	}
}
