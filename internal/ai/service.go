// Package ai 提供 AI 服务相关功能，包括对话管理、流式响应和工具调用。
//
// 该包封装了 OpenAI 兼容的 API 客户端，支持：
//   - 流式和非流式对话
//   - 上下文管理（每个房间独立的持久化对话历史）
//   - MCP 工具调用（让 AI 执行实际操作）
//   - 主动聊天功能（AI 驱动的主动消息）
//
// 主要组件：
//   - Service: AI 服务编排器，协调所有 AI 相关操作
//   - Client: OpenAI 兼容的 API 客户端
//   - ContextManager: 对话上下文管理器
//   - StreamHandler: 流式响应处理器
//   - ProactiveManager: 主动聊天管理器
//   - MessageBuilder: 消息构建器
//   - ResponseHandler: 响应处理器
//   - ToolExecutor: 工具执行器
package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
	"rua.plus/saber/internal/matrix"
	"rua.plus/saber/internal/mcp"
)

// PersonaService 定义人格服务接口。
// 用于获取房间的系统提示词（合并基础提示词和人格提示词）。
type PersonaService interface {
	GetSystemPrompt(roomID id.RoomID, basePrompt string) string
}

// Service 是 AI 服务的核心结构体。
type Service struct {
	// core 是共享核心逻辑。
	core *Core
	// matrixService 是 Matrix 命令服务，用于发送消息。
	matrixService *matrix.CommandService
	// contextManager 是对话上下文管理器。
	contextManager *ContextManager
	// mcpManager 是 MCP 管理器。
	mcpManager *mcp.Manager
	// mediaService 是媒体服务。
	mediaService *matrix.MediaService
	// personaService 是人格服务（可选字段）。
	personaService PersonaService
	// msgBuilder 是消息构建器。
	msgBuilder *MessageBuilder
	// respHandler 是响应处理器。
	respHandler *ResponseHandler
	// toolExecutor 是工具执行器。
	toolExecutor *ToolExecutor
}