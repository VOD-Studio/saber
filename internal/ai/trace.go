package ai

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// traceMaxBytes 与交付文件上限一致：超出后按完整行截断，否则产物根本无法发送。
const traceMaxBytes = 16 << 20

// traceSensitiveKeyParts 是不允许出现在轨迹产物中的键名片段。
//
// 判断基于键名而非取值，才能在不理解业务字段的情况下也剔除凭据。
var traceSensitiveKeyParts = []string{"apikey", "api_key", "token", "authorization", "secret", "password", "credential", "cookie", "responseshistory"}

// traceTool 让模型把已持久化的执行轨迹导出成私有 JSONL 产物。
//
// 正文与参数默认不导出；时间线、工具名与体积足够复盘"第几轮调了什么、结果多大、为什么停"。
func traceTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_trace",
		Description: "把持久任务的执行轨迹导出为任务私有 JSONL 产物，用于复盘每轮调用、结果体积与终止原因。" +
			"默认只导出事件元数据；include_content=true 会附带对话正文与工具参数原文，仅任务发起人或本群任务管理员可用，" +
			"其中的 api_key/token/authorization 之类键会被剔除。" +
			"task_id 必须在当前任务链上（推荐导出当前任务），不能凭编号读取他人任务；产物写入私有目录并返回 artifact 句柄，不回传正文。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"export"}, "description": "操作类型：export 导出轨迹"},
				"task_id": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"description": "要导出的持久任务编号",
				},
				"format": map[string]any{"type": "string", "enum": []string{"jsonl"}, "description": "导出格式，当前只支持 jsonl"},
				"include_content": map[string]any{
					"type":        "boolean",
					"description": "是否附带对话正文与工具参数原文；敏感键与加密推理历史始终剔除",
				},
			},
			"required":             []string{"action", "task_id"},
			"additionalProperties": false,
		},
	}}
}

// traceExportResult 是导出工具的返回值，刻意只给句柄与统计，不回传正文。
type traceExportResult struct {
	Artifact  string `json:"artifact"`
	Records   int    `json:"records"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Skipped   int    `json:"skipped"`
}

// traceLine 是轨迹产物中固定的一行；字段一旦发布就不能改名。
type traceLine struct {
	Seq        int             `json:"seq"`
	At         string          `json:"at"`
	TaskID     int64           `json:"task_id"`
	BatchID    string          `json:"batch_id"`
	Kind       string          `json:"kind"`
	Round      int             `json:"round"`
	Tool       *traceToolBrief `json:"tool,omitempty"`
	ErrorCode  string          `json:"error_code"`
	DurationMS int64           `json:"duration_ms"`
	// RawBytes 只在未知 kind 上出现，表示原始记录长度，便于排查新事件类型。
	RawBytes int `json:"raw_bytes,omitempty"`
	// Content 与 Args 只在调用方明确要求 include_content 时出现，且已剔除敏感键。
	Content string `json:"content,omitempty"`
	Args    string `json:"args,omitempty"`
}

// traceToolBrief 是行内工具摘要；参数只给字节数，正文按需另给。
type traceToolBrief struct {
	Name     string `json:"name"`
	CallID   string `json:"call_id"`
	ArgsByte int    `json:"args_bytes"`
}

// traceRecord 是导出器唯一信任的字段视图。
//
// 刻意白名单解析：Checkpoint、Response 与加密推理历史从不进入结构体，
// 未知记录类型不会因为解码而把正文带进产物。
type traceRecord struct {
	Kind  string `json:"Kind"`
	Round int    `json:"Round"`
	Text  string `json:"Text"`
	Tool  struct {
		Call struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"Call"`
		Content   string `json:"Content"`
		ErrorCode string `json:"ErrorCode"`
		Duration  int64  `json:"Duration"`
	} `json:"Tool"`
}

// knownTraceKinds 列出当前版本认识的运行事件；其余 kind 原样保留并标注原始长度。
func knownTraceKinds(kind string) bool {
	switch agent.EventKind(kind) {
	case agent.RunStarted, agent.ModelStarted, agent.AttemptStarted, agent.TextDelta,
		agent.ThinkingDelta, agent.AttemptFinished, agent.ModelCompleted, agent.ToolStarted,
		agent.ToolFinished, agent.RunFinished, agent.EnvironmentCleanupFailed, agent.TaskStateUpdated,
		agent.ContextCompacted, agent.BudgetExtended, agent.BudgetStopped, agent.Steered,
		agent.SubagentStarted, agent.SubagentFinished, agent.BatchSubmitted, agent.BatchFinished:
		return true
	}
	return false
}

// executeTraceTool 执行模型发起的轨迹导出，授权只取运行时可信身份与任务上下文。
//
// 访问判定完全复用 AuthorizeOutput：当前任务必须在源任务链上，
// 且调用者是源任务发起人或本群任务管理员；因此 include_content 也天然受同一限制。
func (s *Service) executeTraceTool(ctx context.Context, args map[string]any) (any, error) {
	if s.tasks == nil {
		return nil, errors.New("任务服务未启用，无法导出任务轨迹")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("轨迹导出需要可信身份")
	}
	current := task.ID(ctx)
	if current <= 0 {
		return nil, errors.New("轨迹导出需要持久任务上下文")
	}
	if action, _ := args["action"].(string); action != "export" {
		return nil, errors.New("invalid trace action; only export is supported")
	}
	if format, ok := args["format"].(string); ok && format != "" && format != "jsonl" {
		return nil, fmt.Errorf("unsupported trace format %q", format)
	}
	taskID, err := traceTaskID(args["task_id"])
	if err != nil {
		return nil, err
	}
	includeContent, _ := args["include_content"].(bool)
	source, err := s.tasks.Get(ctx, identity.Session, taskID)
	if err != nil {
		return nil, traceLookupError(taskID, err)
	}
	if err := s.tasks.AuthorizeOutput(ctx, identity, current, taskID); err != nil {
		return nil, traceLookupError(taskID, err)
	}
	records, err := s.tasks.TraceRecords(ctx, identity.Session, taskID)
	if err != nil {
		return nil, traceLookupError(taskID, err)
	}
	// 批次归属取任务自身的 batch_id；非批次任务为空字符串，行格式因此保持不变。
	data, emitted, skipped, truncated := buildTraceLines(taskID, source.BatchID, records, includeContent)
	if s.executor == nil {
		return nil, errors.New("cannot write trace artifact: 未配置任务产物目录")
	}
	name := "trace-" + strconv.FormatInt(taskID, 10) + ".jsonl"
	path, err := s.executor.WriteArtifact(taskID, name, data)
	if err != nil {
		return nil, fmt.Errorf("cannot write trace artifact: %w", err)
	}
	slog.Info("任务轨迹已导出", "task", taskID, "artifact", filepath.Base(path), "records", emitted, "bytes", len(data), "truncated", truncated, "skipped", skipped)
	return traceExportResult{Artifact: filepath.Base(path), Records: emitted, Bytes: len(data), Truncated: truncated, Skipped: skipped}, nil
}

// traceTaskID 只接受正整数任务编号，避免用字符串绕过后续授权。
func traceTaskID(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int64(typed)) {
			return 0, errors.New("task id must be a positive integer")
		}
		if int64(typed) <= 0 {
			return 0, errors.New("task id must be a positive integer")
		}
		return int64(typed), nil
	case int64:
		if typed <= 0 {
			return 0, errors.New("task id must be a positive integer")
		}
		return typed, nil
	case int:
		if typed <= 0 {
			return 0, errors.New("task id must be a positive integer")
		}
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed <= 0 {
			return 0, errors.New("task id must be a positive integer")
		}
		return parsed, nil
	default:
		return 0, errors.New("task id must be a positive integer")
	}
}

// traceLookupError 把任务不存在与其他拒绝原因分开，模型才知道该换编号还是该放手。
func traceLookupError(taskID int64, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task #%d not found", taskID)
	}
	return err
}

// buildTraceLines 把持久化记录转换成产物内容，超过上限时按完整行截断。
//
// 返回写入的字节、行数、损坏记录数与截断标记；单条坏记录只计数，不会让整次导出失败。
func buildTraceLines(taskID int64, batchID string, records []task.EventRecord, includeContent bool) (data []byte, emitted, skipped int, truncated bool) {
	buffer := bytes.NewBuffer(nil)
	for _, record := range records {
		var parsed traceRecord
		if err := json.Unmarshal([]byte(record.Record), &parsed); err != nil || parsed.Kind == "" {
			skipped++
			continue
		}
		line := traceLine{
			Seq:        emitted + 1,
			At:         record.At.UTC().Format(time.RFC3339Nano),
			TaskID:     taskID,
			BatchID:    batchID,
			Kind:       parsed.Kind,
			Round:      parsed.Round,
			ErrorCode:  parsed.Tool.ErrorCode,
			DurationMS: parsed.Tool.Duration / int64(time.Millisecond),
		}
		if !knownTraceKinds(parsed.Kind) {
			line.RawBytes = len(record.Record)
		}
		tool := parsed.Tool.Call.Function.Name
		if tool != "" || parsed.Tool.Call.ID != "" {
			line.Tool = &traceToolBrief{Name: tool, CallID: parsed.Tool.Call.ID, ArgsByte: len(parsed.Tool.Call.Function.Arguments)}
		}
		if includeContent {
			if parsed.Tool.Content != "" {
				line.Content = redactTraceText(parsed.Tool.Content)
			} else if parsed.Text != "" {
				line.Content = redactTraceText(parsed.Text)
			}
			if parsed.Tool.Call.Function.Arguments != "" {
				line.Args = redactTraceText(parsed.Tool.Call.Function.Arguments)
			}
		}
		encoded, err := json.Marshal(line)
		if err != nil {
			skipped++
			continue
		}
		if buffer.Len()+len(encoded)+1 > traceMaxBytes {
			truncated = true
			break
		}
		buffer.Write(encoded)
		buffer.WriteByte('\n')
		emitted++
	}
	return buffer.Bytes(), emitted, skipped, truncated
}

// redactTraceText 剔除 JSON 形状正文中的敏感键；纯文本原样返回。
//
// 只在明确要求 include_content 时调用，仍然拒绝把凭据写进可交付产物。
func redactTraceText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return text
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return text
	}
	redacted, err := json.Marshal(redactTraceValue(value))
	if err != nil {
		return text
	}
	return string(redacted)
}

// redactTraceValue 递归剔除键名形似凭据的字段，数组保持原顺序。
func redactTraceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if traceSensitiveKey(key) {
				continue
			}
			result[key] = redactTraceValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactTraceValue(item)
		}
		return result
	default:
		return value
	}
}

// traceSensitiveKey 按小写键名匹配凭据片段，避免大小写与下划线变体漏网。
func traceSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range traceSensitiveKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}
