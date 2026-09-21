package tui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	glamouransi "charm.land/glamour/v2/ansi"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"rua.plus/saber/internal/server"
)

var (
	background  = lipgloss.Color("#10161E")
	panel       = lipgloss.Color("#17212B")
	foreground  = lipgloss.Color("#DFE8EF")
	muted       = lipgloss.Color("#8A9CAB")
	faint       = lipgloss.Color("#344552")
	accent      = lipgloss.Color("#83E2C4")
	gold        = lipgloss.Color("#E9C58E")
	danger      = lipgloss.Color("#F29898")
	accentStyle = lipgloss.NewStyle().Foreground(accent)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	textStyle   = lipgloss.NewStyle().Foreground(foreground)
)

func inputStyles() textarea.Styles {
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Base = lipgloss.NewStyle().Foreground(foreground).Background(panel)
	styles.Focused.Text = textStyle
	styles.Focused.Prompt = accentStyle
	styles.Focused.Placeholder = mutedStyle
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.EndOfBuffer = lipgloss.NewStyle().Foreground(panel)
	styles.Blurred = styles.Focused
	return styles
}
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, ansi.Strip(text))
}
func clipped(text string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(clean(text), "\n", " "), max(1, width), "…")
}
func (m *model) resize(width, height int) {
	m.width, m.height = max(1, width), max(1, height)
	m.bodyWidth = max(1, m.width-4)
	if m.width >= 108 {
		m.bodyWidth -= 28
	}
	m.viewport.SetWidth(max(1, m.bodyWidth-2))
	m.viewport.SetHeight(max(1, m.height-12))
	m.input.SetWidth(max(1, m.bodyWidth-4))
	m.renderCache = make(map[string]string)
	m.refresh()
}
func (m *model) refresh() {
	atBottom := m.viewport.AtBottom()
	var blocks []string
	for _, turn := range m.turns {
		key := fmt.Sprintf("%d:%d:%t", turn.ID, m.bodyWidth, m.details)
		block, cached := m.renderCache[key]
		if !cached {
			block = m.renderTurn(turn)
			if turn.Status != "running" && turn.Status != "queued" {
				m.renderCache[key] = block
			}
		}
		blocks = append(blocks, block)
	}
	m.viewport.SetContent(strings.Join(blocks, "\n\n"))
	if atBottom {
		m.viewport.GotoBottom()
	}
	m.dirty = false
}
func (m *model) markdown(text string) string {
	theme := styles.DarkStyleConfig
	textColor, accentColor, codeColor, panelColor := "#DFE8EF", "#83E2C4", "#E9C58E", "#17212B"
	margin, bold := uint(1), true
	theme.Document.Color, theme.Document.Margin = &textColor, &margin
	theme.Heading.Color, theme.Heading.Bold = &accentColor, &bold
	for _, heading := range []*glamouransi.StyleBlock{&theme.H1, &theme.H2, &theme.H3, &theme.H4, &theme.H5, &theme.H6} {
		heading.Prefix, heading.Suffix, heading.BackgroundColor = "", "", nil
		heading.Color, heading.Bold = &accentColor, &bold
	}
	theme.Code.Color, theme.Code.BackgroundColor = &codeColor, &panelColor
	theme.CodeBlock.Theme, theme.CodeBlock.Chroma = "catppuccin-mocha", nil
	renderer, err := glamour.NewTermRenderer(glamour.WithStyles(theme), glamour.WithWordWrap(max(8, m.bodyWidth-6)), glamour.WithTableWrap(true))
	if err == nil {
		if result, renderErr := renderer.Render(clean(text)); renderErr == nil {
			return strings.TrimSpace(result)
		}
	}
	return textStyle.Width(max(1, m.bodyWidth-4)).Render(clean(text))
}
func (m *model) renderTurn(turn server.Turn) string {
	width := max(1, m.bodyWidth-4)
	userLabel := lipgloss.NewStyle().Foreground(gold).Bold(true).Render("YOU") + mutedStyle.Render("  "+turn.CreatedAt.Local().Format("15:04"))
	user := textStyle.Width(width).Render(clean(turn.Input))
	assistantLabel := accentStyle.Bold(true).Render("SABER")
	assistantLabel += mutedStyle.Render("  " + clipped(turn.Model, max(10, width-10)))
	var parts []string
	parts = append(parts, " "+userLabel, " "+user, "", " "+assistantLabel)
	if len(turn.Tools) > 0 {
		for _, tool := range turn.Tools {
			marker, color := "✓", accent
			state := "完成"
			if tool.ErrorCode != "" {
				marker, color, state = "!", danger, "失败"
			} else if tool.Duration == 0 && tool.Content == "" {
				marker, color, state = "○", muted, "已派发"
				if turn.Status == "running" {
					marker, color, state = m.spinner.View(), accent, "执行中"
				}
			}
			line := lipgloss.NewStyle().Foreground(color).Render(marker+" "+clipped(tool.Call.Function.Name, width-16)) + mutedStyle.Render("  "+state)
			parts = append(parts, " "+line)
			if m.details {
				detail := clean(tool.Call.Function.Arguments)
				if tool.Content != "" {
					detail += "\n" + clean(tool.Content)
				}
				runes := []rune(detail)
				if len(runes) > 4000 {
					detail = string(runes[:4000]) + "\n…"
				}
				parts = append(parts, lipgloss.NewStyle().Foreground(muted).BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(faint).PaddingLeft(1).Width(width-2).Render(detail))
			}
		}
		parts = append(parts, "")
	}
	content := turn.Content
	if content == "" {
		content = m.live[turn.ID]
	}
	if content != "" {
		parts = append(parts, m.markdown(content))
	}
	switch turn.Status {
	case "queued":
		parts = append(parts, " "+mutedStyle.Render("○ 已加入队列"))
	case "running":
		stage := m.stage
		if stage == "" {
			stage = "正在思考"
		}
		parts = append(parts, " "+m.spinner.View()+" "+mutedStyle.Render(stage))
	case "completed":
		meta := fmt.Sprintf("%.1fs", turn.Duration.Seconds())
		if turn.Tokens > 0 {
			meta += fmt.Sprintf("  ·  %s tokens", compactNumber(turn.Tokens))
		}
		parts = append(parts, " "+mutedStyle.Render(meta))
	default:
		label := map[string]string{"failed": "回答未完成", "cancelled": "已停止", "interrupted": "服务重启前的任务已中断"}[turn.Status]
		if label == "" {
			label = turn.Status
		}
		parts = append(parts, " "+lipgloss.NewStyle().Foreground(danger).Width(width).Render(label+"  "+clean(turn.Error)))
	}
	return strings.Join(parts, "\n")
}
func compactNumber(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}
func (m *model) effectiveEffort() string {
	if m.effort != "" {
		return m.effort
	}
	for _, item := range m.info.Models {
		if item.ID == m.selectedModel && item.ReasoningEffort != "" {
			return item.ReasoningEffort
		}
	}
	return "默认"
}
func (m *model) header(width int) string {
	logo := lipgloss.NewStyle().Foreground(background).Background(accent).Bold(true).Padding(0, 1).Render("◈ SABER")
	label := mutedStyle.Render("  /  CHAT")
	status, color := "● 已连接", accent
	if !m.connected {
		status, color = "○ 未连接", muted
	}
	if m.loading {
		status = "◌ 连接中"
	}
	right := lipgloss.NewStyle().Foreground(color).Render(status)
	gap := max(1, width-lipgloss.Width(logo+label)-lipgloss.Width(right))
	first := logo + label + strings.Repeat(" ", gap) + right
	modelName := m.selectedModel
	if modelName == "" {
		modelName = "等待模型配置"
	}
	metadata := clipped(modelName, max(8, width-24)) + "  ·  思考 " + m.effectiveEffort()
	second := mutedStyle.Render(clipped(metadata, width))
	return first + "\n" + second + "\n"
}
func (m *model) sidebar(height int) string {
	var lines []string
	lines = append(lines, mutedStyle.Bold(true).Render("会话"), "", accentStyle.Render("＋ 新建会话   ⌃N"), "")
	count := min(len(m.sessions), max(0, (height-8)/3))
	if count == 0 {
		lines = append(lines, mutedStyle.Render("还没有历史会话"))
	}
	for _, session := range m.sessions[:count] {
		label := clipped(session.Input, 20)
		style := mutedStyle
		if session.Session == m.session {
			style = lipgloss.NewStyle().Foreground(accent).Background(panel)
		}
		lines = append(lines, style.Width(22).Render(" "+label))
		state := session.CreatedAt.Local().Format("01/02 15:04")
		if session.Status == "running" {
			state = "● 正在执行"
		}
		lines = append(lines, mutedStyle.Render(" "+state), "")
	}
	text := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(24).Height(height).BorderRight(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(faint).PaddingRight(1).Render(text)
}
func (m *model) welcome() string {
	width, height := m.viewport.Width(), m.viewport.Height()
	title := accentStyle.Bold(true).Render("◈  S A B E R")
	subtitle := textStyle.Render("把想法变成下一步。")
	hints := mutedStyle.Render("解释代码   /   一起设计   /   整理思路")
	foot := mutedStyle.Render("输入消息开始，或按 Ctrl+O 继续上次的会话")
	content := lipgloss.JoinVertical(lipgloss.Center, title, "", subtitle, "", hints, "", foot)
	if width < 58 {
		content = lipgloss.JoinVertical(lipgloss.Center, title, "", subtitle, "", mutedStyle.Render("输入消息开始对话"))
	}
	if height < 7 {
		content = title + "\n" + subtitle
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content))
}
func (m *model) View() tea.View {
	if m.width < 36 || m.height < 14 {
		v := tea.NewView(lipgloss.NewStyle().Width(m.width).MaxHeight(m.height).Render("Saber · 请扩大窗口至 36×14\nCtrl+Q 退出"))
		v.AltScreen = true
		return v
	}
	width := m.width - 4
	transcript := m.viewport.View()
	if len(m.turns) == 0 && !m.loading {
		transcript = m.welcome()
	}
	if m.menu != "" {
		transcript = m.menuView()
	}
	composer := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Background(panel).Padding(0, 1).Width(m.bodyWidth - 2).Render(m.input.View())
	right := lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Width(m.bodyWidth).Height(m.viewport.Height()).Render(transcript), composer)
	body := right
	if m.width >= 108 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebar(m.viewport.Height()+5), "  ", right)
	}
	notice := ""
	if m.notice != "" {
		notice = clipped(m.notice, width)
	} else if m.active() {
		notice = "任务在服务端运行 · 退出界面后仍会继续"
	}
	keys := "↵ 发送  ⌥↵ 换行  ⌃N 新建  ⌃O 会话  ⌃P 模型  ⌃R 思考  ⌃T 工具  ⌃Q 退出"
	if m.width < 90 {
		keys = "↵ 发送  ⌥↵ 换行  / 命令  ⌃C 停止  ⌃Q 退出"
	}
	footer := mutedStyle.Render(clipped(notice, width)) + "\n" + mutedStyle.Render(clipped(keys, width))
	content := lipgloss.JoinVertical(lipgloss.Left, m.header(width), body, footer)
	frame := lipgloss.NewStyle().Foreground(foreground).Background(background).Padding(1, 2).Width(m.width).Height(m.height).MaxWidth(m.width).MaxHeight(m.height).Render(content)
	v := tea.NewView(frame)
	v.AltScreen = true
	v.BackgroundColor, v.ForegroundColor = background, foreground
	v.WindowTitle = "Saber · Chat"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
