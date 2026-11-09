package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sashabaranov/go-openai"
)

func (d *Delegator) delegateBatch(ctx context.Context, raw any) (ToolOutput, error) {
	var tasks []map[string]any
	switch value := raw.(type) {
	case []map[string]any:
		tasks = value
	case []any:
		for _, item := range value {
			task, ok := item.(map[string]any)
			if !ok {
				return ToolOutput{}, errors.New("tasks 每项必须为对象")
			}
			tasks = append(tasks, task)
		}
	default:
		return ToolOutput{}, errors.New("tasks 必须为数组")
	}
	policy := d.Policy.Normalize()
	if len(tasks) == 0 || len(tasks) > policy.MaxParallel {
		return ToolOutput{}, fmt.Errorf("tasks 数量必须在 1 到 %d 之间", policy.MaxParallel)
	}
	// 全部参数校验完成后才派发第一个子任务，避免错误的后项造成半批副作用。
	for _, args := range tasks {
		if args["tasks"] != nil {
			return ToolOutput{}, errors.New("tasks 不允许嵌套批次")
		}
		goal, err := delegateGoal(args)
		if err != nil {
			return ToolOutput{}, err
		}
		names, err := delegateToolNames(args["tools"])
		if err != nil {
			return ToolOutput{}, err
		}
		if _, err := d.childTools(names); err != nil {
			return ToolOutput{}, err
		}
		if d.Route != nil {
			probe := Request{Model: d.Parent.Model, Messages: []openai.ChatCompletionMessage{{Role: "system"}, {Role: "user", Content: goal}}}
			if err := d.Route(ctx, args, &probe); err != nil {
				return ToolOutput{}, err
			}
		}
	}
	outputs := make([]ToolOutput, len(tasks))
	errorsByIndex := make([]error, len(tasks))
	var workers sync.WaitGroup
	for index, args := range tasks {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := ctx.Err(); err != nil {
				errorsByIndex[index] = err
				return
			}
			outputs[index], errorsByIndex[index] = d.Delegate(ctx, args)
		}()
	}
	workers.Wait()
	results := make([]any, len(tasks))
	var files []string
	var images []string
	for index, output := range outputs {
		if err := errorsByIndex[index]; err != nil {
			results[index] = SubagentResult{Status: Failed, Error: err.Error()}
		} else {
			results[index] = output.Value
		}
		images = append(images, output.Images...)
		files = append(files, output.Files...)
	}
	return ToolOutput{Value: map[string]any{"results": results}, Images: images, Files: files}, nil
}

func childResultImages(result Result) []string {
	var images []string
	seen := make(map[string]bool)
	for _, round := range result.Rounds {
		for _, tool := range round.Tools {
			for _, image := range tool.Images {
				if !seen[image] {
					seen[image] = true
					images = append(images, image)
				}
			}
		}
	}
	return images
}

func childTrace(result Result) []ToolRecord {
	var records []ToolRecord
	for _, round := range result.Rounds {
		for _, record := range round.Tools {
			record.Images = nil
			records = append(records, record)
		}
	}
	return records
}

func childResultFiles(result Result) []string {
	var files []string
	for _, round := range result.Rounds {
		for _, record := range round.Tools {
			files = append(files, record.Files...)
		}
	}
	return files
}

func delegateTasksJSON(raw any) (any, error) {
	if text, ok := raw.(string); ok {
		var tasks []any
		if err := json.Unmarshal([]byte(text), &tasks); err != nil {
			return nil, err
		}
		return tasks, nil
	}
	return raw, nil
}
