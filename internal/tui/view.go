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
	m.layout()
	m.refresh()
}
func (m *model) showSidebar() bool { return m.width >= 108 && !m.sidebarHidden }

func (m *model) layout() {
	atBottom, offset := m.viewport.AtBottom(), m.viewport.YOffset()
	available := max(1, m.width-4)
	if m.showSidebar() {
		available -= 28
	}
	width := min(108, max(1, available))
	widthChanged := m.bodyWidth != width
	maxInputHeight := min(6, max(1, m.height-12))
	if widthChanged || m.input.MaxHeight != maxInputHeight {
		m.bodyWidth = width
		m.input.MaxHeight = maxInputHeight
		m.input.SetWidth(max(1, width-4))
		m.viewport.SetWidth(width)
	}
	height := max(1, m.height-2-lipgloss.Height(m.header(max(1, m.width-4)))-lipgloss.Height(m.composer()))
	heightChanged := m.viewport.Height() != height
	if heightChanged {
		m.viewport.SetHeight(height)
	}
	if widthChanged {
		m.renderCache = make(map[string]string)
		m.refresh()
	}
	if widthChanged || heightChanged {
		if atBottom {
			m.viewport.GotoBottom()
		} else {
			m.viewport.SetYOffset(offset)
		}
	}
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
	margin, bold := uint(0), true
	theme.Document.Color, theme.Document.Margin = &textColor, &margin
	theme.Heading.Color, theme.Heading.Bold = &accentColor, &bold
	for _, heading := range []*glamouransi.StyleBlock{&theme.H1, &theme.H2, &theme.H3, &theme.H4, &theme.H5, &theme.H6} {
		heading.Prefix, heading.Suffix, heading.BackgroundColor = "", "", nil
		heading.Color, heading.Bold = &accentColor, &bold
	}
	theme.Code.Color, theme.Code.BackgroundColor = &codeColor, &panelColor
	theme.CodeBlock.Theme, theme.CodeBlock.Chroma = "catppuccin-mocha", nil
	renderer, err := glamour.NewTermRenderer(glamour.WithStyles(theme), glamour.WithWordWrap(max(8, m.bodyWidth-4)), glamour.WithTableWrap(true))
	if err == nil {
		if result, renderErr := renderer.Render(clean(text)); renderErr == nil {
			return strings.Trim(result, "\n")
		}
	}
	return textStyle.Width(max(1, m.bodyWidth-4)).Render(clean(text))
}
func (m *model) renderTurn(turn server.Turn) string {
	width := max(1, m.bodyWidth-4)
	userLabel := textStyle.Bold(true).Render("你") + mutedStyle.Render("  "+turn.CreatedAt.Local().Format("15:04"))
	user := lipgloss.NewStyle().Foreground(foreground).BorderLeft(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(faint).PaddingLeft(1).Width(width).Render(clean(turn.Input))
	assistantLabel := accentStyle.Bold(true).Render("◈ Saber")
	var parts []string
	parts = append(parts, userLabel, user, "", assistantLabel)
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
			parts = append(parts, line)
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
		parts = append(parts, mutedStyle.Render("○ 已加入队列"))
	case "running":
		stage := m.stage
		if stage == "" {
			stage = "正在思考"
		}
		parts = append(parts, m.spinner.View()+" "+mutedStyle.Render(stage))
	case "completed":
		meta := fmt.Sprintf("%.1fs", turn.Duration.Seconds())
		if turn.Tokens > 0 {
			meta += fmt.Sprintf("  ·  %s tokens", compactNumber(turn.Tokens))
		}
		if turn.Model != "" {
			meta = clipped(m.modelName(turn.Model), max(8, width-lipgloss.Width(meta)-5)) + "  ·  " + meta
		}
		parts = append(parts, mutedStyle.Render(clipped(meta, width)))
	default:
		label := map[string]string{"failed": "回答未完成", "cancelled": "已停止", "interrupted": "服务重启前的任务已中断"}[turn.Status]
		if label == "" {
			label = turn.Status
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(danger).Width(width).Render(label+"  "+clean(turn.Error)))
	}
	return lipgloss.NewStyle().Padding(0, 2).Width(m.bodyWidth).Render(strings.Join(parts, "\n"))
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
func (m *model) modelName(id string) string {
	for _, item := range m.info.Models {
		if item.ID == id && item.Name != "" {
			return item.Name
		}
	}
	return id
}
func (m *model) header(width int) string {
	logo := accentStyle.Bold(true).Render("◈ SABER")
	title := "新会话"
	if len(m.turns) > 0 {
		title = m.turns[0].Input
	}
	status, color := "● 已连接", accent
	if !m.connected {
		status, color = "○ 未连接", muted
	}
	if m.loading {
		status = "◌ 连接中"
	}
	right := lipgloss.NewStyle().Foreground(color).Render(status)
	label := mutedStyle.Render("  /  " + clipped(title, max(1, width-lipgloss.Width(logo+right)-7)))
	gap := max(1, width-lipgloss.Width(logo+label)-lipgloss.Width(right))
	first := logo + label + strings.Repeat(" ", gap) + right
	return first + "\n" + lipgloss.NewStyle().Foreground(faint).Render(strings.Repeat("─", width)) + "\n"
}
func (m *model) sidebar(height int) string {
	var lines []string
	lines = append(lines, mutedStyle.Bold(true).Render("会话"), "", mutedStyle.Render("＋ 新建        Ctrl+N"), "")
	count := min(len(m.sessions), max(0, height-6))
	if count == 0 {
		lines = append(lines, mutedStyle.Render("还没有历史会话"))
	}
	for _, session := range m.sessions[:count] {
		label := clipped(session.Input, 14)
		style := mutedStyle
		prefix := " "
		if session.Session == m.session {
			style = lipgloss.NewStyle().Foreground(accent).Background(panel)
			prefix = "▌"
		}
		state := session.CreatedAt.Local().Format("15:04")
		if session.Status == "running" {
			state = "●"
		}
		gap := strings.Repeat(" ", max(1, 21-lipgloss.Width(label+state)))
		lines = append(lines, style.Width(22).Render(prefix+label+gap+state))
	}
	text := strings.Join(lines, "\n")
	return lipgloss.NewStyle().Width(26).Height(height).BorderRight(true).BorderStyle(lipgloss.NormalBorder()).BorderForeground(faint).PaddingRight(1).Render(text)
}
func (m *model) composer() string {
	border := accent
	if m.menu != "" {
		border = faint
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).BorderBackground(background).Background(panel).Padding(0, 1).Width(m.bodyWidth).Render(m.input.View())
	name := m.modelName(m.selectedModel)
	if name == "" {
		name = "等待模型配置"
	}
	width := max(1, m.bodyWidth-4)
	effort := " · 思考 " + clean(m.effectiveEffort())
	metadata := clipped(name, max(1, width-lipgloss.Width(effort))) + effort
	keys := "Enter 发送 · Alt+Enter 换行 · / 命令"
	if m.active() {
		keys = "Ctrl+C 停止 · Ctrl+Q 离开 · / 命令"
	} else if m.menu != "" {
		keys = "↑↓ 选择 · ↵ 确定 · Esc 返回"
	} else if width < 42 {
		keys = "↵ 发送 · ⌥↵ 换行 · / 命令"
	}
	notice := m.notice
	if notice == "" && m.active() {
		notice = "离开界面后，任务继续运行"
	}
	info := []string{metadata, keys, notice}
	for i := range info {
		info[i] = mutedStyle.Render("  " + clipped(info[i], width))
	}
	return box + "\n" + strings.Join(info, "\n")
}

// paint 补齐嵌套 ANSI 样式重置后缺失的底色，保留面板和代码块的独立背景。
func paint(content string, width, height int) string {
	canvas := lipgloss.NewCanvas(width, height).Compose(lipgloss.NewLayer(content))
	for y := range height {
		for x := range width {
			cell := canvas.CellAt(x, y)
			if cell != nil && cell.Width > 0 && cell.Style.Bg == nil {
				cell.Style.Bg = background
			}
		}
	}
	return canvas.Render()
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
	right := lipgloss.JoinVertical(lipgloss.Left, lipgloss.NewStyle().Width(m.bodyWidth).Height(m.viewport.Height()).Render(transcript), m.composer())
	available := width
	if m.showSidebar() {
		available -= 28
	}
	right = lipgloss.PlaceHorizontal(available, lipgloss.Center, right)
	body := right
	if m.showSidebar() {
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.sidebar(lipgloss.Height(right)), "  ", right)
	}
	content := lipgloss.JoinVertical(lipgloss.Left, m.header(width), body)
	frame := lipgloss.NewStyle().Foreground(foreground).Background(background).Padding(1, 2).Width(m.width).Height(m.height).MaxWidth(m.width).MaxHeight(m.height).Render(content)
	v := tea.NewView(paint(frame, m.width, m.height))
	v.AltScreen = true
	v.BackgroundColor, v.ForegroundColor = background, foreground
	v.WindowTitle = "Saber · Chat"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
