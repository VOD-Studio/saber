package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
)

// Client 是 AI 客户端的结构体，封装了 OpenAI 客户端和相关配置。
type Client struct {
	config       *config.ModelConfig
	httpClient   *http.Client
	openaiClient *openai.Client
}

// ChatCompletionRequest 表示聊天完成请求。
//
// 它包含消息历史、流式传输标志、最大 token 数、温度和模型等参数。
type ChatCompletionRequest struct {
	Messages    []openai.ChatCompletionMessage `json:"messages"`
	Stream      bool                           `json:"stream"`
	MaxTokens   int                            `json:"max_tokens"`
	Temperature float64                        `json:"temperature"`
	Model       string                         `json:"model"`
}

// ChatCompletionResponse 表示聊天完成响应。
//
// 它包含生成的内容、使用统计信息和使用的模型。
type ChatCompletionResponse struct {
	Content string       `json:"content"`
	Usage   openai.Usage `json:"usage"`
	Model   string       `json:"model"`
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

	// 创建 HTTP 客户端
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

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
	if !req.Stream {
		// 非流式请求
		resp, err := c.openaiClient.CreateChatCompletion(
			ctx,
			openai.ChatCompletionRequest{
				Model:       req.Model,
				Messages:    req.Messages,
				MaxTokens:   req.MaxTokens,
				Temperature: float32(req.Temperature),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create chat completion: %w", err)
		}

		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("no choices returned from API")
		}

		return &ChatCompletionResponse{
			Content: resp.Choices[0].Message.Content,
			Usage:   resp.Usage,
			Model:   resp.Model,
		}, nil
	}

	// 流式请求 - 收集所有内容
	stream, err := c.openaiClient.CreateChatCompletionStream(
		ctx,
		openai.ChatCompletionRequest{
			Model:       req.Model,
			Messages:    req.Messages,
			MaxTokens:   req.MaxTokens,
			Temperature: float32(req.Temperature),
			Stream:      true,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create chat completion stream: %w", err)
	}
	defer stream.Close()

	var fullContent string
	var usage openai.Usage
	var model string

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// 流完成
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error in stream: %w", err)
		}

		if len(response.Choices) > 0 {
			fullContent += response.Choices[0].Delta.Content
		}

		// 获取最后一条消息的使用统计和模型信息
		if response.Usage != nil {
			usage = *response.Usage
		}
		if response.Model != "" {
			model = response.Model
		}
	}

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
	// 强制设置 Stream 为 true
	req.Stream = true

	stream, err := c.openaiClient.CreateChatCompletionStream(
		ctx,
		openai.ChatCompletionRequest{
			Model:       req.Model,
			Messages:    req.Messages,
			MaxTokens:   req.MaxTokens,
			Temperature: float32(req.Temperature),
			Stream:      true,
		},
	)
	if err != nil {
		handler.OnError(ctx, fmt.Errorf("failed to create chat completion stream: %w", err))
		return err
	}
	defer stream.Close()

	var fullContent string
	var usage openai.Usage
	var model string

	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// 流完成
			handler.OnComplete(ctx, fullContent, usage, model)
			return nil
		}
		if err != nil {
			handler.OnError(ctx, fmt.Errorf("error in stream: %w", err))
			return err
		}

		if len(response.Choices) > 0 && response.Choices[0].Delta.Content != "" {
			chunk := response.Choices[0].Delta.Content
			fullContent += chunk
			handler.OnChunk(ctx, chunk)
		}

		// 获取最后一条消息的使用统计和模型信息
		if response.Usage != nil {
			usage = *response.Usage
		}
		if response.Model != "" {
			model = response.Model
		}
	}
}
