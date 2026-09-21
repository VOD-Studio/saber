// Package tui 提供连接常驻 Saber 服务的 Charm 终端聊天界面。
package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/mattn/go-isatty"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/server"
	"rua.plus/saber/internal/task"
)

type model struct {
	ctx                                         context.Context
	client                                      *server.Client
	width, height, bodyWidth                    int
	input                                       textarea.Model
	viewport                                    viewport.Model
	spinner                                     spinner.Model
	info                                        server.Info
	session, selectedModel, effort              string
	sessions                                    []server.Turn
	turns                                       []server.Turn
	connected, loading, sending, dirty, details bool
	notice                                      string
	menu                                        string
	menuIndex                                   int
	cursor, streamingID                         int64
	cancelStream                                context.CancelFunc
	generation                                  int
	pending                                     *server.Message
	pendingSession                              string
	stream                                      *server.Stream
	// 每轮尝试的临时文字与最终任务结果分开维护。
	live        map[int64]string
	stage       string
	renderCache map[string]string
}

type bootMsg struct {
	info     server.Info
	sessions []server.Turn
	err      error
}
type historyMsg struct {
	session string
	turns   []server.Turn
	err     error
}
type sentMsg struct {
	session string
	turn    server.Turn
	err     error
}
type streamMsg struct {
	generation int
	stream     *server.Stream
	update     server.Update
	err        error
}
type cancelledMsg struct {
	session string
	turn    server.Turn
	err     error
}
type retryMsg struct{ generation int }

// Run 在独立终端启动聊天界面，退出只取消客户端连接。
func Run(ctx context.Context, client *server.Client, session string) error {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return fmt.Errorf("saber chat 需要交互式终端")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, client, session)
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	m.stopStream()
	return err
}

func newModel(ctx context.Context, client *server.Client, session string) *model {
	input := textarea.New()
	input.Placeholder = "说说你想做什么…  / 查看命令"
	input.Prompt = "› "
	input.ShowLineNumbers = false
	input.CharLimit = 100000
	input.SetHeight(3)
	input.SetWidth(70)
	input.SetVirtualCursor(true)
	input.KeyMap.InsertNewline.SetKeys("alt+enter", "shift+enter", "ctrl+j")
	input.SetStyles(inputStyles())
	input.Focus()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(accentStyle))
	if session == "" {
		session = uuid.NewString()
	}
	m := &model{ctx: ctx, client: client, session: session, input: input, viewport: viewport.New(viewport.WithWidth(70), viewport.WithHeight(15)), spinner: spin, live: make(map[int64]string), loading: true, renderCache: make(map[string]string)}
	m.resize(100, 32)
	return m
}

func (m *model) Init() tea.Cmd { return tea.Batch(m.boot(), m.spinner.Tick, textarea.Blink) }
func (m *model) boot() tea.Cmd {
	client, ctx := m.client, m.ctx
	return func() tea.Msg {
		var result bootMsg
		result.err = client.JSON(ctx, "GET", "/v1/info", nil, &result.info)
		if result.err == nil && result.info.Enabled {
			result.err = client.JSON(ctx, "GET", "/v1/sessions", nil, &result.sessions)
		}
		return result
	}
}
func (m *model) loadHistory() tea.Cmd {
	client, ctx, session := m.client, m.ctx, m.session
	return func() tea.Msg {
		result := historyMsg{session: session}
		result.err = client.JSON(ctx, "GET", "/v1/sessions/"+url.PathEscape(session)+"/tasks", nil, &result.turns)
		return result
	}
}
func (m *model) stopStream() {
	if m.cancelStream != nil {
		m.cancelStream()
		m.cancelStream = nil
	}
	if m.stream != nil {
		if err := m.stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
			m.notice = "关闭订阅失败：" + err.Error()
		}
	}
	m.stream = nil
	m.generation++
}
func (m *model) subscribe(id int64) tea.Cmd {
	m.stopStream()
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancelStream = cancel
	m.streamingID = id
	client, session, cursor, generation := m.client, m.session, m.cursor, m.generation
	return func() tea.Msg {
		stream, err := client.Subscribe(ctx, session, id, cursor)
		if err != nil {
			return streamMsg{generation: generation, err: err}
		}
		update, err := stream.Next()
		if err != nil {
			err = closeWithError(stream, err)
		}
		return streamMsg{generation: generation, stream: stream, update: update, err: err}
	}
}
func closeWithError(stream *server.Stream, err error) error {
	if closeErr := stream.Close(); closeErr != nil && err == nil {
		return closeErr
	}
	return err
}
func readNext(stream *server.Stream, generation int) tea.Cmd {
	return func() tea.Msg {
		update, err := stream.Next()
		if err != nil {
			err = closeWithError(stream, err)
		}
		return streamMsg{generation: generation, stream: stream, update: update, err: err}
	}
}
func (m *model) active() bool {
	return len(m.turns) > 0 && (m.turns[len(m.turns)-1].Status == "queued" || m.turns[len(m.turns)-1].Status == "running")
}
func (m *model) submit() tea.Cmd {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return m.command(text)
	}
	if !m.connected || !m.info.Enabled || m.sending || m.loading {
		return nil
	}
	if m.active() {
		m.notice = "当前回答尚未结束；Ctrl+C 停止后可以继续。"
		return nil
	}
	if m.pending == nil || m.pending.Text != text || m.pendingSession != m.session {
		m.pending = &server.Message{ID: uuid.NewString(), Text: text, Model: m.selectedModel, Effort: m.effort}
		m.pendingSession = m.session
	}
	m.sending = true
	client, ctx, session, input := m.client, m.ctx, m.session, *m.pending
	return func() tea.Msg {
		result := sentMsg{session: session}
		result.err = client.JSON(ctx, "POST", "/v1/sessions/"+url.PathEscape(session)+"/messages", input, &result.turn)
		return result
	}
}
func (m *model) cancel() tea.Cmd {
	if !m.active() {
		return nil
	}
	client, ctx, session, id := m.client, m.ctx, m.session, m.turns[len(m.turns)-1].ID
	m.notice = "正在停止当前任务…"
	return func() tea.Msg {
		result := cancelledMsg{session: session}
		result.err = client.JSON(ctx, "POST", fmt.Sprintf("/v1/sessions/%s/tasks/%d/cancel", url.PathEscape(session), id), nil, &result.turn)
		return result
	}
}
func (m *model) switchSession(session string) tea.Cmd {
	if m.sending {
		return nil
	}
	m.stopStream()
	m.session, m.turns, m.menu, m.notice = session, nil, "", ""
	m.cursor, m.streamingID, m.pending, m.pendingSession = 0, 0, nil, ""
	m.input.Reset()
	m.live = make(map[int64]string)
	m.effort = ""
	m.loading = true
	m.refresh()
	return m.loadHistory()
}
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil
	case bootMsg:
		m.loading = false
		if msg.err != nil {
			m.connected = false
			m.notice = msg.err.Error()
			m.refresh()
			return m, nil
		}
		m.info, m.sessions, m.connected, m.notice = msg.info, msg.sessions, true, ""
		if m.selectedModel == "" {
			for _, choice := range m.info.Models {
				if choice.Default {
					m.selectedModel = choice.ID
					break
				}
			}
		}
		if !m.info.Enabled {
			m.notice = "请在服务端配置 ai.enabled 和模型，然后重启服务。"
			m.refresh()
			return m, nil
		}
		m.loading = true
		return m, m.loadHistory()
	case historyMsg:
		if msg.session != m.session {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		m.turns = msg.turns
		if len(m.turns) > 0 {
			last := m.turns[len(m.turns)-1]
			m.selectedModel, m.effort = last.Model, last.Effort
		}
		m.refresh()
		if m.active() {
			m.cursor = 0
			return m, m.subscribe(m.turns[len(m.turns)-1].ID)
		}
		return m, nil
	case sentMsg:
		m.sending = false
		if msg.session != m.session {
			return m, nil
		}
		if msg.err != nil {
			m.notice = msg.err.Error() + "；保留了输入，可再次发送。"
			return m, nil
		}
		m.input.Reset()
		m.pending, m.notice, m.cursor = nil, "", 0
		found := false
		for i := range m.turns {
			if m.turns[i].ID == msg.turn.ID {
				m.turns[i] = msg.turn
				found = true
			}
		}
		if !found {
			m.turns = append(m.turns, msg.turn)
		}
		m.updateSession(msg.turn)
		m.refresh()
		m.viewport.GotoBottom()
		return m, m.subscribe(msg.turn.ID)
	case streamMsg:
		if msg.generation != m.generation {
			if msg.stream != nil {
				if err := msg.stream.Close(); err != nil && !errors.Is(err, context.Canceled) {
					m.notice = "关闭旧订阅失败：" + err.Error()
				}
			}
			return m, nil
		}
		if msg.err != nil {
			if !m.active() {
				m.stopStream()
				return m, nil
			}
			m.connected = false
			m.notice = "连接已断开，正在恢复订阅；任务继续在服务端运行。"
			generation := m.generation
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return retryMsg{generation: generation} })
		}
		m.connected, m.stream = true, msg.stream
		if strings.HasPrefix(m.notice, "连接已断开") {
			m.notice = ""
		}
		if msg.update.Event != nil && msg.update.Event.ID > m.cursor {
			m.cursor = msg.update.Event.ID
			m.applyEvent(*msg.update.Event)
		}
		if msg.update.Task != nil {
			t := *msg.update.Task
			for i := range m.turns {
				if m.turns[i].ID == t.ID {
					if t.Status == "running" || t.Status == "queued" {
						t.Tools = m.turns[i].Tools
					}
					m.turns[i] = t
				}
			}
			m.updateSession(t)
			m.refresh()
			if t.Status != "running" && t.Status != "queued" {
				m.stopStream()
				return m, nil
			}
		}
		return m, readNext(msg.stream, m.generation)
	case retryMsg:
		if msg.generation != m.generation || !m.active() {
			return m, nil
		}
		return m, m.subscribe(m.streamingID)
	case cancelledMsg:
		if msg.session == m.session {
			if msg.err != nil {
				m.notice = msg.err.Error()
			} else {
				m.notice = "已请求停止，正在等待执行收尾。"
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+q" {
			m.stopStream()
			return m, tea.Quit
		}
		if key == "ctrl+c" {
			if m.active() {
				return m, m.cancel()
			}
			if m.input.Value() != "" {
				m.input.Reset()
				return m, nil
			}
			m.stopStream()
			return m, tea.Quit
		}
		if key == "f5" {
			m.stopStream()
			m.loading = true
			return m, m.boot()
		}
		if m.menu != "" {
			return m, m.menuKey(key)
		}
		switch key {
		case "ctrl+n":
			return m, m.switchSession(uuid.NewString())
		case "ctrl+o":
			m.menu, m.menuIndex = "sessions", 0
			return m, nil
		case "ctrl+p":
			m.menu, m.menuIndex = "models", 0
			return m, nil
		case "ctrl+r":
			m.menu, m.menuIndex = "reasoning", 0
			return m, nil
		case "ctrl+t":
			m.details = !m.details
			m.refresh()
			return m, nil
		case "enter":
			return m, m.submit()
		case "pgup", "pgdown", "ctrl+home", "ctrl+end":
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	case tea.MouseWheelMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case spinner.TickMsg:
		m.spinner, cmd = m.spinner.Update(msg)
		if m.dirty || m.active() {
			m.refresh()
		}
		return m, cmd
	}
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
func (m *model) updateSession(turn server.Turn) {
	for i := range m.sessions {
		if m.sessions[i].Session == turn.Session {
			m.sessions = append(m.sessions[:i], m.sessions[i+1:]...)
			break
		}
	}
	m.sessions = append([]server.Turn{turn}, m.sessions...)
}
func (m *model) applyEvent(event task.Record) {
	if len(m.turns) == 0 {
		return
	}
	t := &m.turns[len(m.turns)-1]
	switch event.Kind {
	case agent.RunStarted:
		m.stage = "准备回答"
	case agent.ModelStarted, agent.AttemptStarted:
		m.live[t.ID] = ""
		m.stage = "正在思考"
	case agent.TextDelta:
		m.live[t.ID] += event.Text
		m.stage = "正在回答"
	case agent.ModelCompleted:
		m.live[t.ID] = event.Text
	case agent.ToolStarted:
		m.stage = "正在使用工具"
		exists := false
		for i := range t.Tools {
			if t.Tools[i].Call.ID == event.Tool.Call.ID {
				t.Tools[i] = event.Tool
				exists = true
			}
		}
		if !exists {
			t.Tools = append(t.Tools, event.Tool)
		}
	case agent.ToolFinished:
		for i := range t.Tools {
			if t.Tools[i].Call.ID == event.Tool.Call.ID {
				t.Tools[i] = event.Tool
			}
		}
	}
	m.dirty = true
}
