package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"rua.plus/saber/internal/chat"
)

func decodeChatContent(raw json.RawMessage) (string, []string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil, nil
	}
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		} `json:"image_url"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return "", nil, fmt.Errorf("content 必须是字符串或非空内容数组")
	}
	var texts, images []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "image_url":
			if b.ImageURL.URL == "" {
				return "", nil, fmt.Errorf("image_url.url 不能为空")
			}
			a := chat.Attachment{Kind: "image", URL: b.ImageURL.URL}
			if err := (chat.Message{Session: chat.Session{Platform: "terminal", Account: "saber", Conversation: "validation"}, SenderID: "validation", Text: "image", Attachments: []chat.Attachment{a}}).Validate(); err != nil {
				return "", nil, err
			}
			images = append(images, a.URL)
		default:
			return "", nil, fmt.Errorf("不支持内容类型 %q", b.Type)
		}
	}
	return strings.Join(texts, "\n"), images, nil
}
func decodeAgentChatOptions(raw map[string]json.RawMessage, in *openAIChatInput) *openAIErrorBody {
	invalid := func(message string) *openAIErrorBody {
		return &openAIErrorBody{Message: message, Type: "invalid_request_error", Code: "invalid_request"}
	}
	for _, key := range []string{"session", "session_id"} {
		if v, ok := raw[key]; ok {
			var s string
			if json.Unmarshal(v, &s) != nil || !identifier.MatchString(s) {
				return invalid("无效会话编号")
			}
			if in.Session != "" && in.Session != s {
				return invalid("session 与 session_id 不一致")
			}
			in.Session = s
		}
	}
	if v, ok := raw["id"]; ok {
		if json.Unmarshal(v, &in.ID) != nil || !identifier.MatchString(in.ID) {
			return invalid("无效幂等键")
		}
	}
	if v, ok := raw["max_completion_tokens"]; ok {
		if _, both := raw["max_tokens"]; both {
			return invalid("max_tokens 与 max_completion_tokens 不能同时指定")
		}
		if json.Unmarshal(v, &in.MaxTokens) != nil || in.MaxTokens <= 0 {
			return invalid("max_completion_tokens 必须为正整数")
		}
	}
	if v, ok := raw["tools"]; ok {
		var tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(v, &tools) != nil {
			return invalid("tools 必须是数组")
		}
		if len(tools) == 0 {
			in.DisableTools = true
		}
		for _, t := range tools {
			if t.Type != "function" || t.Function.Name == "" {
				return invalid("仅接受具名 function 工具")
			}
			in.ToolNames = append(in.ToolNames, t.Function.Name)
		}
	}
	if v, ok := raw["tool_choice"]; ok {
		var choice string
		if json.Unmarshal(v, &choice) == nil {
			if choice != "auto" && choice != "none" && choice != "required" {
				return invalid("无效 tool_choice")
			}
			in.ToolChoice = choice
			in.DisableTools = choice == "none"
		} else {
			var specified struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(v, &specified) != nil || specified.Type != "function" || specified.Function.Name == "" {
				return invalid("无效 tool_choice")
			}
			in.ToolChoice = specified.Function.Name
		}
	}
	if v, ok := raw["stream_options"]; ok {
		var options struct {
			IncludeUsage bool `json:"include_usage"`
		}
		if json.Unmarshal(v, &options) != nil {
			return invalid("无效 stream_options")
		}
		in.IncludeUsage = options.IncludeUsage
	}
	return nil
}
