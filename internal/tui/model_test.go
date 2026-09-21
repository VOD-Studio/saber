package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/ai"
	"rua.plus/saber/internal/server"
	"rua.plus/saber/internal/task"
)

func previewModel() *model {
	m := newModel(context.Background(), nil, "design")
	m.connected, m.loading = true, false
	m.selectedModel = "podlink-responses.gpt-5.6-sol"
	m.info = server.Info{Enabled: true, Models: []ai.ChatModel{{ID: m.selectedModel, Name: "GPT 5.6 Sol", ReasoningEffort: "medium", Default: true}}}
	return m
}

func TestModel_ResponsiveViews(t *testing.T) {
	for _, size := range [][2]int{{132, 40}, {100, 32}, {80, 24}, {44, 18}, {36, 14}, {24, 10}} {
		for _, state := range []string{"welcome", "chat", "models", "reasoning", "sessions", "help"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], state), func(t *testing.T) {
				m := previewModel()
				turn := server.Turn{ID: 1, Session: "design", Input: "将任务执行与结果投递拆开，让 TUI 和 Matrix 各自接收结果。", Content: "可以。任务会在服务端独立运行。\n\n### 一套执行，两种入口\n\n- TUI 实时展示进度与回答。\n- Matrix 按原会话投递结果。\n- 关闭界面后，任务继续执行。\n\n```go\nmanager.RegisterDelivery(\"matrix\", deliver)\n```", Model: m.selectedModel, Status: "completed", Tokens: 1842, Duration: 3400 * time.Millisecond, CreatedAt: time.Date(2026, 9, 21, 16, 30, 0, 0, time.Local), Tools: []agent.ToolRecord{{Call: openai.ToolCall{ID: "read", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"internal/task/manager.go"}`}}, Content: "已读取任务执行入口", Duration: 10 * time.Millisecond}}}
				if state != "welcome" {
					m.turns = []server.Turn{turn}
					m.sessions = []server.Turn{turn}
				}
				if state != "welcome" && state != "chat" {
					m.menu = state
				}
				m.resize(size[0], size[1])
				view := m.View()
				require.True(t, view.AltScreen)
				require.LessOrEqual(t, lipgloss.Height(view.Content), size[1])
				for _, line := range strings.Split(view.Content, "\n") {
					require.LessOrEqual(t, lipgloss.Width(line), size[0], "line: %q", clean(line))
				}
				if size[0] >= 36 && size[1] >= 14 {
					require.Contains(t, clean(view.Content), "SABER")
					if m.menu == "" {
						require.Contains(t, clean(view.Content), "发送")
					} else {
						require.Contains(t, clean(view.Content), "返回")
					}
				}
				if dir := os.Getenv("SABER_TUI_SNAPSHOT_DIR"); dir != "" && (state == "welcome" || state == "chat" || state == "models") {
					require.NoError(t, os.MkdirAll(dir, 0700))
					require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%dx%d-%s.ansi", size[0], size[1], state)), []byte(view.Content), 0600))
				}
			})
		}
	}
}

func TestModel_VisualHierarchy(t *testing.T) {
	m := previewModel()
	m.resize(100, 32)
	m.turns = []server.Turn{{ID: 1, Input: "你好", Content: "你好！", Model: m.selectedModel, Status: "completed", Tokens: 477, Duration: 2 * time.Second}}
	m.refresh()
	view := m.View().Content
	require.NotContains(t, clean(view), m.selectedModel)
	require.Contains(t, clean(view), "GPT 5.6 Sol")
	require.Contains(t, clean(view), "477 tokens")
	require.Equal(t, 1, strings.Count(clean(view), "›"))
	canvas := lipgloss.NewCanvas(m.width, m.height).Compose(lipgloss.NewLayer(view))
	for y := range m.height {
		for x := range m.width {
			cell := canvas.CellAt(x, y)
			if cell != nil && cell.Width > 0 {
				require.NotNil(t, cell.Style.Bg, "missing background at %d,%d", x, y)
			}
		}
	}
	m.turns[0].Status = "running"
	require.Contains(t, clean(m.composer()), "Ctrl+C 停止")
}

func TestModel_EventsRetryAndStaleSession(t *testing.T) {
	m := previewModel()
	m.turns = []server.Turn{{ID: 1, Status: "running"}}
	m.streamingID = 1
	m.applyEvent(task.Record{ID: 1, Kind: agent.TextDelta, Text: "旧尝试"})
	m.applyEvent(task.Record{ID: 2, Kind: agent.AttemptStarted})
	m.applyEvent(task.Record{ID: 3, Kind: agent.TextDelta, Text: "新的回答"})
	require.Equal(t, "新的回答", m.live[1])
	tool := agent.ToolRecord{Call: openai.ToolCall{ID: "call", Function: openai.FunctionCall{Name: "lookup"}}}
	m.applyEvent(task.Record{Kind: agent.ToolStarted, Tool: tool})
	tool.Content = "结果"
	m.applyEvent(task.Record{Kind: agent.ToolFinished, Tool: tool})
	require.Len(t, m.turns[0].Tools, 1)
	require.Equal(t, "结果", m.turns[0].Tools[0].Content)
	_, cmd := m.Update(streamMsg{generation: m.generation, err: errors.New("disconnected")})
	require.NotNil(t, cmd)
	require.False(t, m.connected)
	require.True(t, m.active())
	m.Update(historyMsg{session: "other", turns: []server.Turn{{ID: 99}}})
	require.EqualValues(t, 1, m.turns[0].ID)
	generation := m.generation
	m.stopStream()
	_, cmd = m.Update(retryMsg{generation: generation})
	require.Nil(t, cmd)
}

func TestModel_CommandsAndRequestFailure(t *testing.T) {
	m := previewModel()
	m.command("/reasoning high")
	require.Equal(t, "high", m.effort)
	require.Equal(t, "medium", m.info.Models[0].ReasoningEffort)
	m.command("/reasoning default")
	require.Equal(t, "medium", m.effectiveEffort())
	m.command("/model")
	require.Equal(t, "models", m.menu)
	m.menuKey("esc")
	require.Empty(t, m.menu)
	m.input.SetValue("保留这条消息")
	m.sending = true
	m.pending = &server.Message{ID: "stable", Text: m.input.Value()}
	m.Update(sentMsg{session: m.session, err: errors.New("network")})
	require.Equal(t, "保留这条消息", m.input.Value())
	require.Equal(t, "stable", m.pending.ID)
	require.False(t, m.sending)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	require.NotNil(t, cmd)
	require.IsType(t, tea.QuitMsg{}, cmd())
}

func TestClean_RemovesTerminalControls(t *testing.T) {
	require.Equal(t, "hello世界\nnext", clean("hello\x1b[31m世界\x1b[0m\x1b]52;c;ZXZpbA==\a\nnext\x00"))
}
