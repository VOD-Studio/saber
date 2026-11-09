package agent

import "github.com/sashabaranov/go-openai"

// AppendContextNotifications 将程序领取的真实通知追加为 user 上下文，恢复时共用相同协议。
func AppendContextNotifications(messages []openai.ChatCompletionMessage, notices []string) []openai.ChatCompletionMessage {
	for _, notice := range notices {
		if notice == "" {
			continue
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "[后台执行通知] " + notice})
	}
	return messages
}
