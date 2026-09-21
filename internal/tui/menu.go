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
			items = append(items, choice{model.ID, model.ID, model.Name})
		}
		return "选择模型", items
	case "reasoning":
		return "思考等级", []choice{{"继承模型配置", "", "使用服务端为此模型配置的默认值"}, {"None", "none", "不启用额外推理"}, {"Low", "low", "较低"}, {"Medium", "medium", "中等"}, {"High", "high", "较高"}, {"XHigh", "xhigh", "更高"}, {"Max", "max", "最高"}}
	default:
		return "让对话保持顺手", []choice{{"/new", "new", "新建会话 · Ctrl+N"}, {"/sessions", "sessions", "切换历史 · Ctrl+O"}, {"/model", "models", "选择模型 · Ctrl+P"}, {"/reasoning", "reasoning", "思考等级 · Ctrl+R"}, {"/sidebar", "sidebar", "收起或展开侧栏 · Ctrl+B"}, {"/tools", "tools", "展开工具详情 · Ctrl+T"}, {"F5", "reconnect", "刷新连接与会话"}, {"/quit", "quit", "离开界面 · Ctrl+Q"}}
	}
}
func (m *model) menuKey(key string) tea.Cmd {
	_, items := m.choices()
	switch key {
	case "esc":
		m.menu = ""
		return nil
	case "up", "k":
		m.menuIndex = max(0, m.menuIndex-1)
	case "down", "j":
		m.menuIndex = min(max(0, len(items)-1), m.menuIndex+1)
	case "enter":
		if len(items) == 0 {
			return nil
		}
		selected := items[min(m.menuIndex, len(items)-1)]
		menu := m.menu
		m.menu = ""
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
				m.menu, m.menuIndex = selected.value, 0
			}
		}
	}
	return nil
}
func (m *model) command(text string) tea.Cmd {
	args := strings.Fields(text)
	m.input.Reset()
	switch args[0] {
	case "/":
		m.menu, m.menuIndex = "help", 0
	case "/new":
		return m.switchSession(uuid.NewString())
	case "/sessions":
		m.menu, m.menuIndex = "sessions", 0
	case "/model":
		if len(args) == 1 {
			m.menu, m.menuIndex = "models", 0
			break
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
			m.menu, m.menuIndex = "reasoning", 0
			break
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
		m.menu, m.menuIndex = "help", 0
	case "/quit", "/exit":
		m.stopStream()
		return tea.Quit
	default:
		m.notice = "未知命令；输入 /help 查看快捷操作。"
		m.input.SetValue(text)
	}
	return nil
}
func (m *model) menuView() string {
	title, items := m.choices()
	if m.viewport.Height() < 8 {
		lines := []string{accentStyle.Bold(true).Render(title)}
		if len(items) > 0 {
			lines = append(lines, textStyle.Render("› "+clipped(items[min(m.menuIndex, len(items)-1)].label, m.viewport.Width()-3)))
		}
		if m.viewport.Height() > 2 {
			lines = append(lines, mutedStyle.Render("↑↓ 选择  ↵ 确定  Esc 返回"))
		}
		return lipgloss.NewStyle().Width(m.viewport.Width()).Height(m.viewport.Height()).MaxHeight(m.viewport.Height()).Render(strings.Join(lines, "\n"))
	}
	width := max(20, min(70, m.viewport.Width()-4))
	rows := max(1, (m.viewport.Height()-6)/2)
	start := max(0, m.menuIndex-rows+1)
	end := min(len(items), start+rows)
	lines := []string{accentStyle.Bold(true).Render(title), ""}
	for i := start; i < end; i++ {
		style := textStyle
		prefix := "  "
		if i == m.menuIndex {
			style = lipgloss.NewStyle().Foreground(accent).Background(panel).Bold(true)
			prefix = "› "
		}
		lines = append(lines, style.Width(width-4).Render(prefix+clipped(items[i].label, width-7)))
		lines = append(lines, mutedStyle.Render("  "+clipped(items[i].description, width-7)))
	}
	hint := "↑↓ 选择  ↵ 确定  Esc 返回"
	if m.menu == "reasoning" {
		hint = "支持的等级由模型决定；/reasoning 可输入其他值"
	}
	lines = append(lines, "", mutedStyle.Render(clipped(hint, width-4)))
	if end < len(items) {
		lines[len(lines)-1] = mutedStyle.Render(fmt.Sprintf("↓ 还有 %d 项 · Esc 返回", len(items)-end))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(faint).Padding(0, 1).Width(width).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.viewport.Width(), m.viewport.Height(), lipgloss.Center, lipgloss.Center, box)
}
