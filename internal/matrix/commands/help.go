// Package commands 提供 Matrix 机器人的命令处理实现。
package commands

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"
)

// CommandLister 定义列出命令的接口。
type CommandLister interface {
	// List 返回所有已注册的命令。
	List() []CommandInfo
}

// HelpCommand 列出可用命令。
type HelpCommand struct {
	sender  Sender
	lister  CommandLister
}

// NewHelpCommand 创建一个新的帮助命令处理器。
func NewHelpCommand(sender Sender, lister CommandLister) *HelpCommand {
	return &HelpCommand{
		sender: sender,
		lister: lister,
	}
}

// SanitizeHTML 净化 HTML 内容。
// 这是一个简单的实现，实际使用时应该使用更完整的 HTML 净化。
func SanitizeHTML(html string) string {
	// 基本的 HTML 实体转义
	html = strings.ReplaceAll(html, "&", "&amp;")
	html = strings.ReplaceAll(html, "<", "&lt;")
	html = strings.ReplaceAll(html, ">", "&gt;")
	html = strings.ReplaceAll(html, "\"", "&quot;")
	return html
}

// Handle 实现 CommandHandler，生成 HTML 表格格式的帮助信息。
func (c *HelpCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	commands := c.lister.List()

	if len(commands) == 0 {
		// 无可用命令时返回纯文本帮助
		plain := "暂无可用命令"
		return c.sender.SendFormattedText(ctx, roomID, plain, plain)
	}

	var htmlBuilder strings.Builder
	htmlBuilder.WriteString("<table>")
	htmlBuilder.WriteString("<thead><tr><th>命令</th><th>描述</th></tr></thead>")
	htmlBuilder.WriteString("<tbody>")
	for _, cmd := range commands {
		desc := cmd.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(&htmlBuilder, "<tr><td><code>!%s</code></td><td>%s</td></tr>",
			SanitizeHTML(cmd.Name), SanitizeHTML(desc))
	}
	htmlBuilder.WriteString("</tbody></table>")

	var plainBuilder strings.Builder
	plainBuilder.WriteString("可用命令：\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(&plainBuilder, "  !%s", cmd.Name)
		if cmd.Description != "" {
			fmt.Fprintf(&plainBuilder, " - %s", cmd.Description)
		}
		plainBuilder.WriteString("\n")
	}

	return c.sender.SendFormattedText(ctx, roomID, htmlBuilder.String(), plainBuilder.String())
}
