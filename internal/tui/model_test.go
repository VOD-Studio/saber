package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	for _, size := range [][2]int{{200, 50}, {132, 40}, {108, 32}, {108, 14}, {107, 32}, {100, 32}, {80, 24}, {64, 20}, {44, 18}, {36, 14}, {24, 10}} {
		for _, state := range []string{"welcome", "chat", "short-chat", "running", "queued", "error", "disconnected", "tools", "multiline", "long-model", "models", "reasoning", "sessions", "help", "commands"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], state), func(t *testing.T) {
				m := previewModel()
				turn := server.Turn{ID: 1, Session: "design", Input: "将任务执行与结果投递拆开，让 TUI 和 Matrix 各自接收结果。", Content: "可以。任务会在服务端独立运行。\n\n### 一套执行，两种入口\n\n- TUI 实时展示进度与回答。\n- Matrix 按原会话投递结果。\n- 关闭界面后，任务继续执行。\n\n```go\nmanager.RegisterDelivery(\"matrix\", deliver)\n```", Model: m.selectedModel, Status: "completed", Tokens: 1842, Duration: 3400 * time.Millisecond, CreatedAt: time.Date(2026, 9, 21, 16, 30, 0, 0, time.Local), Tools: []agent.ToolRecord{{Call: openai.ToolCall{ID: "read", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"internal/task/manager.go"}`}}, Content: "已读取任务执行入口", Duration: 10 * time.Millisecond}}}
				if state != "welcome" {
					m.turns = []server.Turn{turn}
					m.sessions = []server.Turn{turn}
					for i, title := range []string{"整理项目结构", "一个新的想法", "帮我解释这段代码"} {
						m.sessions = append(m.sessions, server.Turn{Session: fmt.Sprint(i), Input: title, CreatedAt: turn.CreatedAt.Add(-time.Duration(i+1) * time.Hour), Status: "completed"})
					}
				}
				switch state {
				case "short-chat":
					m.turns[0].Input, m.turns[0].Content, m.turns[0].Tools = "你好", "你好！今天想一起做点什么？", nil
				case "running", "queued":
					m.turns[0].Status = state
				case "error":
					m.turns[0].Status, m.turns[0].Error = "failed", "连接中断，请稍后再试。"
				case "disconnected":
					m.connected = false
					m.notice = "连接已断开，请按 F5 重新连接。"
				case "tools":
					m.details = true
				case "multiline":
					m.input.SetValue(strings.Repeat("中文与 emoji 🌱 长输入\n", 8))
				case "long-model":
					m.selectedModel = strings.Repeat("very-long-model-", 10)
					m.turns[0].Model = m.selectedModel
				case "models", "reasoning", "sessions", "help", "commands":
					m.openMenu(state)
				}
				if len(m.turns) > 0 {
					m.sessions[0] = m.turns[0]
				}
				m.resize(size[0], size[1])
				view := m.View()
				require.True(t, view.AltScreen)
				require.LessOrEqual(t, lipgloss.Height(view.Content), size[1])
				for _, line := range strings.Split(view.Content, "\n") {
					require.LessOrEqual(t, lipgloss.Width(line), size[0], "line: %q", clean(line))
				}
				if size[0] >= 36 && size[1] >= 14 {
					require.LessOrEqual(t, lipgloss.Width(m.composer()), m.bodyWidth, "composer must fit before the frame clips it")
					require.LessOrEqual(t, 2+lipgloss.Height(m.header(m.width-4))+m.viewport.Height()+lipgloss.Height(m.composer()), m.height)
					if m.menu != "" {
						require.LessOrEqual(t, lipgloss.Height(m.menuView()), m.height-4)
					}
					require.Contains(t, clean(view.Content), "SABER")
					if m.active() {
						require.Contains(t, clean(view.Content), "停止")
					} else if m.menu == "" {
						require.Contains(t, clean(view.Content), "发送")
					} else {
						require.Contains(t, clean(view.Content), "返回")
					}
				}
				if dir := os.Getenv("SABER_TUI_SNAPSHOT_DIR"); dir != "" {
					require.NoError(t, os.MkdirAll(dir, 0700))
					require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("%dx%d-%s.ansi", size[0], size[1], state)), []byte(view.Content), 0600))
				}
			})
		}
	}
}

func TestModel_PanelBackgrounds(t *testing.T) {
	m := previewModel()
	m.resize(132, 40)
	m.openMenu("models")
	composer := m.composer()
	for name, content := range map[string]string{
		"composer": strings.Join(strings.Split(composer, "\n")[:lipgloss.Height(composer)-1], "\n"),
		"sidebar":  m.sidebar(32),
		"menu":     m.menuView(),
	} {
		t.Run(name, func(t *testing.T) {
			canvas := lipgloss.NewCanvas(lipgloss.Width(content), lipgloss.Height(content)).Compose(lipgloss.NewLayer(content))
			for y := range lipgloss.Height(content) {
				for x := range lipgloss.Width(content) {
					cell := canvas.CellAt(x, y)
					if cell != nil && cell.Width > 0 {
						require.NotNil(t, cell.Style.Bg, "ANSI resets must not cut holes in a panel at %d,%d", x, y)
					}
				}
			}
		})
	}
}

func TestModel_ComposerResizeAndSidebar(t *testing.T) {
	m := previewModel()
	m.resize(132, 40)
	height := m.viewport.Height()
	require.Equal(t, 1, m.input.Height())
	text := strings.Repeat("中文与 emoji 🌱 输入不会因六行高度限制而丢失\n", 10)
	m.Update(tea.PasteMsg{Content: text})
	require.Equal(t, text, m.input.Value())
	require.Equal(t, 6, m.input.Height())
	require.Equal(t, height-5, m.viewport.Height())
	require.Contains(t, clean(m.View().Content), "发送")
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	require.Empty(t, m.input.Value())
	require.Equal(t, 1, m.input.Height())
	require.Equal(t, height, m.viewport.Height())
	m.Update(tea.PasteMsg{Content: strings.Repeat("宽字符", 40)})
	require.Greater(t, m.input.Height(), 1, "soft-wrapped input must grow too")
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	require.False(t, m.showSidebar())
	require.Equal(t, 108, m.bodyWidth)
	m.resize(80, 24)
	m.resize(200, 50)
	require.False(t, m.showSidebar(), "manual preference survives resize")
	require.Equal(t, 108, m.bodyWidth)
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	require.True(t, m.showSidebar())
	m.resize(36, 14)
	require.LessOrEqual(t, m.input.Height(), 2)
	require.GreaterOrEqual(t, m.viewport.Height(), 2)
	require.Contains(t, clean(m.View().Content), "发送")
}

func TestModel_ResizePreservesReadingPosition(t *testing.T) {
	m := previewModel()
	m.turns = []server.Turn{{ID: 1, Status: "completed", Content: strings.Repeat("历史内容\n\n", 50)}}
	m.resize(100, 32)
	m.viewport.SetYOffset(10)
	m.Update(tea.PasteMsg{Content: "第一行\n第二行\n第三行"})
	require.Equal(t, 10, m.viewport.YOffset())
	m.viewport.GotoBottom()
	m.Update(tea.PasteMsg{Content: "\n第四行"})
	require.True(t, m.viewport.AtBottom())
}

func TestModel_ControlCClearsInput(t *testing.T) {
	for _, status := range []string{"completed", "running", "queued"} {
		for _, menu := range []string{"", "models", "commands"} {
			t.Run(status+"/"+menu, func(t *testing.T) {
				m := previewModel()
				m.turns = []server.Turn{{ID: 42, Session: m.session, Status: status}}
				streamCtx, cancel := context.WithCancel(m.ctx)
				t.Cleanup(cancel)
				m.cancelStream = cancel
				m.input.SetValue("尚未发送的草稿\n第二行")
				m.openMenu(menu)
				if menu != "" {
					m.filter.SetValue("model")
				}
				m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
				require.Empty(t, m.input.Value())
				require.Empty(t, m.menu)
				require.Empty(t, m.filter.Value())
				require.True(t, m.input.Focused())
				require.Equal(t, status, m.turns[0].Status)
				require.NoError(t, streamCtx.Err(), "clearing a draft must keep the event stream open")
				require.Empty(t, m.notice, "Ctrl+C must not request cancellation")
				_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
				require.Nil(t, cmd, "Ctrl+C on an empty input must neither quit nor cancel")
			})
		}
	}
}

func TestModel_EscStopsCurrentTask(t *testing.T) {
	for _, status := range []string{"running", "queued"} {
		t.Run(status, func(t *testing.T) {
			requests := make(chan string, 1)
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Method + " " + r.URL.Path
				if err := json.NewEncoder(w).Encode(server.Turn{ID: 42, Status: "cancelled"}); err != nil {
					t.Error(err)
				}
			}))
			defer endpoint.Close()
			m := previewModel()
			var err error
			m.client, err = server.NewClient(endpoint.URL, "test-token")
			require.NoError(t, err)
			m.turns = []server.Turn{{ID: 1, Status: "completed"}, {ID: 42, Session: m.session, Status: status}}
			m.input.SetValue("保留草稿")
			m.openMenu("models")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			require.Empty(t, m.menu)
			require.Empty(t, m.notice, "Esc dismisses a dialog before stopping the task")
			require.Equal(t, "保留草稿", m.input.Value())
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			require.NotNil(t, cmd)
			result := cmd().(cancelledMsg)
			require.NoError(t, result.err)
			require.Equal(t, "POST /v1/sessions/design/tasks/42/cancel", <-requests)
			require.Equal(t, m.session, result.session)
			m.Update(result)
			require.Contains(t, m.notice, "已请求停止")
			require.Equal(t, "保留草稿", m.input.Value())
			require.True(t, m.input.Focused())
		})
	}
	m := previewModel()
	m.input.SetValue("空闲草稿")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, cmd)
	require.Equal(t, "空闲草稿", m.input.Value())
}

func TestModel_MenuSearchPreservesDraftAndHistory(t *testing.T) {
	m := previewModel()
	m.info.Models = append(m.info.Models, ai.ChatModel{ID: "provider.other", Name: "另一个模型"})
	m.input.SetValue("尚未发送的草稿")
	m.turns = []server.Turn{{ID: 1, Status: "completed", Content: strings.Repeat("历史内容\n\n", 50)}}
	m.resize(100, 32)
	m.viewport.SetYOffset(8)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	require.False(t, m.input.Focused())
	require.True(t, m.filter.Focused())
	m.Update(tea.PasteMsg{Content: "另一个"})
	_, items := m.filteredChoices()
	require.Len(t, items, 1)
	require.Equal(t, "provider.other", items[0].value)
	require.Contains(t, clean(m.View().Content), "历史内容", "dialog must overlay the transcript")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Empty(t, m.menu)
	require.Equal(t, "provider.other", m.selectedModel)
	require.Equal(t, "尚未发送的草稿", m.input.Value())
	require.Equal(t, 8, m.viewport.YOffset())
	require.True(t, m.input.Focused())
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	require.Equal(t, "j", m.filter.Value(), "letter keys must filter rather than navigate")
	require.Contains(t, clean(m.menuView()), "没有匹配项")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "models", m.menu)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Empty(t, m.menu)
	require.True(t, m.input.Focused())
}

func TestModel_SlashCommandMenu(t *testing.T) {
	m := previewModel()
	m.turns = []server.Turn{{ID: 1, Status: "completed", Content: strings.Repeat("历史内容\n\n", 50)}}
	m.resize(100, 32)
	m.viewport.SetYOffset(8)
	slash := tea.KeyPressMsg{Code: '/', Text: "/"}
	m.Update(slash)
	require.Equal(t, "commands", m.menu, "slash opens the menu without Enter")
	require.True(t, m.filter.Focused())
	require.False(t, m.input.Focused())
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Zero(t, m.menuIndex)
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	_, items := m.filteredChoices()
	require.Equal(t, "sessions", items[m.menuIndex].value)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "sessions", m.menu)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Empty(t, m.input.Value(), "executed commands must not remain in the draft")
	require.Equal(t, 8, m.viewport.YOffset())
	require.True(t, m.input.Focused())

	m.Update(slash)
	m.Update(tea.PasteMsg{Content: "tools"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, m.details)
	require.Empty(t, m.menu)
	m.Update(slash)
	m.Update(tea.PasteMsg{Content: "reasoning high"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "high", m.effort)
	require.Empty(t, m.input.Value())
	m.Update(slash)
	m.Update(tea.PasteMsg{Content: "reasoning low"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, "/reasoning low", m.input.Value(), "Esc restores the typed command for editing")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "low", m.effort)
	m.Update(slash)
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.Empty(t, m.menu)
	require.Empty(t, m.input.Value())
	require.True(t, m.input.Focused())
}

func TestModel_SlashInsideTextAndPaste(t *testing.T) {
	for _, draft := range []string{"https:/", "查看 internal", "第一行\n"} {
		t.Run(draft, func(t *testing.T) {
			m := previewModel()
			m.input.SetValue(draft)
			m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
			require.Empty(t, m.menu)
			require.Equal(t, draft+"/", m.input.Value())
		})
	}
	m := previewModel()
	m.Update(tea.PasteMsg{Content: "/reasoning high"})
	require.Empty(t, m.menu, "pasting must preserve the complete command")
	require.Equal(t, "/reasoning high", m.input.Value())
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, "high", m.effort)
}

func TestModel_NewContentWhileReading(t *testing.T) {
	m := previewModel()
	m.turns = []server.Turn{{ID: 1, Status: "running"}}
	m.live[1] = strings.Repeat("正在阅读的历史\n\n", 60)
	m.resize(100, 32)
	m.viewport.SetYOffset(10)
	m.applyEvent(task.Record{Kind: agent.TextDelta, Text: "新的内容"})
	m.refresh()
	require.Equal(t, 10, m.viewport.YOffset())
	require.True(t, m.unread)
	require.Contains(t, clean(m.composer()), "Ctrl+End")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	require.True(t, m.viewport.AtBottom())
	require.False(t, m.unread)
	m.applyEvent(task.Record{Kind: agent.TextDelta, Text: "继续追加"})
	m.refresh()
	require.True(t, m.viewport.AtBottom())
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	require.Equal(t, 0, m.viewport.YOffset())
}

func TestToolSummary(t *testing.T) {
	for _, tc := range []struct{ args, want string }{
		{`{"path":"internal/tui/view.go","offset":10}`, "internal/tui/view.go"},
		{`{"command":"go test -tags goolm ./internal/tui"}`, "go test -tags goolm ./internal/tui"},
		{`{"query":"中文搜索"}`, "中文搜索"},
		{`{"count":3}`, `{"count":3}`},
		{"unstructured\noutput", "unstructured output"},
	} {
		t.Run(tc.args, func(t *testing.T) { require.Equal(t, tc.want, toolSummary(tc.args)) })
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
	require.Contains(t, clean(m.composer()), "Esc 停止")
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
	m.menuKey(tea.KeyPressMsg{Code: tea.KeyEscape})
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
