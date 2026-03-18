package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
)

// sharedTransport 是共享的 HTTP Transport，用于连接复用。
// 所有 AI 客户端共享同一个 Transport，减少 TCP 连接和 TLS 握手开销。
var sharedTransport = &http.Transport{
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// Client 是 AI 客户端的结构体，封装了 OpenAI 客户端和相关配置。
type Client struct {
	config       *config.ModelConfig
	httpClient   *http.Client
	openaiClient *openai.Client
}

// ChatCompletionRequest 表示聊天完成请求。
//
// 它包含消息历史、流式传输标志、最大 token 数、温度、模型、工具等参数。
type ChatCompletionRequest struct {
	Messages    []openai.ChatCompletionMessage `json:"messages"`
	Stream      bool                           `json:"stream"`
	MaxTokens   int                            `json:"max_tokens"`
	Temperature float64                        `json:"temperature"`
	Model       string                         `json:"model"`
	Tools       []openai.Tool                  `json:"tools,omitempty"`
	ToolChoice  *string                        `json:"tool_choice,omitempty"`
}

// ChatCompletionResponse 表示聊天完成响应。
//
// 它包含生成的内容、使用统计信息、使用的模型和工具调用等。
type ChatCompletionResponse struct {
	Content   string            `json:"content"`
	Usage     openai.Usage      `json:"usage"`
	Model     string            `json:"model"`
	ToolCalls []openai.ToolCall `json:"tool_calls,omitempty"`
}

// StreamingChatCompletionHandler 定义了流式聊天完成的处理接口。
//
// 它提供了处理数据块、完成事件和错误的回调方法。
type StreamingChatCompletionHandler interface {
	// OnChunk 处理接收到的数据块。
	OnChunk(ctx context.Context, chunk string)

	// OnComplete 在流完成时调用。
	OnComplete(ctx context.Context, finalContent string, usage openai.Usage, model string)

	// OnError 在发生错误时调用。
	OnError(ctx context.Context, err error)
}

// NewClientWithModel 使用指定的模型配置创建一个新的 AI 客户端。
//
// 参数:
//   - cfg: 模型配置，包含模型、提供商、基础 URL、API 密钥等信息
//
// 返回值:
//   - *Client: 创建的客户端实例
//   - error: 创建过程中发生的错误
func NewClientWithModel(cfg *config.ModelConfig) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("model config is required")
	}

	httpClient := &http.Client{
		Timeout:   30 * time.Second,
		Transport: sharedTransport,
	}

	slog.Debug("创建AI客户端",
		"model", cfg.Model,
		"provider", cfg.Provider,
		"base_url", cfg.BaseURL)

	// 创建 OpenAI 客户端配置
	var clientConfig openai.ClientConfig

	// 处理 Azure OpenAI 提供商的特殊情况
	if cfg.Provider == "azure" || cfg.Provider == "azure-openai" {
		// 使用 Azure 配置
		clientConfig = openai.DefaultAzureConfig(cfg.APIKey, cfg.BaseURL)
	} else {
		// 使用标准 OpenAI 配置
		clientConfig = openai.DefaultConfig(cfg.APIKey)
		if cfg.BaseURL != "" {
			clientConfig.BaseURL = cfg.BaseURL
		}
	}

	// 设置 HTTP 客户端
	clientConfig.HTTPClient = httpClient

	// 创建 OpenAI 客户端
	openaiClient := openai.NewClientWithConfig(clientConfig)

	return &Client{
		config:       cfg,
		httpClient:   httpClient,
		openaiClient: openaiClient,
	}, nil
}

// CreateChatCompletion 创建聊天完成。
//
// 它支持流式和非流式两种模式。对于非流式请求，返回完整的响应。
// 对于流式请求，它会收集所有内容并返回最终结果。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - req: 聊天完成请求
//
// 返回值:
//   - *ChatCompletionResponse: 聊天完成响应
//   - error: 操作过程中发生的错误
func (c *Client) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	slog.Debug("开始AI请求",
		"model", req.Model,
		"stream", req.Stream,
		"messages_count", len(req.Messages),
		"max_tokens", req.MaxTokens,
		"temperature", req.Temperature)

	if !req.Stream {
		resp, err := c.openaiClient.CreateChatCompletion(
			ctx,
			openai.ChatCompletionRequest{
				Model:       req.Model,
				Messages:    req.Messages,
				MaxTokens:   req.MaxTokens,
				Temperature: float32(req.Temperature),
				Tools:       req.Tools,
				ToolChoice:  req.ToolChoice,
			},
		)
		if err != nil {
			slog.Error("AI请求失败", "model", req.Model, "error", err)
			return nil, fmt.Errorf("failed to create chat completion: %w", err)
		}

		if len(resp.Choices) == 0 {
			slog.Error("AI响应无内容", "model", req.Model)
			return nil, fmt.Errorf("no choices returned from API")
		}

		slog.Debug("AI响应成功",
			"model", resp.Model,
			"content_length", len(resp.Choices[0].Message.Content),
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens,
			"total_tokens", resp.Usage.TotalTokens)

		return &ChatCompletionResponse{
			Content:   resp.Choices[0].Message.Content,
			Usage:     resp.Usage,
			Model:     resp.Model,
			ToolCalls: resp.Choices[0].Message.ToolCalls,
		}, nil
	}

	slog.Debug("开始流式AI请求", "model", req.Model)
	stream, err := c.openaiClient.CreateChatCompletionStream(
		ctx,
		openai.ChatCompletionRequest{
			Model:       req.Model,
			Messages:    req.Messages,
			MaxTokens:   req.MaxTokens,
			Temperature: float32(req.Temperature),
			Stream:      true,
			Tools:       req.Tools,
			ToolChoice:  req.ToolChoice,
		},
	)
	if err != nil {
		slog.Error("创建流式请求失败", "model", req.Model, "error", err)
		return nil, fmt.Errorf("failed to create chat completion stream: %w", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			slog.Debug("Failed to close stream", "error", closeErr)
		}
	}()

	var builder strings.Builder
	builder.Grow(2048)
	var usage openai.Usage
	var model string
	var chunkCount int

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Error("流式响应错误", "model", req.Model, "error", err)
			return nil, fmt.Errorf("error in stream: %w", err)
		}

		if len(response.Choices) > 0 {
			builder.WriteString(response.Choices[0].Delta.Content)
			chunkCount++
		}

		if response.Usage != nil {
			usage = *response.Usage
		}
		if response.Model != "" {
			model = response.Model
		}
	}

	fullContent := builder.String()

	slog.Debug("流式AI响应完成",
		"model", model,
		"content_length", len(fullContent),
		"chunks", chunkCount,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"total_tokens", usage.TotalTokens)

	return &ChatCompletionResponse{
		Content: fullContent,
		Usage:   usage,
		Model:   model,
	}, nil
}

// CreateStreamingChatCompletion 创建流式聊天完成。
//
// 它使用提供的处理器来处理流式数据块、完成事件和错误。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - req: 聊天完成请求
//   - handler: 流式处理接口
//
// 返回值:
//   - error: 操作过程中发生的错误
func (c *Client) CreateStreamingChatCompletion(
	ctx context.Context,
	req ChatCompletionRequest,
	handler StreamingChatCompletionHandler,
) error {
	slog.Debug("开始回调式流式AI请求",
		"model", req.Model,
		"messages_count", len(req.Messages),
		"max_tokens", req.MaxTokens,
		"temperature", req.Temperature)

	stream, err := c.openaiClient.CreateChatCompletionStream(
		ctx,
		openai.ChatCompletionRequest{
			Model:       req.Model,
			Messages:    req.Messages,
			MaxTokens:   req.MaxTokens,
			Temperature: float32(req.Temperature),
			Stream:      true,
			Tools:       req.Tools,
			ToolChoice:  req.ToolChoice,
		},
	)
	if err != nil {
		slog.Error("创建回调式流式请求失败", "model", req.Model, "error", err)
		handler.OnError(ctx, fmt.Errorf("failed to create chat completion stream: %w", err))
		return err
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			slog.Debug("Failed to close stream", "error", closeErr)
		}
	}()

	var builder strings.Builder
	builder.Grow(2048)
	var usage openai.Usage
	var model string
	var chunkCount int

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			fullContent := builder.String()
			slog.Debug("回调式流式AI响应完成",
				"model", model,
				"content_length", len(fullContent),
				"chunks", chunkCount,
				"prompt_tokens", usage.PromptTokens,
				"completion_tokens", usage.CompletionTokens,
				"total_tokens", usage.TotalTokens)
			handler.OnComplete(ctx, fullContent, usage, model)
			return nil
		}
		if err != nil {
			slog.Error("回调式流式响应错误", "model", req.Model, "error", err)
			handler.OnError(ctx, fmt.Errorf("error in stream: %w", err))
			return err
		}

		if len(response.Choices) > 0 && response.Choices[0].Delta.Content != "" {
			chunk := response.Choices[0].Delta.Content
			builder.WriteString(chunk)
			chunkCount++
			handler.OnChunk(ctx, chunk)
		}

		if response.Usage != nil {
			usage = *response.Usage
		}
		if response.Model != "" {
			model = response.Model
		}
	}
}
