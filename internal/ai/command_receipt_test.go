package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/chat/memory"
	"rua.plus/saber/internal/config"
)

// stubEntrypoint 是平台接入端替身：只用内存 adapter 记录回执，
// 证明 !ai 子命令的输出不再经任何 Matrix 服务发出。
type stubEntrypoint struct {
	adapter *memory.Adapter
	session chat.Session
}

func newStubEntrypoint() *stubEntrypoint {
	session := chat.Session{Platform: "memory", Account: "@bot:stub", Conversation: "!room:stub"}
	return &stubEntrypoint{
		adapter: memory.New("@bot:stub", chat.Capabilities{Reply: true}, nil),
		session: session,
	}
}

func (e *stubEntrypoint) HandleCommand(context.Context, id.UserID, id.RoomID, []string, string) error {
	return nil
}

func (e *stubEntrypoint) NormalizeCommand(_ context.Context, _ id.UserID, _ id.RoomID, _ string) (chat.Message, chat.Adapter, error) {
	return chat.Message{Session: e.session, ID: "$incoming", SenderID: "@user:stub"}, e.adapter, nil
}

func (e *stubEntrypoint) Session(context.Context, id.RoomID) chat.Session { return e.session }

func (e *stubEntrypoint) OutboundAdapter() chat.Adapter { return e.adapter }

func (e *stubEntrypoint) EventID(context.Context) string { return "$incoming" }

// receiptServiceWith 构造一个完全没有 Matrix 依赖的 AI 服务并接入内存平台入口，
// 允许用例在构造前调整配置（例如关掉上下文管理）。
func receiptServiceWith(t *testing.T, tweak func(*config.Config)) (*Service, *stubEntrypoint) {
	t.Helper()
	cfg := createTestMultiProviderAIConfig()
	cfg.AI.DefaultModel = "openai.gpt-4"
	if tweak != nil {
		tweak(cfg)
	}
	service, err := NewService(cfg)
	require.NoError(t, err)
	entry := newStubEntrypoint()
	service.SetChatEntrypoint(entry)
	return service, entry
}

// onlyReply 返回平台收到的唯一一条回执。
func onlyReply(t *testing.T, entry *stubEntrypoint) chat.Reply {
	t.Helper()
	replies := entry.adapter.Replies()
	require.Len(t, replies, 1, "命令应只发一条回执")
	return replies[0]
}

// TestAICommands_ReceiptWithoutMatrix 验证模型与上下文类命令的回执全部经平台端口发出：
// 服务未注入 matrixService 也能完成，回执指向来源消息。
func TestAICommands_ReceiptWithoutMatrix(t *testing.T) {
	tests := []struct {
		name     string
		tweak    func(*config.Config)
		handle   func(*testing.T, *Service)
		contains []string
	}{
		{
			name: "models 列出模型表格",
			handle: func(t *testing.T, s *Service) {
				require.NoError(t, NewModelsCommand(s).Handle(context.Background(), "@user:stub", "!room:stub", nil))
			},
			contains: []string{"| 模型 ID | 实际模型 | 状态 |", "`openai.gpt-4`", "⭐ 当前默认"},
		},
		{
			name: "current 显示当前模型",
			handle: func(t *testing.T, s *Service) {
				require.NoError(t, NewCurrentModelCommand(s).Handle(context.Background(), "@user:stub", "!room:stub", nil))
			},
			contains: []string{"🤖 当前默认模型", "配置默认：`openai.gpt-4`"},
		},
		{
			name: "switch 缺参数时给出用法",
			handle: func(t *testing.T, s *Service) {
				require.NoError(t, NewSwitchModelCommand(s).Handle(context.Background(), "@user:stub", "!room:stub", nil))
			},
			contains: []string{"❌ 请指定模型 ID", "`!ai switch <model-id>`"},
		},
		{
			name: "switch 成功后报告原与新模型",
			handle: func(t *testing.T, s *Service) {
				require.NoError(t, NewSwitchModelCommand(s).Handle(context.Background(), "@user:stub", "!room:stub", []string{"openai.gpt-4o"}))
			},
			contains: []string{"✅ 默认模型已切换", "原模型：`openai.gpt-4`", "新模型：`openai.gpt-4o`"},
		},
		{
			name:  "context 未启用上下文管理",
			tweak: func(cfg *config.Config) { cfg.Agent.Context.Enabled = false },
			handle: func(t *testing.T, s *Service) {
				require.NoError(t, NewContextInfoCommand(s).Handle(context.Background(), "@user:stub", "!room:stub", nil))
			},
			contains: []string{"上下文管理未启用"},
		},
		{
			name:  "clear 未启用上下文管理",
			tweak: func(cfg *config.Config) { cfg.Agent.Context.Enabled = false },
			handle: func(t *testing.T, s *Service) {
				require.NoError(t, NewClearContextCommand(s).Handle(context.Background(), "@user:stub", "!room:stub", nil))
			},
			contains: []string{"上下文管理未启用"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, entry := receiptServiceWith(t, tt.tweak)
			tt.handle(t, service)

			reply := onlyReply(t, entry)
			assertReply(t, reply, entry.session)
			for _, want := range tt.contains {
				if !strings.Contains(reply.Text, want) {
					t.Errorf("回执缺少 %q:\n%s", want, reply.Text)
				}
			}
		})
	}
}

// TestAICommands_ContextReceiptWithTable 覆盖启用上下文后的表格回执。
func TestAICommands_ContextReceiptWithTable(t *testing.T) {
	cfg := createTestMultiProviderAIConfig()
	cfg.AI.DefaultModel = "openai.gpt-4"
	cfg.Agent.Context.Enabled = true
	cfg.Agent.Context.MaxMessages = 10
	service, err := NewService(cfg)
	require.NoError(t, err)
	entry := newStubEntrypoint()
	service.SetChatEntrypoint(entry)

	service.contextManager.history.AddMessage(entry.session.Key(), RoleUser, "你好", "@user:stub")
	service.contextManager.history.AddMessage(entry.session.Key(), RoleAssistant, "你好呀", "")

	require.NoError(t, NewContextInfoCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", nil))
	reply := onlyReply(t, entry)
	assertReply(t, reply, entry.session)
	for _, want := range []string{"📊 对话上下文信息", "| 项目 | 数值 |", "消息数量", "估算令牌数"} {
		if !strings.Contains(reply.Text, want) {
			t.Errorf("回执缺少 %q:\n%s", want, reply.Text)
		}
	}

	require.NoError(t, NewClearContextCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", nil))
	require.Contains(t, entry.adapter.Replies()[1].Text, "✅ 对话上下文已清除")
	count, _ := service.contextManager.history.GetContextSize(entry.session.Key())
	require.Zero(t, count, "清除后不应残留历史")
}

// TestAICommands_ReceiptWithoutEntrypoint 验证未接入平台时给出统一错误而不是 panic。
func TestAICommands_ReceiptWithoutEntrypoint(t *testing.T) {
	cfg := createTestMultiProviderAIConfig()
	cfg.AI.DefaultModel = "openai.gpt-4"
	service, err := NewService(cfg)
	require.NoError(t, err)

	require.ErrorIs(t, NewModelsCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", nil), ErrNoChatEntrypoint)
	require.ErrorIs(t, NewCurrentModelCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", nil), ErrNoChatEntrypoint)
	require.ErrorIs(t, NewSwitchModelCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", []string{"x"}), ErrNoChatEntrypoint)
	require.ErrorIs(t, NewClearContextCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", nil), ErrNoChatEntrypoint)
	require.ErrorIs(t, NewContextInfoCommand(service).Handle(context.Background(), "@user:stub", "!room:stub", nil), ErrNoChatEntrypoint)
}

// assertReply 检查回执落在来源会话并引用来源消息。
func assertReply(t *testing.T, reply chat.Reply, session chat.Session) {
	t.Helper()
	require.Equal(t, session, reply.Session)
	require.Equal(t, "$incoming", reply.ReplyTo)
	require.NotEmpty(t, strings.TrimSpace(reply.Text))
}
