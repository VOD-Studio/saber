// Package ai_test 包含 AI 客户端的单元测试。
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
)

// TestNewClientWithModel 测试 NewClientWithModel 函数。
func TestNewClientWithModel(t *testing.T) {
	tests := []struct {
		name    string
		config  *config.ModelConfig
		wantErr bool
	}{
		{
			name: "valid openai config",
			config: &config.ModelConfig{
				Model:    "gpt-4",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "test-key",
			},
			wantErr: false,
		},
		{
			name: "valid azure config",
			config: &config.ModelConfig{
				Model:    "gpt-4",
				Provider: "azure",
				BaseURL:  "https://test.openai.azure.com",
				APIKey:   "test-key",
			},
			wantErr: false,
		},
		{
			name: "valid azure-openai config",
			config: &config.ModelConfig{
				Model:    "gpt-4",
				Provider: "azure-openai",
				BaseURL:  "https://test.openai.azure.com",
				APIKey:   "test-key",
			},
			wantErr: false,
		},
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
		},
		{
			name: "custom base url",
			config: &config.ModelConfig{
				Model:    "gpt-4",
				Provider: "openai",
				BaseURL:  "https://custom.api.com/v1",
				APIKey:   "test-key",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClientWithModel(tt.config)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if client == nil {
				t.Error("client is nil")
			}
		})
	}
}

// mockStreamingHandler 实现 StreamingChatCompletionHandler 接口。
type mockStreamingHandler struct {
	chunks    []string
	completed bool
	finalMsg  string
	err       error
}

func (m *mockStreamingHandler) OnChunk(ctx context.Context, chunk string) {
	m.chunks = append(m.chunks, chunk)
}

func (m *mockStreamingHandler) OnComplete(ctx context.Context, finalContent string, usage openai.Usage, model string) {
	m.completed = true
	m.finalMsg = finalContent
}

func (m *mockStreamingHandler) OnError(ctx context.Context, err error) {
	m.err = err
}

// setupMockServer 创建一个模拟的 OpenAI API 服务器。
func setupMockServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(func() { server.Close() })

	cfg := &config.ModelConfig{
		Model:    "gpt-4",
		Provider: "openai",
		BaseURL:  server.URL,
		APIKey:   "test-key",
	}

	client, err := NewClientWithModel(cfg)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	return client, server
}

// TestClient_CreateChatCompletion_NonStreaming 测试非流式聊天完成。
func TestClient_CreateChatCompletion_NonStreaming(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		resp := openai.ChatCompletionResponse{
			ID:      "test-id",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "gpt-4",
			Choices: []openai.ChatCompletionChoice{
				{
					Index: 0,
					Message: openai.ChatCompletionMessage{
						Role:    "assistant",
						Content: "Hello, I am an AI assistant.",
					},
					FinishReason: "stop",
				},
			},
			Usage: openai.Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, _ := setupMockServer(t, handler)
	ctx := context.Background()

	req := ChatCompletionRequest{
		Model:       "gpt-4",
		Messages:    []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:      false,
		MaxTokens:   100,
		Temperature: 0.7,
	}

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Content != "Hello, I am an AI assistant." {
		t.Errorf("unexpected content: %s", resp.Content)
	}
	if resp.Model != "gpt-4" {
		t.Errorf("unexpected model: %s", resp.Model)
	}
	if resp.Usage.TotalTokens != 30 {
		t.Errorf("unexpected total tokens: %d", resp.Usage.TotalTokens)
	}
}

// TestClient_CreateChatCompletion_NoChoices 测试无选择返回的情况。
func TestClient_CreateChatCompletion_NoChoices(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		resp := openai.ChatCompletionResponse{
			ID:      "test-id",
			Model:   "gpt-4",
			Choices: []openai.ChatCompletionChoice{},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	client, _ := setupMockServer(t, handler)
	ctx := context.Background()

	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:   false,
	}

	_, err := client.CreateChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected error for no choices")
	}
}

// TestClient_CreateChatCompletion_APIError 测试 API 错误。
func TestClient_CreateChatCompletion_APIError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"message": "internal server error"}}`))
	}

	client, _ := setupMockServer(t, handler)
	ctx := context.Background()

	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:   false,
	}

	_, err := client.CreateChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected error for API error")
	}
}

// TestClient_CreateChatCompletion_Streaming 测试流式聊天完成。
func TestClient_CreateChatCompletion_Streaming(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("streaming not supported")
			return
		}

		chunks := []string{"Hello", ", ", "world", "!"}
		for i, chunk := range chunks {
			data := map[string]any{
				"id":      "test-id",
				"object":  "chat.completion.chunk",
				"created": 1234567890,
				"model":   "gpt-4",
				"choices": []map[string]any{
					{
						"index": 0,
						"delta": map[string]string{
							"content": chunk,
						},
						"finish_reason": nil,
					},
				},
			}
			if i == len(chunks)-1 {
				data["choices"].([]map[string]any)[0]["finish_reason"] = "stop"
			}

			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}

	client, _ := setupMockServer(t, handler)
	ctx := context.Background()

	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:   true,
	}

	resp, err := client.CreateChatCompletion(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Hello, world!"
	if resp.Content != expected {
		t.Errorf("expected %q, got %q", expected, resp.Content)
	}
}

// TestClient_CreateStreamingChatCompletion 测试带回调的流式聊天完成。
func TestClient_CreateStreamingChatCompletion(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		chunks := []string{"Test", " ", "response"}
		for _, chunk := range chunks {
			data := map[string]any{
				"id":      "test-id",
				"object":  "chat.completion.chunk",
				"created": 1234567890,
				"model":   "gpt-4",
				"choices": []map[string]any{
					{
						"index": 0,
						"delta": map[string]string{
							"content": chunk,
						},
					},
				},
			}

			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}

	client, _ := setupMockServer(t, handler)
	ctx := context.Background()

	mockHandler := &mockStreamingHandler{}
	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:   true,
	}

	err := client.CreateStreamingChatCompletion(ctx, req, mockHandler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mockHandler.chunks) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(mockHandler.chunks))
	}

	if !mockHandler.completed {
		t.Error("OnComplete was not called")
	}

	expected := "Test response"
	if mockHandler.finalMsg != expected {
		t.Errorf("expected %q, got %q", expected, mockHandler.finalMsg)
	}
}

// TestClient_CreateStreamingChatCompletion_Error 测试流式聊天错误处理。
func TestClient_CreateStreamingChatCompletion_Error(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": {"message": "rate limit exceeded"}}`))
	}

	client, _ := setupMockServer(t, handler)
	ctx := context.Background()

	mockHandler := &mockStreamingHandler{}
	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:   true,
	}

	err := client.CreateStreamingChatCompletion(ctx, req, mockHandler)
	if err == nil {
		t.Error("expected error")
	}

	if mockHandler.err == nil {
		t.Error("OnError should have been called with error")
	}
}

// TestClient_CreateChatCompletion_ContextCancellation 测试上下文取消。
func TestClient_CreateChatCompletion_ContextCancellation(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-make(chan struct{}):
		}
	}

	client, _ := setupMockServer(t, handler)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := ChatCompletionRequest{
		Model:    "gpt-4",
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "Hello"}},
		Stream:   false,
	}

	_, err := client.CreateChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected error due to cancelled context")
	}
}

// TestChatCompletionRequest 测试请求结构体。
func TestChatCompletionRequest(t *testing.T) {
	req := ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello"},
		},
		Stream:      true,
		MaxTokens:   100,
		Temperature: 0.7,
		Model:       "gpt-4",
	}

	if len(req.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(req.Messages))
	}
	if req.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", req.Model)
	}
}

// TestChatCompletionResponse 测试响应结构体。
func TestChatCompletionResponse(t *testing.T) {
	resp := ChatCompletionResponse{
		Content: "Hello!",
		Usage: openai.Usage{
			PromptTokens:     5,
			CompletionTokens: 10,
			TotalTokens:      15,
		},
		Model: "gpt-4",
	}

	if resp.Content != "Hello!" {
		t.Errorf("unexpected content: %s", resp.Content)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("unexpected total tokens: %d", resp.Usage.TotalTokens)
	}
}
