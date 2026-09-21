package execution

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

// Tools 返回当前身份允许使用的工具定义，实际调用仍必须再次通过 Check。
func (e *Executor) Tools(ctx context.Context) []openai.Tool {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	definitions := []struct {
		name, description string
		properties        map[string]any
		required          []string
	}{
		{"exec", "在断网容器的 /workspace 中运行 shell 命令。返回退出码、输出摘要和完整日志位置。非零退出后可调整命令继续操作。", map[string]any{"command": text("要执行的 shell 命令")}, []string{"command"}},
		{"read_file", "读取工作区内文件。deliver=true 时保存不可变快照，任务完成后作为文件发送到原群消息。", map[string]any{"path": text("相对 /workspace 的文件路径"), "deliver": map[string]any{"type": "boolean"}}, []string{"path"}},
		{"list_files", "列出工作区指定目录的直属文件，不跟随符号链接。", map[string]any{"path": text("相对路径，默认 .")}, nil},
		{"write_file", "创建或覆盖工作区文件。父目录必须已存在；需要交付时随后 read_file(deliver=true)。", map[string]any{"path": text("相对文件路径"), "content": text("完整文件内容")}, []string{"path", "content"}},
		{"apply_patch", "精确修改单个文件。edits 中每项 old 必须只出现一次，全部验证成功才写回；失败后先读文件再调整。", map[string]any{"path": text("相对文件路径"), "edits": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "properties": map[string]any{"old": text("唯一匹配的原文本"), "new": text("替换文本")}, "required": []string{"old", "new"}, "additionalProperties": false}}}, []string{"path", "edits"}},
	}
	var tools []openai.Tool
	for _, d := range definitions {
		if e.Check(ctx, d.name) == nil {
			if d.required == nil {
				d.required = []string{}
			}
			tools = append(tools, openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: d.name, Description: d.description, Parameters: map[string]any{"type": "object", "properties": d.properties, "required": d.required, "additionalProperties": false}}})
		}
	}
	return tools
}
