package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
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
	background  = lipgloss.Color("#15131C")
	panel       = lipgloss.Color("#1E1B28")
	raised      = lipgloss.Color("#282333")
	selected    = lipgloss.Color("#352C48")
	foreground  = lipgloss.Color("#EAE5F2")
	muted       = lipgloss.Color("#A49AAF")
	faint       = lipgloss.Color("#453B53")
	accent      = lipgloss.Color("#BC9BFA")
	pink        = lipgloss.Color("#E5A1CE")
	success     = lipgloss.Color("#8BD5BE")
	danger      = lipgloss.Color("#F19BAA")
	accentStyle = lipgloss.NewStyle().Foreground(accent)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	textStyle   = lipgloss.NewStyle().Foreground(foreground)
	quietStyle  = lipgloss.NewStyle().Foreground(faint)
)

func inputStyles() textarea.Styles {
	styles := textarea.DefaultDarkStyles()
	styles.Focused.Base = lipgloss.NewStyle().Foreground(foreground).Background(panel)
	styles.Focused.Text = textStyle.Background(panel)
	styles.Focused.Prompt = accentStyle.Background(panel)
	styles.Focused.Placeholder = mutedStyle.Background(panel)
	styles.Focused.CursorLine = lipgloss.NewStyle().Background(panel)
	styles.Focused.EndOfBuffer = lipgloss.NewStyle().Foreground(panel).Background(panel)
	styles.Blurred = styles.Focused
	styles.Blurred.Prompt = mutedStyle.Background(panel)
	styles.Cursor.Color = accent
	return styles
}

// splitLine 在同一行保留右侧操作，长标题按终端列宽截断。
func splitLine(left, right string, width int) string {
	width = max(1, width)
	if lipgloss.Width(right) >= width {
		return ansi.Truncate(right, width, "…")
	}
	left = ansi.Truncate(left, max(0, width-lipgloss.Width(right)-1), "…")
	return left + strings.Repeat(" ", max(0, width-lipgloss.Width(left+right))) + right
}

func gradient(text string) string {
	colors := lipgloss.Blend1D(len([]rune(text)), accent, pink)
	var result strings.Builder
	for i, r := range []rune(text) {
		result.WriteString(lipgloss.NewStyle().Foreground(colors[i]).Bold(true).Render(string(r)))
	}
	return result.String()
}

func keyHint(key, label string) string {
	return textStyle.Render(key) + mutedStyle.Render(" "+label)
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
	m.filter.SetWidth(max(1, m.menuWidth()-8))
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
		m.unread = false
	}
	m.dirty = false
}
func (m *model) markdown(text string) string {
	theme := styles.DarkStyleConfig
	textColor, accentColor, codeColor, panelColor := "#EAE5F2", "#BC9BFA", "#EBCB8B", "#282333"
	margin, bold := uint(0), true
	theme.Document.Color, theme.Document.Margin = &textColor, &margin
	theme.Heading.Color, theme.Heading.Bold = &accentColor, &bold
	for _, heading := range []*glamouransi.StyleBlock{&theme.H1, &theme.H2, &theme.H3, &theme.H4, &theme.H5, &theme.H6} {
		heading.Prefix, heading.Suffix, heading.BackgroundColor = "", "", nil
		heading.Color, heading.Bold = &accentColor, &bold
	}
	theme.Code.Color, theme.Code.BackgroundColor = &codeColor, &panelColor
	theme.Link.Color, theme.LinkText.Color = &accentColor, &accentColor
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
	userLabel := splitLine(accentStyle.Background(panel).Bold(true).Render("你"), mutedStyle.Background(panel).Render(turn.CreatedAt.Local().Format("15:04")), width-3)
	user := lipgloss.NewStyle().Foreground(foreground).Background(panel).
		BorderLeft(true).BorderStyle(lipgloss.Border{Left: "▎"}).BorderForeground(accent).BorderBackground(panel).
		Padding(0, 1).Width(width).Render(userLabel + "\n" + clean(turn.Input))
	user = paint(user, width, lipgloss.Height(user), panel)
	assistantLabel := lipgloss.NewStyle().Foreground(success).Bold(true).Render("✦ Saber")
	var parts []string
	parts = append(parts, user, "", assistantLabel, "")
	if len(turn.Tools) > 0 {
		var toolLines []string
		for _, tool := range turn.Tools {
			marker, color := "✓", success
			state := "完成"
			if tool.ErrorCode != "" {
				marker, color, state = "!", danger, "失败"
			} else if tool.Duration == 0 && tool.Content == "" {
				marker, color, state = "○", muted, "已派发"
				if turn.Status == "running" {
					marker, color, state = m.spinner.View(), accent, "执行中"
				}
			}
			name := clipped(tool.Call.Function.Name, max(8, width/3))
			summary := clipped(toolSummary(tool.Call.Function.Arguments), max(1, width-lipgloss.Width(name+state)-9))
			line := lipgloss.NewStyle().Foreground(color).Background(panel).Render(marker) + " " + textStyle.Background(panel).Render(name) + mutedStyle.Background(panel).Render("  "+summary)
			toolLines = append(toolLines, splitLine(line, mutedStyle.Background(panel).Render(state), width-2))
			if m.details {
				detail := clean(tool.Call.Function.Arguments)
				if tool.Content != "" {
					detail += "\n" + clean(tool.Content)
				}
				runes := []rune(detail)
				if len(runes) > 4000 {
					detail = string(runes[:4000]) + "\n…"
				}
				toolLines = append(toolLines, mutedStyle.Background(panel).PaddingLeft(2).Width(width-2).Render(detail))
			}
		}
		tools := lipgloss.NewStyle().Background(panel).Padding(0, 1).Width(width).Render(strings.Join(toolLines, "\n"))
		parts = append(parts, paint(tools, width, lipgloss.Height(tools), panel), "")
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
		parts = append(parts, "", mutedStyle.Render(clipped("↳ "+meta, width)))
	default:
		label := map[string]string{"failed": "回答未完成", "cancelled": "已停止", "interrupted": "服务重启前的任务已中断"}[turn.Status]
		if label == "" {
			label = turn.Status
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(danger).Width(width).Render(label+"  "+clean(turn.Error)))
	}
	return lipgloss.NewStyle().Padding(0, 2).Width(m.bodyWidth).Render(strings.Join(parts, "\n"))
}
func toolSummary(arguments string) string {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) == nil {
		for _, key := range []string{"path", "file_path", "command", "query", "url"} {
			if value, ok := args[key].(string); ok && value != "" {
				return value
			}
		}
	}
	return strings.Join(strings.Fields(clean(arguments)), " ")
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
	logo := gradient("SABER")
	title := "新会话"
	if len(m.turns) > 0 {
		title = m.turns[0].Input
	}
	status, color := "● 已连接", success
	if !m.connected {
		status, color = "○ 未连接", muted
	}
	if m.loading {
		status, color = "◌ 连接中", accent
	}
	right := lipgloss.NewStyle().Foreground(color).Render(status)
	label := quietStyle.Render("  /  ") + mutedStyle.Render(clipped(title, max(1, width-lipgloss.Width(logo+right)-7)))
	return splitLine(logo+label, right, width) + "\n\n"
}
func (m *model) sidebar(height int) string {
	const width = 22
	normal, quiet := textStyle.Background(panel), mutedStyle.Background(panel)
	title := splitLine(normal.Bold(true).Render("会话"), quiet.Render(fmt.Sprint(len(m.sessions))), width)
	button := lipgloss.NewStyle().Foreground(accent).Background(raised).Width(width).Render(splitLine("＋ 新建", "Ctrl+N", width))
	lines := []string{title, "", button, ""}
	footer := quiet.Render("Ctrl+O 查看全部") + "\n" + quiet.Render("Ctrl+B 收起侧栏")
	available := max(0, height-2-len(lines)-lipgloss.Height(footer)-1)
	count := min(len(m.sessions), available/3)
	if len(m.sessions) == 0 && available > 0 {
		lines = append(lines, quiet.Render("还没有历史会话"))
	}
	for _, session := range m.sessions[:count] {
		style, meta, marker := normal, quiet, " "
		if session.Session == m.session {
			style = accentStyle.Background(selected).Bold(true)
			meta = mutedStyle.Background(selected)
			marker = "▎"
		}
		state := "已完成"
		switch session.Status {
		case "running":
			state = "回答中"
		case "queued":
			state = "排队中"
		case "failed":
			state = "失败"
		case "cancelled", "interrupted":
			state = "已停止"
		}
		lines = append(lines,
			style.Width(width).Render(marker+" "+clipped(session.Input, width-3)),
			meta.Width(width).Render("  "+session.CreatedAt.Local().Format("01/02 15:04")+" · "+state), "")
	}
	text := lipgloss.NewStyle().Height(max(1, height-2-lipgloss.Height(footer))).Render(strings.Join(lines, "\n")) + "\n" + footer
	view := lipgloss.NewStyle().Background(panel).Width(26).Height(height).MaxHeight(height).Padding(1, 2).Render(text)
	return paint(view, 26, height, panel)
}
func (m *model) composer() string {
	focus := accent
	if m.menu != "" {
		focus = faint
	}
	name := m.modelName(m.selectedModel)
	if name == "" {
		name = "等待模型配置"
	}
	width := max(1, m.bodyWidth-4)
	effort := mutedStyle.Background(panel).Render(" · 思考 " + clean(m.effectiveEffort()))
	keys := keyHint("↵", "发送") + "   " + keyHint("/", "命令")
	if m.menu != "" {
		keys = ""
	} else if m.active() {
		keys = keyHint("Esc", "停止")
	}
	metaWidth := width
	if width >= 64 {
		// 固定保留操作区域，打开选择器时不改变输入区高度。
		metaWidth -= 20
	}
	metadata := accentStyle.Background(panel).Render("◇ "+clipped(name, max(1, metaWidth-lipgloss.Width(effort)-2))) + effort
	metadata = ansi.Truncate(metadata, metaWidth, "…")
	if width >= 64 {
		metadata = splitLine(metadata, keys, width)
	} else {
		metadata += "\n" + lipgloss.PlaceHorizontal(width, lipgloss.Right, keys)
	}
	padding, gap := 1, "\n\n"
	if m.height < 22 {
		padding, gap = 0, "\n"
	}
	box := lipgloss.NewStyle().BorderLeft(true).BorderStyle(lipgloss.Border{Left: "▎"}).
		BorderForeground(focus).BorderBackground(panel).Background(panel).
		Padding(padding, 2, padding, 1).Width(m.bodyWidth).Render(m.input.View() + gap + metadata)
	box = paint(box, m.bodyWidth, lipgloss.Height(box), panel)
	notice := m.notice
	if m.unread && !m.viewport.AtBottom() {
		notice = "↓ 新内容 · Ctrl+End 到底部"
	}
	return box + "\n" + mutedStyle.Render("  "+clipped(notice, width))
}

// paint 补齐嵌套 ANSI 样式重置后缺失的底色，保留面板和代码块的独立背景。
func paint(content string, width, height int, bg color.Color) string {
	canvas := lipgloss.NewCanvas(width, height).Compose(lipgloss.NewLayer(content))
	for y := range height {
		for x := range width {
			cell := canvas.CellAt(x, y)
			if cell != nil && cell.Width > 0 && cell.Style.Bg == nil {
				cell.Style.Bg = bg
			}
		}
	}
	return canvas.Render()
}
func (m *model) welcome() string {
	width, height := m.viewport.Width(), m.viewport.Height()
	title := gradient("S A B E R")
	subtitle := textStyle.Render("从一个想法开始。")
	content := lipgloss.JoinVertical(lipgloss.Center, title, "", subtitle)
	if width >= 54 && height >= 18 {
		logo := []string{
			"█▀▀▀▀  ▄▀▀▀▄  █▀▀▀▄  █▀▀▀▀  █▀▀▀▄",
			"▀▀▀▀█  █▄▄▄█  █▀▀▀▄  █▀▀▀   █▄▄▄▀",
			"▄▄▄▄█  █   █  █▄▄▄▀  █▄▄▄▄  █   █",
		}
		for i := range logo {
			logo[i] = gradient(logo[i])
		}
		art := strings.Join(logo, "\n")
		var shortcuts []string
		for _, item := range [][2]string{{"新建会话", "Ctrl+N"}, {"选择模型", "Ctrl+P"}, {"继续对话", "Ctrl+O"}} {
			shortcuts = append(shortcuts, lipgloss.NewStyle().Background(panel).Padding(1, 2).Width(16).Render(textStyle.Background(panel).Render(item[0])+"\n"+accentStyle.Background(panel).Render(item[1])))
		}
		content = lipgloss.JoinVertical(lipgloss.Center, art, "", subtitle, mutedStyle.Render("写代码、找答案，把想法聊清楚。"), "", lipgloss.JoinHorizontal(lipgloss.Top, shortcuts[0], "  ", shortcuts[1], "  ", shortcuts[2]))
	} else if height >= 9 {
		content = lipgloss.JoinVertical(lipgloss.Center, content, "", mutedStyle.Render("输入消息开始对话"))
	}
	if height < 5 {
		content = title
	}
	return lipgloss.NewStyle().Width(width).Height(height).MaxHeight(height).Render(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content))
}
func (m *model) View() tea.View {
	if m.width < 36 || m.height < 14 {
		v := tea.NewView(lipgloss.NewStyle().Width(m.width).MaxHeight(m.height).Render("Saber · 请扩大窗口至 36×14\nCtrl+C 清空/退出"))
		v.AltScreen = true
		return v
	}
	width := m.width - 4
	transcript := m.viewport.View()
	if len(m.turns) == 0 && !m.loading {
		transcript = m.welcome()
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
	if m.menu != "" {
		dialog := m.menuView()
		x, y := (m.width-lipgloss.Width(dialog))/2, (m.height-lipgloss.Height(dialog))/2
		shadow := lipgloss.NewStyle().Background(lipgloss.Color("#0E0C13")).Width(lipgloss.Width(dialog)).Height(lipgloss.Height(dialog)).Render("")
		frame = lipgloss.NewCompositor(lipgloss.NewLayer(frame), lipgloss.NewLayer(shadow).X(x+1).Y(y+1), lipgloss.NewLayer(dialog).X(x).Y(y)).Render()
	}
	v := tea.NewView(paint(frame, m.width, m.height, background))
	v.AltScreen = true
	v.BackgroundColor, v.ForegroundColor = background, foreground
	v.WindowTitle = "Saber · Chat"
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
