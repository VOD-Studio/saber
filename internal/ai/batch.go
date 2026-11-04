package ai

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

const (
	// batchMaxItems 是一次提交的条目上限，避免一次批次把队列排满。
	batchMaxItems = 20
	// batchMaxText 是单条指令的字符上限，超出整批拒绝而不是截断。
	batchMaxText = 8000
	// batchMaxLabel 是批次说明的字符上限，说明不参与执行。
	batchMaxLabel = 256
)

// batchIDPattern 固定批次编号形状，避免拿任意字符串去查询。
var batchIDPattern = regexp.MustCompile(`^b_[0-9a-f]{16}$`)

// batchTool 让模型把多条互不依赖的指令一次提交成独立任务。
//
// 批次不共享上下文、不绕过工作目录互斥；描述里写明这些边界，避免模型把它当并发加速器。
func batchTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: "saber_batch",
		Description: "一次提交多条互不依赖的任务（1..20 条，每条 text ≤ 8000 字符），或用 batch_id 查询、取消批次。" +
			"批次条目各自是根任务：不共享对话历史与检查点，需要的上下文只能写进各自的 text。" +
			"批次不绕过工作目录互斥与 agent.max_concurrent_tasks：同一授权工作区的条目会串行执行，不要用它做并行加速。" +
			"silent=true 的条目结果只落库、不向聊天投递。提交是单事务，任一条校验失败整批拒绝。" +
			"只有批次创建者或本群任务管理员可以查询与取消；身份与群由系统提供，不得代其他用户操作。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"submit", "status", "cancel"},
					"description": "操作类型：submit 提交批次；status 查询状态与逐条结果；cancel 取消批次内非终态任务",
				},
				"items": map[string]any{
					"type":        "array",
					"minItems":    1,
					"maxItems":    batchMaxItems,
					"description": "submit 必填：按顺序提交的任务条目",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text":   map[string]any{"type": "string", "description": "这条任务完整的用户指令，需自带所需上下文"},
							"silent": map[string]any{"type": "boolean", "description": "结果只落库、不向聊天投递"},
						},
						"required":             []string{"text"},
						"additionalProperties": false,
					},
				},
				"label": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("可选的批次说明，最多 %d 字符，不参与执行", batchMaxLabel),
				},
				"batch_id": map[string]any{
					"type":        "string",
					"description": "status 与 cancel 必填：submit 返回的批次编号",
				},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}}
}

// batchSubmitResult 是提交结果；任务编号按 items 顺序排列。
type batchSubmitResult struct {
	BatchID string  `json:"batch_id"`
	TaskIDs []int64 `json:"task_ids"`
}

// batchStatusItem 是批次内一条任务的状态摘要。
type batchStatusItem struct {
	TaskID    int64  `json:"task_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// batchStatusResult 汇总批次状态，counts 始终包含全部六种状态。
type batchStatusResult struct {
	BatchID string            `json:"batch_id"`
	Total   int               `json:"total"`
	Counts  map[string]int    `json:"counts"`
	Items   []batchStatusItem `json:"items"`
}

// batchCancelResult 区分已取消与未取消的任务，不做假原子性。
type batchCancelResult struct {
	Cancelled []int64 `json:"cancelled"`
	Skipped   []int64 `json:"skipped"`
}

// executeBatchTool 执行模型发起的批次操作，授权只取运行时可信身份。
func (s *Service) executeBatchTool(ctx context.Context, args map[string]any) (any, error) {
	if s.tasks == nil {
		return nil, errors.New("任务服务未启用，无法使用批次")
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return nil, errors.New("批次工具需要可信身份")
	}
	action, _ := args["action"].(string)
	switch action {
	case "submit":
		return s.submitBatchTool(ctx, identity, args)
	case "status":
		batchID, err := batchIDArg(args["batch_id"])
		if err != nil {
			return nil, err
		}
		batch, tasks, err := s.tasks.BatchStatus(ctx, identity, batchID)
		if err != nil {
			return nil, err
		}
		return batchStatusView(batch, tasks), nil
	case "cancel":
		batchID, err := batchIDArg(args["batch_id"])
		if err != nil {
			return nil, err
		}
		_, cancelled, skipped, err := s.tasks.CancelBatch(ctx, identity, batchID)
		if err != nil {
			return nil, err
		}
		return batchCancelResult{Cancelled: cancelled, Skipped: skipped}, nil
	default:
		return nil, errors.New("invalid batch action; use submit, status or cancel")
	}
}

// submitBatchTool 校验条目、按当前授权目录组装请求，再交给存储层单事务落库。
func (s *Service) submitBatchTool(ctx context.Context, identity chat.Identity, args map[string]any) (any, error) {
	entries, err := batchEntriesArg(args["items"])
	if err != nil {
		return nil, err
	}
	label, err := batchLabelArg(args["label"])
	if err != nil {
		return nil, err
	}
	dir := s.taskDir
	if s.executor != nil {
		if granted, err := s.executor.Workspace(identity); err == nil {
			dir = granted
		}
	}
	model := s.GetModelRegistry().GetDefault()
	requests := make([]task.BatchEntry, 0, len(entries))
	for index, entry := range entries {
		message := chat.Message{Session: identity.Session, SenderID: identity.SenderID, Direct: identity.Direct, Text: entry.text}
		req, err := s.taskRequest(message, model)
		if err != nil {
			return nil, fmt.Errorf("batch item %d: %w", index, err)
		}
		// 条目是独立根任务：只带本条目指令，不继承提交者任务链或聊天历史。
		req.Messages = append(req.Messages, taskInput(message))
		requests = append(requests, task.BatchEntry{Text: entry.text, Silent: entry.silent, Request: req})
	}
	batch, ids, err := s.tasks.SubmitBatch(ctx, identity, dir, label, requests)
	if err != nil {
		return nil, err
	}
	return batchSubmitResult{BatchID: batch.ID, TaskIDs: ids}, nil
}

// batchStatusView 把批次与任务摘要转换成工具返回值，counts 补齐零值状态。
func batchStatusView(batch task.Batch, tasks []task.BatchTask) batchStatusResult {
	counts := map[string]int{"queued": 0, "running": 0, "completed": 0, "failed": 0, "cancelled": 0, "interrupted": 0}
	items := make([]batchStatusItem, 0, len(tasks))
	for _, item := range tasks {
		counts[item.Status]++
		items = append(items, batchStatusItem{TaskID: item.TaskID, Status: item.Status, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return batchStatusResult{BatchID: batch.ID, Total: batch.Total, Counts: counts, Items: items}
}

// batchEntry 是校验后的提交条目。
type batchEntry struct {
	text   string
	silent bool
}

// batchEntriesArg 校验 items 的数量、形状与单条长度；任何一条不合法都整批拒绝。
func batchEntriesArg(value any) ([]batchEntry, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("items is required and must contain 1..20 entries")
	}
	if len(items) > batchMaxItems {
		return nil, fmt.Errorf("batch accepts at most %d items", batchMaxItems)
	}
	entries := make([]batchEntry, 0, len(items))
	for index, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("batch item %d: must be an object", index)
		}
		text, _ := item["text"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, fmt.Errorf("batch item %d: text is required", index)
		}
		if utf8.RuneCountInString(text) > batchMaxText {
			return nil, fmt.Errorf("batch item %d: text exceeds %d characters", index, batchMaxText)
		}
		silent, _ := item["silent"].(bool)
		entries = append(entries, batchEntry{text: text, silent: silent})
	}
	return entries, nil
}

// batchLabelArg 规范化可选说明；说明不参与执行，只用于人工识别。
func batchLabelArg(value any) (string, error) {
	label, _ := value.(string)
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > batchMaxLabel {
		return "", fmt.Errorf("label exceeds %d characters", batchMaxLabel)
	}
	return label, nil
}

// batchIDArg 只接受程序生成的批次编号。
func batchIDArg(value any) (string, error) {
	id, _ := value.(string)
	if !batchIDPattern.MatchString(id) {
		return "", errors.New("batch_id must look like b_<16 hex characters>")
	}
	return id, nil
}
