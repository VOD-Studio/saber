package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
)

type choice struct{ label, value, description string }

func (m *model) choices() (string, []choice) {
	switch m.menu {
	case "sessions":
		items := []choice{{"＋ 新建会话", "", "开始新的对话"}}
		for _, session := range m.sessions {
			items = append(items, choice{clipped(session.Input, 54), session.Session, session.CreatedAt.Local().Format("01/02 15:04")})
		}
		return "继续一段对话", items
	case "models":
		var items []choice
		for _, model := range m.info.Models {
			items = append(items, choice{m.modelName(model.ID), model.ID, model.ID})
		}
		return "选择模型", items
	case "reasoning":
		return "思考等级", []choice{{"继承模型配置", "", "使用服务端为此模型配置的默认值"}, {"None", "none", "不启用额外推理"}, {"Low", "low", "较低"}, {"Medium", "medium", "中等"}, {"High", "high", "较高"}, {"XHigh", "xhigh", "更高"}, {"Max", "max", "最高"}}
	default:
		return "快捷操作", []choice{{"/new", "new", "新建会话 · Ctrl+N"}, {"/sessions", "sessions", "切换历史 · Ctrl+O"}, {"/model", "models", "选择模型 · Ctrl+P"}, {"/reasoning", "reasoning", "思考等级 · Ctrl+R"}, {"/sidebar", "sidebar", "收起或展开侧栏 · Ctrl+B"}, {"/tools", "tools", "展开工具详情 · Ctrl+T"}, {"F5", "reconnect", "刷新连接与会话"}, {"/quit", "quit", "离开界面 · Ctrl+Q"}}
	}
}
func (m *model) openMenu(menu string) tea.Cmd {
	m.menu, m.menuIndex = menu, 0
	m.filter.Reset()
	if menu == "" {
		m.filter.Blur()
		return m.input.Focus()
	}
	m.input.Blur()
	return m.filter.Focus()
}

func (m *model) filteredChoices() (string, []choice) {
	title, items := m.choices()
	query := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if query == "" {
		return title, items
	}
	var matches []choice
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.label+" "+item.description), query) {
			matches = append(matches, item)
		}
	}
	return title, matches
}

func (m *model) menuKey(msg tea.KeyPressMsg) tea.Cmd {
	_, items := m.filteredChoices()
	switch msg.String() {
	case "esc":
		return m.openMenu("")
	case "up":
		m.menuIndex = max(0, m.menuIndex-1)
	case "down":
		m.menuIndex = min(max(0, len(items)-1), m.menuIndex+1)
	case "enter":
		if len(items) == 0 {
			return nil
		}
		selected := items[min(m.menuIndex, len(items)-1)]
		menu := m.menu
		focus := m.openMenu("")
		switch menu {
		case "sessions":
			if selected.value == "" {
				return m.switchSession(uuid.NewString())
			}
			return m.switchSession(selected.value)
		case "models":
			m.selectedModel, m.effort = selected.value, ""
			m.notice = "后续消息使用 " + selected.value
		case "reasoning":
			m.effort = selected.value
			m.notice = "后续消息的思考等级：" + m.effectiveEffort()
		default:
			switch selected.value {
			case "new":
				return m.switchSession(uuid.NewString())
			case "tools":
				m.details = !m.details
				m.refresh()
			case "sidebar":
				m.sidebarHidden = !m.sidebarHidden
			case "quit":
				m.stopStream()
				return tea.Quit
			case "reconnect":
				m.stopStream()
				m.loading = true
				return m.boot()
			default:
				return m.openMenu(selected.value)
			}
		}
		return focus
	default:
		before := m.filter.Value()
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		if m.filter.Value() != before {
			m.menuIndex = 0
		}
		return cmd
	}
	return nil
}
func (m *model) command(text string) tea.Cmd {
	args := strings.Fields(text)
	m.input.Reset()
	switch args[0] {
	case "/":
		return m.openMenu("help")
	case "/new":
		return m.switchSession(uuid.NewString())
	case "/sessions":
		return m.openMenu("sessions")
	case "/model":
		if len(args) == 1 {
			return m.openMenu("models")
		}
		for _, model := range m.info.Models {
			if args[1] == model.ID {
				m.selectedModel, m.effort = model.ID, ""
				m.notice = "后续消息使用 " + model.ID
				return nil
			}
		}
		m.notice = "没有找到此模型；使用 /model 查看可用模型。"
	case "/reasoning":
		if len(args) == 1 {
			return m.openMenu("reasoning")
		}
		m.effort = args[1]
		if m.effort == "default" {
			m.effort = ""
		}
		m.notice = "后续消息的思考等级：" + m.effectiveEffort()
	case "/tools":
		m.details = !m.details
		m.refresh()
	case "/sidebar":
		m.sidebarHidden = !m.sidebarHidden
	case "/help":
		return m.openMenu("help")
	case "/quit", "/exit":
		m.stopStream()
		return tea.Quit
	default:
		m.notice = "未知命令；输入 /help 查看快捷操作。"
		m.input.SetValue(text)
	}
	return nil
}
func (m *model) menuWidth() int { return max(20, min(70, m.width-6)) }

func (m *model) menuView() string {
	title, items := m.filteredChoices()
	width, height := m.menuWidth(), max(8, m.height-4)
	normal, quiet := textStyle.Background(panel), mutedStyle.Background(panel)
	rowHeight := 2
	if height < 14 {
		rowHeight = 1
	}
	helpRows := 0
	if m.menu == "help" && height >= 14 {
		helpRows = 1
	}
	rows := max(1, (height-7-helpRows)/rowHeight)
	start := max(0, m.menuIndex-rows+1)
	end := min(len(items), start+rows)
	filter := lipgloss.NewStyle().Background(raised).Padding(0, 1).Width(width - 4).Render(m.filter.View())
	lines := []string{
		accentStyle.Bold(true).Background(panel).Render(clipped(title, width-4)),
		paint(filter, width-4, lipgloss.Height(filter), raised), "",
	}
	if len(items) == 0 {
		lines = append(lines, quiet.Render("没有匹配项"))
	}
	for i := start; i < end; i++ {
		style, description, prefix := normal, quiet, "  "
		if i == m.menuIndex {
			style = accentStyle.Background(selected).Bold(true)
			description = mutedStyle.Background(selected)
			prefix = "› "
		}
		lines = append(lines, style.Width(width-4).Render(prefix+clipped(items[i].label, width-6)))
		if rowHeight == 2 {
			lines = append(lines, description.Width(width-4).Render("  "+clipped(items[i].description, width-6)))
		}
	}
	hint := "↑↓ 选择 · ↵ 确定 · Esc 返回"
	if width < 40 {
		hint = "↑↓ 选择  ↵ 确定  Esc 返回"
	}
	if end < len(items) {
		hint = fmt.Sprintf("↓ 还有 %d 项 · Esc 返回", len(items)-end)
	}
	lines = append(lines, "")
	if helpRows > 0 {
		lines = append(lines, quiet.Render(clipped("Enter 发送 · Alt+Enter 换行", width-4)))
	}
	lines = append(lines, quiet.Render(clipped(hint, width-4)))
	view := lipgloss.NewStyle().Background(panel).Padding(1, 2).Width(width).Render(strings.Join(lines, "\n"))
	return paint(view, width, lipgloss.Height(view), panel)
}
