package agent

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// ValidateImageReference 只接受明确的 HTTP 图片引用或合法图片 data URL，不读取宿主任意文件。
func ValidateImageReference(ref string) error {
	if strings.HasPrefix(ref, "data:image/") {
		header, data, ok := strings.Cut(ref, ",")
		if !ok || !strings.HasSuffix(header, ";base64") || len(data) == 0 || len(data) > 28*1024*1024 {
			return errors.New("图片 data URL 必须为非空 base64 且不超过 20 MiB")
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return err
		}
		return nil
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errors.New("图片必须为 HTTP(S) URL 或 image base64 data URL")
	}
	return nil
}

// AppendToolMessages 先补齐一个 assistant 的所有工具回复，再追加工具图片，恢复与实时运行共用协议。
func AppendToolMessages(messages []openai.ChatCompletionMessage, records []ToolRecord) []openai.ChatCompletionMessage {
	for _, record := range records {
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleTool, ToolCallID: record.Call.ID, Content: record.Content})
	}
	return appendToolImages(messages, records)
}

func appendToolImages(messages []openai.ChatCompletionMessage, records []ToolRecord) []openai.ChatCompletionMessage {
	for _, record := range records {
		if len(record.Images) == 0 {
			continue
		}
		parts := []openai.ChatMessagePart{{Type: openai.ChatMessagePartTypeText, Text: "[工具图片] " + record.Call.Function.Name + " (" + record.Call.ID + ")"}}
		for _, ref := range record.Images {
			parts = append(parts, openai.ChatMessagePart{Type: openai.ChatMessagePartTypeImageURL, ImageURL: &openai.ChatMessageImageURL{URL: ref, Detail: openai.ImageURLDetailAuto}})
		}
		messages = append(messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, MultiContent: parts})
	}
	return messages
}
