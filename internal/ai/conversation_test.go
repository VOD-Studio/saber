//go:build goolm

package ai

import (
	"testing"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"
)

// TestMessageBuilder_BuildTextMessages 测试构建文本消息。
// 注意：BuildTextMessages 在有 contextManager 时只返回历史消息，不添加当前用户消息
// （与其他 Build 方法行为不同，这是当前实现的行为）
func TestMessageBuilder_BuildTextMessages(t *testing.T) {
	t.Run("without context manager", func(t *testing.T) {
		cfg := createTestAIConfig()
		cfg.Context.Enabled = false // 明确禁用 context
		service, _ := NewService(cfg, nil, nil, nil)

		builder := NewMessageBuilder()
		messages := builder.BuildTextMessages(service, "!room:example.com", "hello")

		// 无 contextManager 时，会创建包含 userInput 的消息
		if len(messages) < 1 {
			t.Error("expected at least 1 message")
		}
		// 检查消息内容包含用户输入
		foundUserInput := false
		for _, msg := range messages {
			if msg.Content == "hello" {
				foundUserInput = true
				break
			}
		}
		if !foundUserInput {
			t.Error("expected to find user input 'hello' in messages")
		}
	})

	t.Run("with context manager", func(t *testing.T) {
		cfg := createTestAIConfig()
		cfg.Context.Enabled = true
		service, _ := NewService(cfg, nil, nil, nil)

		roomID := id.RoomID("!room:example.com")
		service.contextManager.AddMessage(roomID, RoleUser, "previous message", "@user:example.com")

		builder := NewMessageBuilder()
		// 注意：当前实现中，BuildTextMessages 不会添加 "new message"
		messages := builder.BuildTextMessages(service, roomID, "new message")

		// 有 contextManager 时，只返回历史消息（不添加当前用户消息）
		if len(messages) < 1 {
			t.Error("expected at least 1 message from history")
		}
		// 验证历史消息存在
		foundHistory := false
		for _, msg := range messages {
			if msg.Content == "previous message" {
				foundHistory = true
				break
			}
		}
		if !foundHistory {
			t.Error("expected to find history message 'previous message'")
		}
	})

	t.Run("with context manager and system prompt", func(t *testing.T) {
		cfg := createTestAIConfig()
		cfg.Context.Enabled = true
		cfg.SystemPrompt = "You are a helpful assistant"
		service, _ := NewService(cfg, nil, nil, nil)

		roomID := id.RoomID("!room:example.com")
		service.contextManager.AddMessage(roomID, RoleUser, "user message", "@user:example.com")

		builder := NewMessageBuilder()
		messages := builder.BuildTextMessages(service, roomID, "new input")

		// 应包含系统提示词和历史消息
		if len(messages) < 2 {
			t.Errorf("expected at least 2 messages (system + history), got %d", len(messages))
		}
		// 第一条应该是系统消息
		if messages[0].Role != string(RoleSystem) {
			t.Errorf("expected first message to be system role, got %s", messages[0].Role)
		}
	})
}

// TestMessageBuilder_BuildMultimodalMessages 测试构建多模态消息。
func TestMessageBuilder_BuildMultimodalMessages(t *testing.T) {
	t.Run("basic multimodal message", func(t *testing.T) {
		cfg := createTestAIConfig()
		service, _ := NewService(cfg, nil, nil, nil)

		builder := NewMessageBuilder()
		messages := builder.BuildMultimodalMessages(
			service,
			nil,
			"!room:example.com",
			"what is this?",
			"data:image/png;base64,test",
		)

		if len(messages) < 1 {
			t.Error("expected at least 1 message")
		}

		// 检查最后一条消息是多模态的
		lastMsg := messages[len(messages)-1]
		if lastMsg.Role != openai.ChatMessageRoleUser {
			t.Errorf("expected user role, got %s", lastMsg.Role)
		}
		if len(lastMsg.MultiContent) < 2 {
			t.Errorf("expected at least 2 parts (text + image), got %d", len(lastMsg.MultiContent))
		}
	})
}

// TestMessageBuilder_BuildMultiImageMessages 测试构建多图片消息。
func TestMessageBuilder_BuildMultiImageMessages(t *testing.T) {
	t.Run("multiple images", func(t *testing.T) {
		cfg := createTestAIConfig()
		service, _ := NewService(cfg, nil, nil, nil)

		builder := NewMessageBuilder()
		messages := builder.BuildMultiImageMessages(
			service,
			nil,
			"!room:example.com",
			"compare these",
			[]string{"data:image/png;base64,img1", "data:image/png;base64,img2"},
		)

		if len(messages) < 1 {
			t.Error("expected at least 1 message")
		}

		// 检查最后一条消息有多个部分
		lastMsg := messages[len(messages)-1]
		if len(lastMsg.MultiContent) < 3 {
			t.Errorf("expected at least 3 parts (text + 2 images), got %d", len(lastMsg.MultiContent))
		}
	})

	t.Run("empty image list", func(t *testing.T) {
		cfg := createTestAIConfig()
		service, _ := NewService(cfg, nil, nil, nil)

		builder := NewMessageBuilder()
		messages := builder.BuildMultiImageMessages(
			service,
			nil,
			"!room:example.com",
			"hello",
			[]string{},
		)

		if len(messages) < 1 {
			t.Error("expected at least 1 message")
		}

		// 检查最后一条消息
		lastMsg := messages[len(messages)-1]
		if len(lastMsg.MultiContent) < 1 {
			t.Error("expected at least 1 part (text)")
		}
	})
}

// TestMessageBuilder_prependSystemPrompt 测试系统提示词添加。
func TestMessageBuilder_prependSystemPrompt(t *testing.T) {
	builder := &MessageBuilder{}

	t.Run("add to empty messages", func(t *testing.T) {
		messages := []openai.ChatCompletionMessage{}
		result := builder.prependSystemPrompt(messages, "system prompt")

		if len(result) != 1 {
			t.Errorf("expected 1 message, got %d", len(result))
		}
		if result[0].Role != string(RoleSystem) {
			t.Errorf("expected system role, got %s", result[0].Role)
		}
	})

	t.Run("add to existing messages", func(t *testing.T) {
		messages := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		}
		result := builder.prependSystemPrompt(messages, "system prompt")

		if len(result) != 2 {
			t.Errorf("expected 2 messages, got %d", len(result))
		}
		if result[0].Role != string(RoleSystem) {
			t.Errorf("expected first message to be system, got %s", result[0].Role)
		}
	})

	t.Run("skip if already has system", func(t *testing.T) {
		messages := []openai.ChatCompletionMessage{
			{Role: string(RoleSystem), Content: "existing system"},
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		}
		result := builder.prependSystemPrompt(messages, "new system prompt")

		if len(result) != 2 {
			t.Errorf("expected 2 messages (unchanged), got %d", len(result))
		}
		if result[0].Content != "existing system" {
			t.Errorf("expected 'existing system', got %s", result[0].Content)
		}
	})
}
