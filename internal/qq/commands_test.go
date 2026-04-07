// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"context"
	"testing"
)

// MockCommandSender 是测试用的 Mock 消息发送器。
//
// 记录最后一次发送的消息，用于验证命令处理结果。
type MockCommandSender struct {
	LastUserID  string
	LastGroupID string
	LastMessage string
	LastError   error
}

// Send 实现 CommandSender 接口。
//
// 记录发送参数到结构体字段，便于测试验证。
func (m *MockCommandSender) Send(ctx context.Context, userID, groupID, message string) error {
	m.LastUserID = userID
	m.LastGroupID = groupID
	m.LastMessage = message
	return m.LastError
}

// TestCommandRegistry_Parse 测试命令解析功能。
func TestCommandRegistry_Parse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantNil  bool
		wantName string
		wantArgs []string
	}{
		{
			name:     "有效命令",
			input:    "!ping",
			wantNil:  false,
			wantName: "ping",
			wantArgs: nil,
		},
		{
			name:     "带参数命令",
			input:    "!ai 你好 世界",
			wantNil:  false,
			wantName: "ai",
			wantArgs: []string{"你好", "世界"},
		},
		{
			name:    "无前缀",
			input:   "ping",
			wantNil: true,
		},
		{
			name:    "空字符串",
			input:   "",
			wantNil: true,
		},
		{
			name:    "仅感叹号",
			input:   "!",
			wantNil: true,
		},
		{
			name:     "前导空格",
			input:    "  !ping",
			wantNil:  false,
			wantName: "ping",
			wantArgs: nil,
		},
		{
			name:     "尾部空格",
			input:    "!ping  ",
			wantNil:  false,
			wantName: "ping",
			wantArgs: nil,
		},
		{
			name:     "多参数",
			input:    "!test arg1 arg2 arg3",
			wantNil:  false,
			wantName: "test",
			wantArgs: []string{"arg1", "arg2", "arg3"},
		},
	}

	r := NewCommandRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Parse(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("Parse(%q) = %+v, want nil", tt.input, got)
				}
				return
			}
			if got == nil {
				t.Errorf("Parse(%q) = nil, want non-nil", tt.input)
				return
			}
			if got.Name != tt.wantName {
				t.Errorf("Parse(%q).Name = %q, want %q", tt.input, got.Name, tt.wantName)
			}
			if len(got.Args) != len(tt.wantArgs) {
				t.Errorf("Parse(%q).Args len = %d, want %d", tt.input, len(got.Args), len(tt.wantArgs))
				return
			}
			for i, arg := range got.Args {
				if arg != tt.wantArgs[i] {
					t.Errorf("Parse(%q).Args[%d] = %q, want %q", tt.input, i, arg, tt.wantArgs[i])
				}
			}
		})
	}
}

// TestCommandRegistry_Dispatch 测试命令分发功能。
func TestCommandRegistry_Dispatch(t *testing.T) {
	r := NewCommandRegistry()
	r.Register("ping", &PingCommand{}, "测试在线状态")

	mock := &MockCommandSender{}
	parsed := &ParsedCommand{Name: "ping"}

	found, err := r.Dispatch(context.Background(), "user1", "", parsed, mock)
	if err != nil {
		t.Errorf("Dispatch() error = %v", err)
	}
	if !found {
		t.Error("Dispatch() found = false, want true")
	}
	if mock.LastMessage != "pong" {
		t.Errorf("LastMessage = %q, want %q", mock.LastMessage, "pong")
	}
}

// TestCommandRegistry_Dispatch_NotFound 测试分发不存在的命令。
func TestCommandRegistry_Dispatch_NotFound(t *testing.T) {
	r := NewCommandRegistry()

	mock := &MockCommandSender{}
	parsed := &ParsedCommand{Name: "nonexistent"}

	found, err := r.Dispatch(context.Background(), "user1", "", parsed, mock)
	if err != nil {
		t.Errorf("Dispatch() error = %v", err)
	}
	if found {
		t.Error("Dispatch() found = true for nonexistent command, want false")
	}
}

// TestCommandRegistry_GetHelpText 测试帮助文本生成。
func TestCommandRegistry_GetHelpText(t *testing.T) {
	r := NewCommandRegistry()
	r.Register("ping", &PingCommand{}, "测试在线状态")
	r.Register("help", &HelpCommand{registry: r}, "显示帮助信息")

	help := r.GetHelpText()
	if help == "" {
		t.Error("GetHelpText() returned empty string")
	}
	if !contains(help, "!ping") {
		t.Error("GetHelpText() should contain !ping")
	}
	if !contains(help, "!help") {
		t.Error("GetHelpText() should contain !help")
	}
}

// contains 检查字符串是否包含子串。
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestPingCommand 测试 !ping 命令。
func TestPingCommand(t *testing.T) {
	cmd := &PingCommand{}
	mock := &MockCommandSender{}

	err := cmd.Handle(context.Background(), "user1", "group1", nil, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage != "pong" {
		t.Errorf("LastMessage = %q, want %q", mock.LastMessage, "pong")
	}
	if mock.LastUserID != "user1" {
		t.Errorf("LastUserID = %q, want %q", mock.LastUserID, "user1")
	}
	if mock.LastGroupID != "group1" {
		t.Errorf("LastGroupID = %q, want %q", mock.LastGroupID, "group1")
	}
}

// TestHelpCommand 测试 !help 命令。
func TestHelpCommand(t *testing.T) {
	r := NewCommandRegistry()
	r.Register("ping", &PingCommand{}, "测试在线状态")

	cmd := &HelpCommand{registry: r}
	mock := &MockCommandSender{}

	err := cmd.Handle(context.Background(), "user1", "", nil, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Help message should not be empty")
	}
	if !contains(mock.LastMessage, "ping") {
		t.Error("Help message should contain 'ping'")
	}
}

// TestVersionCommand 测试 !version 命令。
func TestVersionCommand(t *testing.T) {
	info := &BuildInfo{
		Version:       "1.0.0",
		GitCommit:     "abc123",
		GitBranch:     "main",
		BuildTime:     "2024-01-01T00:00:00Z",
		GoVersion:     "go1.21",
		BuildPlatform: "darwin/arm64",
	}
	cmd := &VersionCommand{buildInfo: info}
	mock := &MockCommandSender{}

	err := cmd.Handle(context.Background(), "user1", "", nil, mock)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
	if mock.LastMessage == "" {
		t.Error("Version message should not be empty")
	}
	if !contains(mock.LastMessage, "1.0.0") {
		t.Error("Version message should contain version number")
	}
	if !contains(mock.LastMessage, "abc123") {
		t.Error("Version message should contain git commit")
	}
}

// TestCommandRegistry_Concurrent 测试并发注册和调用。
func TestCommandRegistry_Concurrent(t *testing.T) {
	r := NewCommandRegistry()

	// 并发注册
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			name := string(rune('a' + id))
			r.Register(name, &PingCommand{}, "test command")
			done <- true
		}(i)
	}

	// 等待所有注册完成
	for i := 0; i < 10; i++ {
		<-done
	}

	// 并发读取
	for i := 0; i < 10; i++ {
		go func() {
			_ = r.GetHelpText()
			_ = r.Parse("!ping")
			done <- true
		}()
	}

	// 等待所有读取完成
	for i := 0; i < 10; i++ {
		<-done
	}
}
