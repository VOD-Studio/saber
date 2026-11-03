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
		{"exec", "在授权工作区中运行 shell 命令。返回退出码、输出摘要和可用 read_output 回读的 output_id；非零退出后先核查再调整命令。", map[string]any{"command": text("要执行的 shell 命令")}, []string{"command"}},
		{"read_output", "按原始字节游标分页回读工具输出归档。用工具返回的 output_id，不要猜测路径；offset 为 0-based 字节位置，limit 为 4..4096。", map[string]any{"output_id": text("工具返回的输出句柄"), "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 4, "maximum": 4096}}, []string{"output_id"}},
		{"read_file", "分页读取工作区文本文件。offset 为 0-based 行号，limit 为 1..1000（默认 200），单页正文最多 4096 字节；超长行用返回的 next_byte_offset 续读。deliver=true 时改为保存并交付完整文件快照。", map[string]any{"path": text("相对工作区的文件路径"), "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "byte_offset": map[string]any{"type": "integer", "minimum": 0}, "deliver": map[string]any{"type": "boolean"}}, []string{"path"}},
		{"list_files", "列出工作区指定目录的直属文件，不跟随符号链接。", map[string]any{"path": text("相对路径，默认 .")}, nil},
		{"write_file", "创建或覆盖工作区文件。父目录必须已存在；需要交付时随后 read_file(deliver=true)。", map[string]any{"path": text("相对文件路径"), "content": text("完整文件内容")}, []string{"path", "content"}},
		{"apply_patch", "精确修改单个文件。edits 中每项 old 必须只出现一次，全部验证成功才写回；失败后先读文件再调整。", map[string]any{"path": text("相对文件路径"), "edits": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "properties": map[string]any{"old": text("唯一匹配的原文本"), "new": text("替换文本")}, "required": []string{"old", "new"}, "additionalProperties": false}}}, []string{"path", "edits"}},
		{"search_files", "按正则搜索工作区文件内容。默认忽略隐藏项和 .git；返回 path、1-based line 与命中文本，并用 next_offset 分页。二进制、超大文件和不可读项计入 skipped。", map[string]any{"pattern": text("Go RE2 正则，最多 512 字节"), "path": text("相对目录，默认 ."), "file_glob": text("文件 glob，默认 **"), "case_sensitive": map[string]any{"type": "boolean"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "include_hidden": map[string]any{"type": "boolean"}}, []string{"pattern"}},
		{"glob_files", "按路径 glob 查找工作区条目，支持 * ? [] 与匹配零到多个路径段的 **。默认忽略隐藏项和 .git；用 next_offset 分页。", map[string]any{"pattern": text("路径 glob，例如 **/*.go"), "path": text("相对目录，默认 ."), "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "include_hidden": map[string]any{"type": "boolean"}}, []string{"pattern"}},
		{"process", "管理本任务的后台进程：start 启动并返回 id 与 output_id，list/status/wait 查询，logs 按字节分页回读输出，kill 幂等停止。后台进程只能由当前任务访问。", map[string]any{"action": map[string]any{"type": "string", "enum": []string{"start", "list", "status", "wait", "logs", "kill"}}, "command": text("start 时执行的 shell 命令"), "id": text("start 返回的作业 ID"), "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 4, "maximum": 4096}, "wait_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 30}}, []string{"action"}},
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
