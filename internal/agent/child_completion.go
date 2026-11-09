package agent

import (
	"encoding/json"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// ChildCompletion 是任务管理器权威领取的子任务终态，产物只来自已落盘的工具记录。
type ChildCompletion struct {
	TaskID  int64    `json:"task_id"`
	Goal    string   `json:"goal"`
	Status  string   `json:"status"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
	Files   []string `json:"files,omitempty"`
}

// AppendChildCompletionMessages 在完整父工具批次之后回流子结果；图片不混入 JSON token 文本。
func AppendChildCompletionMessages(messages []openai.ChatCompletionMessage, completions []ChildCompletion) []openai.ChatCompletionMessage {
	for _, completion := range completions {
		text, err := json.Marshal(struct {
			TaskID  int64    `json:"task_id"`
			Goal    string   `json:"goal"`
			Status  string   `json:"status"`
			Content string   `json:"content"`
			Files   []string `json:"files,omitempty"`
		}{completion.TaskID, completion.Goal, completion.Status, completion.Content, completion.Files})
		if err != nil {
			panic(err)
		} // 固定字符串结构不存在可失败的自定义编码器。
		if len(completion.Images) == 0 {
			messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "[子代理结果] " + string(text)})
			continue
		}
		parts := []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: fmt.Sprintf("[子代理结果] %s", text)}}
		for _, image := range completion.Images {
			parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: image, Detail: openai.ImageURLDetailAuto}})
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts})
	}
	return messages
}
