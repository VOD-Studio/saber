// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
package ai

import (
	"context"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
)

// MessageBuilder 负责构建 AI 对话消息。
//
// 它提供多种方法来构建不同类型的消息列表：
//   - 纯文本消息
//   - 单图片多模态消息
//   - 多图片多模态消息
//
// MessageBuilder 是无状态的，所有方法都接收 Service 作为参数来访问配置和上下文。
type MessageBuilder struct{}

// NewMessageBuilder 创建一个新的消息构建器。
//
// 返回值:
//   - *MessageBuilder: 新创建的消息构建器实例
func NewMessageBuilder() *MessageBuilder {
	return &MessageBuilder{}
}

// BuildTextMessages 构建纯文本消息列表。
// 它从上下文管理器获取历史消息，并添加当前用户消息。
//
// 参数:
//   - service: AI 服务实例，用于访问配置和上下文管理器
//   - roomID: Matrix 房间 ID
//   - userInput: 用户输入的文本
//
// 返回值:
//   - []openai.ChatCompletionMessage: 构建的消息列表
func (b *MessageBuilder) BuildTextMessages(service *Service, roomID id.RoomID, userInput string) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage
	if service.contextManager != nil {
		messages = service.contextManager.GetContext(roomID)
	} else {
		messages = []openai.ChatCompletionMessage{
			{Role: string(RoleUser), Content: userInput},
		}
	}

	// 获取系统提示词（合并基础提示词和人格提示词）
	systemPrompt := b.getSystemPrompt(service, roomID)
	if systemPrompt != "" {
		messages = b.prependSystemPrompt(messages, systemPrompt)
	}

	return messages
}

// BuildMultimodalMessages 构建多模态消息列表（文本 + 图片）。
// 它从上下文管理器获取历史消息，并添加包含文本和图片的用户消息。
//
// 参数:
//   - service: AI 服务实例
//   - ctx: 上下文
//   - roomID: Matrix 房间 ID
//   - userInput: 用户输入的文本
//   - imageData: Base64 Data URL 格式的图片数据
//
// 返回值:
//   - []openai.ChatCompletionMessage: 构建的消息列表
func (b *MessageBuilder) BuildMultimodalMessages(service *Service, _ context.Context, roomID id.RoomID, userInput, imageData string) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage
	if service.contextManager != nil {
		messages = service.contextManager.GetContext(roomID)
	}

	// 构建当前用户消息（文本 + 图片）
	userMessage := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{
				Type: openai.ChatMessagePartTypeText,
				Text: userInput,
			},
			{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    imageData,
					Detail: openai.ImageURLDetailAuto,
				},
			},
		},
	}

	messages = append(messages, userMessage)

	// 获取系统提示词（合并基础提示词和人格提示词）
	systemPrompt := b.getSystemPrompt(service, roomID)
	if systemPrompt != "" {
		messages = b.prependSystemPrompt(messages, systemPrompt)
	}

	return messages
}

// BuildMultiImageMessages 构建包含多张图片的多模态消息列表。
// 支持同时处理用户发送的图片和引用消息中的图片。
//
// 参数:
//   - service: AI 服务实例
//   - ctx: 上下文
//   - roomID: Matrix 房间 ID
//   - userInput: 用户输入的文本
//   - imageDataList: Base64 Data URL 格式的图片数据列表
//
// 返回值:
//   - []openai.ChatCompletionMessage: 构建的消息列表
func (b *MessageBuilder) BuildMultiImageMessages(service *Service, _ context.Context, roomID id.RoomID, userInput string, imageDataList []string) []openai.ChatCompletionMessage {
	var messages []openai.ChatCompletionMessage
	if service.contextManager != nil {
		messages = service.contextManager.GetContext(roomID)
	}

	// 构建消息部分，首先是文本
	parts := []openai.ChatMessagePart{
		{
			Type: openai.ChatMessagePartTypeText,
			Text: userInput,
		},
	}

	// 添加所有图片
	for _, imageData := range imageDataList {
		parts = append(parts, openai.ChatMessagePart{
			Type: openai.ChatMessagePartTypeImageURL,
			ImageURL: &openai.ChatMessageImageURL{
				URL:    imageData,
				Detail: openai.ImageURLDetailAuto,
			},
		})
	}

	userMessage := openai.ChatCompletionMessage{
		Role:         openai.ChatMessageRoleUser,
		MultiContent: parts,
	}

	messages = append(messages, userMessage)

	// 获取系统提示词（合并基础提示词和人格提示词）
	systemPrompt := b.getSystemPrompt(service, roomID)
	if systemPrompt != "" {
		messages = b.prependSystemPrompt(messages, systemPrompt)
	}

	return messages
}

// getSystemPrompt 获取系统提示词。
// 如果设置了提示词提供者，会合并基础提示词和人格提示词。
func (b *MessageBuilder) getSystemPrompt(service *Service, roomID id.RoomID) string {
	basePrompt := service.core.GetConfig().SystemPrompt
	if service.promptProvider != nil {
		return service.promptProvider.GetSystemPrompt(roomID, basePrompt)
	}
	return basePrompt
}

// prependSystemPrompt 在消息列表前添加系统提示词（如果还没有）。
//
// 参数:
//   - messages: 当前消息列表
//   - prompt: 系统提示词
//
// 返回值:
//   - []openai.ChatCompletionMessage: 处理后的消息列表
func (b *MessageBuilder) prependSystemPrompt(messages []openai.ChatCompletionMessage, prompt string) []openai.ChatCompletionMessage {
	hasSystem := false
	for _, msg := range messages {
		if msg.Role == string(RoleSystem) {
			hasSystem = true
			break
		}
	}
	if hasSystem {
		return messages
	}
	result := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	result = append(result, openai.ChatCompletionMessage{
		Role:    string(RoleSystem),
		Content: prompt,
	})
	return append(result, messages...)
}
