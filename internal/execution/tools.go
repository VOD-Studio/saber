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
		{"write_file", "创建或覆盖工作区文本文件；覆盖前须读取完整当前版本。expected_sha256 可额外保护 CAS；诊断和快照独立返回。", map[string]any{"path": text("相对文件路径"), "content": text("完整文件内容"), "expected_sha256": text("当前原始文件字节的 SHA-256，可选")}, []string{"path", "content"}},
		{"apply_patch", "精确修改单个文件。edits 中每项 old 必须只出现一次，全部验证成功才写回；expected_sha256 可拒绝读取后的文件漂移。", map[string]any{"path": text("相对文件路径"), "expected_sha256": text("当前原始文件字节的 SHA-256，可选"), "edits": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "properties": map[string]any{"old": text("唯一匹配的原文本"), "new": text("替换文本")}, "required": []string{"old", "new"}, "additionalProperties": false}}}, []string{"path", "edits"}},
		{"search_files", "按正则搜索工作区文件内容。默认忽略隐藏项和 .git；返回 path、1-based line 与命中文本，并用 next_offset 分页。二进制、超大文件和不可读项计入 skipped。", map[string]any{"pattern": text("Go RE2 正则，最多 512 字节"), "path": text("相对目录，默认 ."), "file_glob": text("文件 glob，默认 **"), "case_sensitive": map[string]any{"type": "boolean"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}, "include_hidden": map[string]any{"type": "boolean"}}, []string{"pattern"}},
		{"glob_files", "按路径 glob 查找工作区条目，支持 * ? [] 与匹配零到多个路径段的 **。默认忽略隐藏项和 .git；用 next_offset 分页。", map[string]any{"pattern": text("路径 glob，例如 **/*.go"), "path": text("相对目录，默认 ."), "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}, "include_hidden": map[string]any{"type": "boolean"}}, []string{"pattern"}},
		{"process", "管理当前任务的真实后台进程：start、list/status/wait、logs、kill；write/close_stdin 控制 stdin，PTY 支持 resize，signal 发命名信号。notify、watch_patterns、heartbeat_seconds 产生自动任务通知，也可 notifications 消费。重启不重放旧命令。", processToolProperties(), []string{"action"}},
		{"snapshot", "查看或回滚本任务的文件编辑快照。write_file/apply_patch 修改前自动保存原内容；list 只返回摘要，rollback 用 snapshot_id 或 path 恢复，覆盖前会再保存当前内容。", map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "rollback"}}, "snapshot_id": text("list 返回的快照 ID"), "path": text("工作区相对路径，取该路径最近的快照")}, []string{"action"}},
	}
	var tools []openai.Tool
	for _, d := range definitions {
		// snapshot 不单独授权，只随已有写入能力暴露；实际调用仍由 Check 复核。
		if d.name == "snapshot" {
			if !e.snapshotAllowed(ctx) {
				continue
			}
		} else if e.Check(ctx, d.name) != nil {
			continue
		}
		if d.required == nil {
			d.required = []string{}
		}
		tools = append(tools, openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: d.name, Description: d.description, Parameters: map[string]any{"type": "object", "properties": d.properties, "required": d.required, "additionalProperties": false}}})
	}
	return tools
}
