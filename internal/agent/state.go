package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
)

const maxWorkStateBytes = 8192

// TaskStateTool 返回持久化任务内部状态工具的 schema。
func TaskStateTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name:        "saber_task_state",
		Description: "读取或更新本任务的程序化工作状态（计划、发现、产物、未决问题）。原始用户指令、revision 与身份由程序维护，不能通过此工具修改。",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"read", "update"}},
			"plan": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"text":   map[string]any{"type": "string"},
				"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done"}},
			}, "required": []string{"text", "status"}, "additionalProperties": false}},
			"findings":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"artifacts":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"open_issues": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, "required": []string{"action"}, "additionalProperties": false},
	}}
}

// CloneTaskState 深拷贝状态，避免持久化或并发 UI 共享可变切片。
func CloneTaskState(state *TaskState) *TaskState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.Instructions = slices.Clone(state.Instructions)
	clone.Plan = slices.Clone(state.Plan)
	clone.Findings = slices.Clone(state.Findings)
	clone.Artifacts = slices.Clone(state.Artifacts)
	clone.OpenIssues = slices.Clone(state.OpenIssues)
	return &clone
}

// ApplyTaskStateTool 校验并应用状态更新；changed 为 false 时不递增 revision。
func ApplyTaskStateTool(state *TaskState, args map[string]any) (updated *TaskState, changed bool, err error) {
	action, ok := args["action"].(string)
	if !ok || action != "read" && action != "update" {
		return nil, false, errors.New("action must be read or update")
	}
	workKeys := []string{"plan", "findings", "artifacts", "open_issues"}
	for key := range args {
		if key != "action" && !slices.Contains(workKeys, key) {
			return nil, false, fmt.Errorf("unexpected task state field %q", key)
		}
	}
	if action == "read" {
		for _, key := range workKeys {
			if _, exists := args[key]; exists {
				return nil, false, fmt.Errorf("read does not accept %q", key)
			}
		}
		return CloneTaskState(state), false, nil
	}
	present := false
	for _, key := range workKeys {
		if _, exists := args[key]; exists {
			present = true
		}
	}
	if !present {
		return nil, false, errors.New("update requires at least one work field")
	}
	next := CloneTaskState(state)
	if value, exists := args["plan"]; exists {
		plan, err := parsePlan(value)
		if err != nil {
			return nil, false, err
		}
		next.Plan = plan
	}
	for _, key := range []string{"findings", "artifacts", "open_issues"} {
		value, exists := args[key]
		if !exists {
			continue
		}
		items, err := parseStringList(value)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", key, err)
		}
		switch key {
		case "findings":
			next.Findings = items
		case "artifacts":
			next.Artifacts = items
		case "open_issues":
			next.OpenIssues = items
		}
	}
	work, err := json.Marshal(struct {
		Plan       []PlanStep `json:"plan"`
		Findings   []string   `json:"findings"`
		Artifacts  []string   `json:"artifacts"`
		OpenIssues []string   `json:"open_issues"`
	}{next.Plan, next.Findings, next.Artifacts, next.OpenIssues})
	if err != nil {
		return nil, false, err
	}
	if len(work) > maxWorkStateBytes {
		return nil, false, fmt.Errorf("task work state exceeds %d bytes", maxWorkStateBytes)
	}
	same := slices.Equal(next.Plan, state.Plan) && slices.Equal(next.Findings, state.Findings) && slices.Equal(next.Artifacts, state.Artifacts) && slices.Equal(next.OpenIssues, state.OpenIssues)
	if same {
		return next, false, nil
	}
	next.Revision = state.Revision + 1
	return next, true, nil
}

// callStateTool 在 Runtime 内部执行状态工具，复用同样的记录与截断规则。
func (r Runtime) callStateTool(ctx context.Context, call openai.ToolCall, state **TaskState, round, limit int, emit func(Event)) ToolRecord {
	started := time.Now()
	record := ToolRecord{Call: call}
	var args map[string]any
	var data []byte
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
		record.ErrorCode = "invalid_arguments"
		if err == nil {
			err = errors.New("tool arguments must be a JSON object")
		}
		data = errorContent(record.ErrorCode, err.Error())
	} else if err := ctx.Err(); err != nil {
		record.ErrorCode = "cancelled"
		data = errorContent(record.ErrorCode, err.Error())
	} else if next, changed, err := ApplyTaskStateTool(*state, args); err != nil {
		record.ErrorCode = "invalid_arguments"
		data = errorContent(record.ErrorCode, err.Error())
	} else {
		*state = next
		data, err = json.Marshal(map[string]any{"ok": true, "state": next})
		if err != nil {
			record.ErrorCode = "serialization_failed"
			data = errorContent(record.ErrorCode, err.Error())
		} else if changed {
			emit(Event{Kind: TaskStateUpdated, Round: round, State: CloneTaskState(next)})
		}
	}
	record.OriginalBytes = len(data)
	record.Content = string(data)
	if len(data) > limit {
		suffix := fmt.Sprintf("\n[truncated: original_bytes=%d]", len(data))
		end := limit - len(suffix)
		for end > 0 && !utf8.Valid(data[:end]) {
			end--
		}
		record.Content = string(data[:end]) + suffix
		record.Truncated = true
	}
	record.Duration = time.Since(started)
	return record
}

func parsePlan(value any) ([]PlanStep, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("plan must be an array")
	}
	plan := make([]PlanStep, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok || len(entry) != 2 {
			return nil, errors.New("plan step requires text and status")
		}
		text, textOK := entry["text"].(string)
		status, statusOK := entry["status"].(string)
		if !textOK || text == "" || !statusOK {
			return nil, errors.New("plan step requires non-empty text and status")
		}
		if !slices.Contains([]string{"pending", "in_progress", "done"}, status) {
			return nil, fmt.Errorf("invalid plan status %q", status)
		}
		plan = append(plan, PlanStep{Text: text, Status: status})
	}
	return plan, nil
}

func parseStringList(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("must be an array of strings")
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, errors.New("must be an array of strings")
		}
		result = append(result, text)
	}
	return result, nil
}
