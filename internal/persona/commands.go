// Package persona 提供机器人人格管理功能。
package persona

import (
	"context"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"
)

// Sender 定义发送消息的接口。
type Sender interface {
	// SendFormattedText 向房间发送格式化消息（支持 HTML）。
	SendFormattedText(ctx context.Context, roomID id.RoomID, html, plain string) error
}

// PersonaService 定义人格服务接口。
type PersonaService interface {
	// List 返回所有可用人格。
	List() []*Persona
	// Get 根据 ID 获取人格。
	Get(id string) *Persona
	// Create 创建新的自定义人格。
	Create(id, name, prompt, description string) error
	// Delete 删除人格（内置人格不可删除）。
	Delete(id string) error
	// GetRoomPersona 获取房间的人格。
	GetRoomPersona(roomID id.RoomID) *Persona
	// SetRoomPersona 设置房间的人格。
	SetRoomPersona(ctx context.Context, roomID id.RoomID, personaID string) error
	// ClearRoomPersona 清除房间的人格设置。
	ClearRoomPersona(ctx context.Context, roomID id.RoomID) error
}

// PersonaCommand 处理人格管理命令。
// 支持的子命令：
//   - list: 列出所有人格
//   - set <id>: 设置当前房间的人格
//   - clear: 清除当前房间的人格
//   - new <id> "<name>" "<prompt>" "<desc>": 创建新人格
//   - del <id>: 删除人格
type PersonaCommand struct {
	sender  Sender
	service PersonaService
}

// NewPersonaCommand 创建新的人格命令处理器。
func NewPersonaCommand(sender Sender, service PersonaService) *PersonaCommand {
	return &PersonaCommand{
		sender:  sender,
		service: service,
	}
}

// Handle 实现 CommandHandler 接口，处理人格命令。
func (c *PersonaCommand) Handle(ctx context.Context, userID id.UserID, roomID id.RoomID, args []string) error {
	if len(args) == 0 {
		return c.showHelp(ctx, roomID)
	}

	subCmd := strings.ToLower(args[0])
	subArgs := args[1:]

	switch subCmd {
	case "list", "ls":
		return c.handleList(ctx, roomID)
	case "set":
		return c.handleSet(ctx, roomID, subArgs)
	case "clear", "reset":
		return c.handleClear(ctx, roomID)
	case "new", "create":
		return c.handleNew(ctx, roomID, subArgs)
	case "del", "delete", "rm":
		return c.handleDelete(ctx, roomID, subArgs)
	case "help", "?":
		return c.showHelp(ctx, roomID)
	case "status", "show":
		return c.handleStatus(ctx, roomID)
	default:
		return c.sender.SendFormattedText(ctx, roomID,
			"<p>未知子命令: <code>"+escapeHTML(subCmd)+"</code></p>",
			"未知子命令: "+subCmd+"\n使用 !persona help 查看帮助")
	}
}

// showHelp 显示帮助信息。
func (c *PersonaCommand) showHelp(ctx context.Context, roomID id.RoomID) error {
	help := `<h3>🎭 人格管理命令</h3>
<table>
<thead><tr><th>命令</th><th>描述</th></tr></thead>
<tbody>
<tr><td><code>!persona list</code></td><td>列出所有可用人格</td></tr>
<tr><td><code>!persona set &lt;id&gt;</code></td><td>设置当前房间的人格</td></tr>
<tr><td><code>!persona clear</code></td><td>清除当前房间的人格设置</td></tr>
<tr><td><code>!persona status</code></td><td>显示当前房间的人格状态</td></tr>
<tr><td><code>!persona new &lt;id&gt; "&lt;name&gt;" "&lt;prompt&gt;" "&lt;desc&gt;"</code></td><td>创建新人格</td></tr>
<tr><td><code>!persona del &lt;id&gt;</code></td><td>删除自定义人格</td></tr>
</tbody>
</table>
<p>💡 提示: 内置人格不可删除</p>`

	plainHelp := `🎭 人格管理命令

!persona list - 列出所有可用人格
!persona set <id> - 设置当前房间的人格
!persona clear - 清除当前房间的人格设置
!persona status - 显示当前房间的人格状态
!persona new <id> "<name>" "<prompt>" "<desc>" - 创建新人格
!persona del <id> - 删除自定义人格

💡 提示: 内置人格不可删除`

	return c.sender.SendFormattedText(ctx, roomID, help, plainHelp)
}

// handleList 列出所有人格。
func (c *PersonaCommand) handleList(ctx context.Context, roomID id.RoomID) error {
	personas := c.service.List()
	if len(personas) == 0 {
		return c.sender.SendFormattedText(ctx, roomID,
			"<p>暂无可用人格</p>",
			"暂无可用人格")
	}

	var htmlBuilder strings.Builder
	htmlBuilder.WriteString(`<h3>🎭 可用人格列表</h3><table>`)
	htmlBuilder.WriteString("<thead><tr><th>ID</th><th>名称</th><th>描述</th><th>类型</th></tr></thead><tbody>")

	var plainBuilder strings.Builder
	plainBuilder.WriteString("🎭 可用人格列表\n\n")

	currentPersona := c.service.GetRoomPersona(roomID)

	for _, p := range personas {
		typeLabel := "自定义"
		if p.IsBuiltin {
			typeLabel = "内置"
		}

		// 标记当前激活的人格
		marker := ""
		if currentPersona != nil && currentPersona.ID == p.ID {
			marker = " ✓ [当前]"
			htmlBuilder.WriteString(`<tr style="background-color: #e8f5e9;">`)
		} else {
			htmlBuilder.WriteString("<tr>")
		}

		fmt.Fprintf(&htmlBuilder, "<td><code>%s</code></td><td>%s</td><td>%s</td><td>%s%s</td></tr>",
			escapeHTML(p.ID), escapeHTML(p.Name), escapeHTML(p.Description), escapeHTML(typeLabel), escapeHTML(marker))

		fmt.Fprintf(&plainBuilder, "  %s (%s) - %s [%s]%s\n",
			p.ID, p.Name, p.Description, typeLabel, marker)
	}

	htmlBuilder.WriteString("</tbody></table>")
	if currentPersona != nil {
		htmlBuilder.WriteString("<p>✓ 表示当前房间激活的人格</p>")
	}

	return c.sender.SendFormattedText(ctx, roomID, htmlBuilder.String(), plainBuilder.String())
}

// handleSet 设置房间人格。
func (c *PersonaCommand) handleSet(ctx context.Context, roomID id.RoomID, args []string) error {
	if len(args) == 0 {
		return c.sender.SendFormattedText(ctx, roomID,
			"<p>用法: <code>!persona set &lt;id&gt;</code></p>",
			"用法: !persona set <id>")
	}

	personaID := strings.ToLower(args[0])
	persona := c.service.Get(personaID)
	if persona == nil {
		return c.sender.SendFormattedText(ctx, roomID,
			fmt.Sprintf("<p>人格 <code>%s</code> 不存在</p>", escapeHTML(personaID)),
			fmt.Sprintf("人格 %s 不存在", personaID))
	}

	err := c.service.SetRoomPersona(ctx, roomID, personaID)
	if err != nil {
		return c.sender.SendFormattedText(ctx, roomID,
			fmt.Sprintf("<p>设置人格失败: %s</p>", escapeHTML(err.Error())),
			fmt.Sprintf("设置人格失败: %s", err.Error()))
	}

	return c.sender.SendFormattedText(ctx, roomID,
		fmt.Sprintf("<p>✅ 已将当前房间的人格设置为 <strong>%s</strong> (%s)</p>",
			escapeHTML(persona.Name), escapeHTML(personaID)),
		fmt.Sprintf("✅ 已将当前房间的人格设置为 %s (%s)", persona.Name, personaID))
}

// handleClear 清除房间人格。
func (c *PersonaCommand) handleClear(ctx context.Context, roomID id.RoomID) error {
	err := c.service.ClearRoomPersona(ctx, roomID)
	if err != nil {
		return c.sender.SendFormattedText(ctx, roomID,
			fmt.Sprintf("<p>清除人格失败: %s</p>", escapeHTML(err.Error())),
			fmt.Sprintf("清除人格失败: %s", err.Error()))
	}

	return c.sender.SendFormattedText(ctx, roomID,
		"<p>✅ 已清除当前房间的人格设置</p>",
		"✅ 已清除当前房间的人格设置")
}

// handleStatus 显示当前房间的人格状态。
func (c *PersonaCommand) handleStatus(ctx context.Context, roomID id.RoomID) error {
	persona := c.service.GetRoomPersona(roomID)
	if persona == nil {
		return c.sender.SendFormattedText(ctx, roomID,
			"<p>当前房间未设置人格（使用默认行为）</p>",
			"当前房间未设置人格（使用默认行为）")
	}

	typeLabel := "自定义"
	if persona.IsBuiltin {
		typeLabel = "内置"
	}

	html := fmt.Sprintf(`<h3>🎭 当前房间人格</h3>
<table>
<tr><td><strong>ID</strong></td><td><code>%s</code></td></tr>
<tr><td><strong>名称</strong></td><td>%s</td></tr>
<tr><td><strong>类型</strong></td><td>%s</td></tr>
<tr><td><strong>描述</strong></td><td>%s</td></tr>
</table>`,
		escapeHTML(persona.ID), escapeHTML(persona.Name), escapeHTML(typeLabel), escapeHTML(persona.Description))

	plain := fmt.Sprintf("🎭 当前房间人格\n\nID: %s\n名称: %s\n类型: %s\n描述: %s",
		persona.ID, persona.Name, typeLabel, persona.Description)

	return c.sender.SendFormattedText(ctx, roomID, html, plain)
}

// handleNew 创建新人格。
func (c *PersonaCommand) handleNew(ctx context.Context, roomID id.RoomID, args []string) error {
	// 参数: new <id> "<name>" "<prompt>" "<desc>"
	if len(args) < 4 {
		return c.sender.SendFormattedText(ctx, roomID,
			`<p>用法: <code>!persona new &lt;id&gt; "&lt;name&gt;" "&lt;prompt&gt;" "&lt;desc&gt;"</code></p>
<p>示例: <code>!persona new robot "机器人" "你是一个友好的机器人" "机器人人格"</code></p>`,
			`用法: !persona new <id> "<name>" "<prompt>" "<desc>"
示例: !persona new robot "机器人" "你是一个友好的机器人" "机器人人格"`)
	}

	personaID := strings.ToLower(args[0])
	name := args[1]
	prompt := args[2]
	description := args[3]

	err := c.service.Create(personaID, name, prompt, description)
	if err != nil {
		return c.sender.SendFormattedText(ctx, roomID,
			fmt.Sprintf("<p>创建人格失败: %s</p>", escapeHTML(err.Error())),
			fmt.Sprintf("创建人格失败: %s", err.Error()))
	}

	return c.sender.SendFormattedText(ctx, roomID,
		fmt.Sprintf("<p>✅ 已创建人格 <strong>%s</strong> (%s)</p>",
			escapeHTML(name), escapeHTML(personaID)),
		fmt.Sprintf("✅ 已创建人格 %s (%s)", name, personaID))
}

// handleDelete 删除人格。
func (c *PersonaCommand) handleDelete(ctx context.Context, roomID id.RoomID, args []string) error {
	if len(args) == 0 {
		return c.sender.SendFormattedText(ctx, roomID,
			"<p>用法: <code>!persona del &lt;id&gt;</code></p>",
			"用法: !persona del <id>")
	}

	personaID := strings.ToLower(args[0])
	persona := c.service.Get(personaID)
	if persona == nil {
		return c.sender.SendFormattedText(ctx, roomID,
			fmt.Sprintf("<p>人格 <code>%s</code> 不存在</p>", escapeHTML(personaID)),
			fmt.Sprintf("人格 %s 不存在", personaID))
	}

	err := c.service.Delete(personaID)
	if err != nil {
		return c.sender.SendFormattedText(ctx, roomID,
			fmt.Sprintf("<p>删除人格失败: %s</p>", escapeHTML(err.Error())),
			fmt.Sprintf("删除人格失败: %s", err.Error()))
	}

	return c.sender.SendFormattedText(ctx, roomID,
		fmt.Sprintf("<p>✅ 已删除人格 <strong>%s</strong> (%s)</p>",
			escapeHTML(persona.Name), escapeHTML(personaID)),
		fmt.Sprintf("✅ 已删除人格 %s (%s)", persona.Name, personaID))
}

// escapeHTML 转义 HTML 特殊字符。
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&#39;")
	return s
}