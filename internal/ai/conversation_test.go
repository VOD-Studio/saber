// Package ai 提供 AI 服务相关功能。
package ai

import (
	"testing"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

// mockPersonaService 是测试用的 Mock PersonaService。
type mockPersonaService struct {
	systemPrompt string
}

func (m *mockPersonaService) GetSystemPrompt(_ id.RoomID, basePrompt string) string {
	if m.systemPrompt != "" {
		return m.systemPrompt
	}
	return basePrompt
}

// TestNewMessageBuilder 测试 MessageBuilder 构造函数。
func TestNewMessageBuilder(t *testing.T) {
	builder := NewMessageBuilder()
	if builder == nil {
		t.Fatal("NewMessageBuilder() returned nil")
	}
}

// TestBuildTextMessages 测试构建纯文本消息。
func TestBuildTextMessages(t *testing.T) {
	t.Run("无上下文无系统提示", func(t *testing.T) {
		service := &Service{
			contextManager: nil,
			personaService: nil,
			core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: ""}},
		}
		builder := NewMessageBuilder()

		msgs := builder.BuildTextMessages(service, "!room:server.com", "hello")

		if len(msgs) != 1 {
			t.Errorf("BuildTextMessages() returned %d messages, want 1", len(msgs))
		}
		if msgs[0].Role != openai.ChatMessageRoleUser {
			t.Errorf("message role = %q, want user", msgs[0].Role)
		}
	})

	t.Run("有系统提示", func(t *testing.T) {
		service := &Service{
			contextManager: nil,
			personaService: nil,
			core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: "You are helpful"}},
		}
		builder := NewMessageBuilder()

		msgs := builder.BuildTextMessages(service, "!room:server.com", "hello")

		if len(msgs) != 2 {
			t.Errorf("BuildTextMessages() returned %d messages, want 2", len(msgs))
		}
		if msgs[0].Role != openai.ChatMessageRoleSystem {
			t.Errorf("first message role = %q, want system", msgs[0].Role)
		}
		if msgs[0].Content != "You are helpful" {
			t.Errorf("system prompt = %q, want %q", msgs[0].Content, "You are helpful")
		}
	})

	t.Run("有 PersonaService", func(t *testing.T) {
		service := &Service{
			contextManager: nil,
			personaService: &mockPersonaService{systemPrompt: "You are a helpful assistant"},
			core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: "base prompt"}},
		}
		builder := NewMessageBuilder()

		msgs := builder.BuildTextMessages(service, "!room:server.com", "hello")

		if len(msgs) != 2 {
			t.Errorf("BuildTextMessages() returned %d messages, want 2", len(msgs))
		}
		if msgs[0].Content != "You are a helpful assistant" {
			t.Errorf("system prompt = %q, want %q", msgs[0].Content, "You are a helpful assistant")
		}
	})
}

// TestBuildMultimodalMessages 测试构建多模态消息。
func TestBuildMultimodalMessages(t *testing.T) {
	t.Run("基本多模态消息", func(t *testing.T) {
		service := &Service{
			contextManager: nil,
			core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: "system"}},
		}
		builder := NewMessageBuilder()

		msgs := builder.BuildMultimodalMessages(service, nil, "!room:server.com", "hello", "data:image/png;base64,abc")

		// 应该有系统提示 + 用户消息
		if len(msgs) != 2 {
			t.Errorf("BuildMultimodalMessages() returned %d messages, want 2", len(msgs))
		}

		// 验证用户消息包含多模态内容
		userMsg := msgs[1]
		if userMsg.Role != openai.ChatMessageRoleUser {
			t.Errorf("user message role = %q, want user", userMsg.Role)
		}
		if len(userMsg.MultiContent) != 2 {
			t.Errorf("MultiContent length = %d, want 2", len(userMsg.MultiContent))
		}
	})

	t.Run("无系统提示", func(t *testing.T) {
		service := &Service{
			contextManager: nil,
			core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: ""}},
		}
		builder := NewMessageBuilder()

		msgs := builder.BuildMultimodalMessages(service, nil, "!room:server.com", "hello", "image_data")

		// 应该只有用户消息
		if len(msgs) != 1 {
			t.Errorf("BuildMultimodalMessages() returned %d messages, want 1", len(msgs))
		}
	})
}

// TestBuildMultiImageMessages 测试构建多图片消息。
func TestBuildMultiImageMessages(t *testing.T) {
	service := &Service{
		contextManager: nil,
		core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: ""}},
	}
	builder := NewMessageBuilder()

	imageDataList := []string{
		"data:image/png;base64,abc",
		"data:image/png;base64,def",
		"data:image/png;base64,ghi",
	}

	msgs := builder.BuildMultiImageMessages(service, nil, "!room:server.com", "hello", imageDataList)

	// 应该只有用户消息
	if len(msgs) != 1 {
		t.Errorf("BuildMultiImageMessages() returned %d messages, want 1", len(msgs))
	}

	// 验证多模态内容：文本 + 3张图片 = 4个部分
	userMsg := msgs[0]
	if len(userMsg.MultiContent) != 4 {
		t.Errorf("MultiContent length = %d, want 4 (text + 3 images)", len(userMsg.MultiContent))
	}

	// 验证第一部分是文本
	if userMsg.MultiContent[0].Type != openai.ChatMessagePartTypeText {
		t.Errorf("first part type = %v, want Text", userMsg.MultiContent[0].Type)
	}
	if userMsg.MultiContent[0].Text != "hello" {
		t.Errorf("first part text = %q, want %q", userMsg.MultiContent[0].Text, "hello")
	}
}

// TestPrependSystemPrompt 测试添加系统提示（通过公开方法间接测试）。
func TestPrependSystemPrompt(t *testing.T) {
	t.Run("已有系统提示不重复添加", func(t *testing.T) {
		// 通过 BuildTextMessages 测试 prependSystemPrompt 的行为
		// 当有 PersonaService 返回系统提示时，不应该覆盖已有提示
		service := &Service{
			contextManager: nil,
			personaService: &mockPersonaService{systemPrompt: "persona prompt"},
			core:           &Core{globalConfig: &config.AIConfig{SystemPrompt: "base prompt"}},
		}
		builder := NewMessageBuilder()

		// 第一次构建
		msgs1 := builder.BuildTextMessages(service, "!room:server.com", "hello")

		// 再次构建相同房间
		msgs2 := builder.BuildTextMessages(service, "!room:server.com", "world")

		// 两次应该都只有一个系统提示
		systemCount1 := 0
		for _, m := range msgs1 {
			if m.Role == openai.ChatMessageRoleSystem {
				systemCount1++
			}
		}
		systemCount2 := 0
		for _, m := range msgs2 {
			if m.Role == openai.ChatMessageRoleSystem {
				systemCount2++
			}
		}

		if systemCount1 != 1 {
			t.Errorf("first call: system message count = %d, want 1", systemCount1)
		}
		if systemCount2 != 1 {
			t.Errorf("second call: system message count = %d, want 1", systemCount2)
		}
	})
}