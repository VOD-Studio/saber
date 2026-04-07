// Package commands 提供 Matrix 机器人的命令注册和处理机制。
package commands

import (
	"context"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// mockHandler 是用于测试的 Mock CommandHandler。
type mockHandler struct {
	called bool
	err    error
}

func (m *mockHandler) Handle(_ context.Context, _ id.UserID, _ id.RoomID, _ []string) error {
	m.called = true
	return m.err
}

// TestNewRegistry 测试 Registry 构造函数。
func TestNewRegistry(t *testing.T) {
	client := &mautrix.Client{}
	botID := id.UserID("@bot:server.com")

	registry := NewRegistry(client, botID)

	if registry == nil {
		t.Fatal("NewRegistry() returned nil")
	}
	if registry.Client() != client {
		t.Error("Client() mismatch")
	}
	if registry.BotID() != botID {
		t.Error("BotID() mismatch")
	}
}

// TestRegistry_Register 测试命令注册。
func TestRegistry_Register(t *testing.T) {
	registry := NewRegistry(nil, "@bot:server.com")

	handler := &mockHandler{}
	registry.Register("ping", handler)

	info, ok := registry.Get("ping")
	if !ok {
		t.Error("Get(ping) should return true")
	}
	if info.Name != "ping" {
		t.Errorf("Name = %q, want %q", info.Name, "ping")
	}
	if info.Handler != handler {
		t.Error("Handler mismatch")
	}
}

// TestRegistry_RegisterWithDesc 测试带描述的命令注册。
func TestRegistry_RegisterWithDesc(t *testing.T) {
	registry := NewRegistry(nil, "@bot:server.com")

	handler := &mockHandler{}
	registry.RegisterWithDesc("ping", "测试在线状态", handler)

	info, ok := registry.Get("ping")
	if !ok {
		t.Error("Get(ping) should return true")
	}
	if info.Description != "测试在线状态" {
		t.Errorf("Description = %q, want %q", info.Description, "测试在线状态")
	}
}

// TestRegistry_Register_CaseInsensitive 测试命令注册大小写不敏感。
func TestRegistry_Register_CaseInsensitive(t *testing.T) {
	registry := NewRegistry(nil, "@bot:server.com")

	handler := &mockHandler{}
	registry.Register("Ping", handler)

	// 小写查找
	info, ok := registry.Get("ping")
	if !ok {
		t.Error("Get(ping) should return true for registered Ping")
	}
	if info.Name != "Ping" {
		t.Errorf("Name = %q, want %q", info.Name, "Ping")
	}

	// 大写查找
	info, ok = registry.Get("PING")
	if !ok {
		t.Error("Get(PING) should return true for registered Ping")
	}
}

// TestRegistry_Unregister 测试命令注销。
func TestRegistry_Unregister(t *testing.T) {
	registry := NewRegistry(nil, "@bot:server.com")

	handler := &mockHandler{}
	registry.Register("ping", handler)

	_, ok := registry.Get("ping")
	if !ok {
		t.Error("Get(ping) should return true before unregister")
	}

	registry.Unregister("ping")

	_, ok = registry.Get("ping")
	if ok {
		t.Error("Get(ping) should return false after unregister")
	}
}

// TestRegistry_Get_NotFound 测试获取不存在的命令。
func TestRegistry_Get_NotFound(t *testing.T) {
	registry := NewRegistry(nil, "@bot:server.com")

	_, ok := registry.Get("nonexistent")
	if ok {
		t.Error("Get(nonexistent) should return false")
	}
}

// TestRegistry_List 测试命令列表。
func TestRegistry_List(t *testing.T) {
	registry := NewRegistry(nil, "@bot:server.com")

	// 空注册表
	list := registry.List()
	if len(list) != 0 {
		t.Errorf("List() length = %d, want 0", len(list))
	}

	// 注册多个命令
	handler := &mockHandler{}
	registry.Register("ping", handler)
	registry.Register("help", handler)
	registry.Register("ai", handler)

	list = registry.List()
	if len(list) != 3 {
		t.Errorf("List() length = %d, want 3", len(list))
	}

	// 验证所有命令都在列表中
	found := make(map[string]bool)
	for _, info := range list {
		found[info.Name] = true
	}
	for _, name := range []string{"ping", "help", "ai"} {
		if !found[name] {
			t.Errorf("List() missing command %q", name)
		}
	}
}

// TestRegistry_Parse 测试命令解析。
func TestRegistry_Parse(t *testing.T) {
	botID := id.UserID("@bot:server.com")
	registry := NewRegistry(nil, botID)

	tests := []struct {
		name     string
		input    string
		wantNil  bool
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "简单命令",
			input:    "!ping",
			wantNil:  false,
			wantCmd:  "ping",
			wantArgs: nil,
		},
		{
			name:     "带参数命令",
			input:    "!ai hello world",
			wantNil:  false,
			wantCmd:  "ai",
			wantArgs: []string{"hello", "world"},
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
			name:    "感叹号加空格",
			input:   "! ",
			wantNil: true,
		},
		{
			name:    "前导空格",
			input:   "  !ping",
			wantNil: false,
			wantCmd: "ping",
		},
		{
			name:    "尾部空格",
			input:   "!ping  ",
			wantNil: false,
			wantCmd: "ping",
		},
		{
			name:     "多参数",
			input:    "!test arg1 arg2 arg3",
			wantNil:  false,
			wantCmd:  "test",
			wantArgs: []string{"arg1", "arg2", "arg3"},
		},
		{
			name:    "大写命令转小写",
			input:   "!PING",
			wantNil: false,
			wantCmd: "ping",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registry.Parse(tt.input)
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
			if got.Command != tt.wantCmd {
				t.Errorf("Parse(%q).Command = %q, want %q", tt.input, got.Command, tt.wantCmd)
			}
			if len(got.Args) != len(tt.wantArgs) {
				t.Errorf("Parse(%q).Args length = %d, want %d", tt.input, len(got.Args), len(tt.wantArgs))
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

// TestRegistry_Parse_Mention 测试提及命令解析。
func TestRegistry_Parse_Mention(t *testing.T) {
	botID := id.UserID("@bot:server.com")
	registry := NewRegistry(nil, botID)

	tests := []struct {
		name     string
		input    string
		wantNil  bool
		wantCmd  string
		wantArgs []string
	}{
		{
			name:     "提及后跟命令",
			input:    "@bot:server.com ping",
			wantNil:  false,
			wantCmd:  "ping",
			wantArgs: nil,
		},
		{
			name:     "提及带冒号后跟命令",
			input:    "@bot:server.com: ping",
			wantNil:  false,
			wantCmd:  "ping",
			wantArgs: nil,
		},
		{
			name:     "提及带参数",
			input:    "@bot:server.com ai hello",
			wantNil:  false,
			wantCmd:  "ai",
			wantArgs: []string{"hello"},
		},
		{
			name:    "错误的提及",
			input:   "@other:server.com ping",
			wantNil: true,
		},
		{
			name:    "仅提及无命令",
			input:   "@bot:server.com",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registry.Parse(tt.input)
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
			if got.Command != tt.wantCmd {
				t.Errorf("Parse(%q).Command = %q, want %q", tt.input, got.Command, tt.wantCmd)
			}
		})
	}
}

// TestRegistry_Client 测试 Client 方法。
func TestRegistry_Client(t *testing.T) {
	client := &mautrix.Client{}
	registry := NewRegistry(client, "@bot:server.com")

	if registry.Client() != client {
		t.Error("Client() mismatch")
	}
}

// TestRegistry_BotID 测试 BotID 方法。
func TestRegistry_BotID(t *testing.T) {
	botID := id.UserID("@bot:server.com")
	registry := NewRegistry(nil, botID)

	if registry.BotID() != botID {
		t.Error("BotID() mismatch")
	}
}
