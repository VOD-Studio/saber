//go:build goolm

// Package mcp 提供 MCP (Model Context Protocol) 集成测试辅助函数。
package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	appcontext "rua.plus/saber/internal/context"
)

// MockToolResult 定义模拟工具的返回结果。
type MockToolResult struct {
	// Content 是工具返回的内容列表。
	Content []any
	// IsError 指示结果是否为错误。
	IsError bool
}

// MockTool 定义模拟 MCP 工具。
type MockTool struct {
	// Name 是工具名称。
	Name string
	// Description 是工具描述。
	Description string
	// InputSchema 是工具的输入参数 schema。
	InputSchema map[string]any
	// Handler 是工具的处理函数。
	Handler func(ctx context.Context, args map[string]any) (*MockToolResult, error)
}

// MockMCPServer 提供用于测试的模拟 MCP 服务器。
//
// 它实现了 MCP 服务器的核心功能，包括工具注册、列表和调用，
// 但不依赖外部服务。适用于单元测试和集成测试。
type MockMCPServer struct {
	mu     sync.RWMutex
	tools  map[string]*MockTool
	config *config.MCPConfig
}

// NewMockMCPServer 创建新的模拟 MCP 服务器。
//
// 返回配置好的 MockMCPServer 实例，可用于测试。
func NewMockMCPServer() *MockMCPServer {
	return &MockMCPServer{
		tools: make(map[string]*MockTool),
	}
}

// NewMockMCPServerWithConfig 创建带有配置的模拟 MCP 服务器。
//
// 参数:
//   - cfg: MCP 配置
//
// 返回配置好的 MockMCPServer 实例。
func NewMockMCPServerWithConfig(cfg *config.MCPConfig) *MockMCPServer {
	return &MockMCPServer{
		tools:  make(map[string]*MockTool),
		config: cfg,
	}
}

// RegisterTool 注册模拟工具。
//
// 参数:
//   - tool: 要注册的模拟工具
func (m *MockMCPServer) RegisterTool(tool *MockTool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[tool.Name] = tool
}

// UnregisterTool 移除已注册的工具。
//
// 参数:
//   - name: 要移除的工具名称
func (m *MockMCPServer) UnregisterTool(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tools, name)
}

// GetTool 获取指定名称的工具。
//
// 如果工具不存在，返回 nil。
func (m *MockMCPServer) GetTool(name string) *MockTool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tools[name]
}

// ListTools 返回所有已注册的工具列表。
func (m *MockMCPServer) ListTools() []*MockTool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools := make([]*MockTool, 0, len(m.tools))
	for _, tool := range m.tools {
		tools = append(tools, tool)
	}
	return tools
}

// ListToolsAsMCP 返回 MCP SDK 格式的工具列表。
//
// 用于与 Manager.ListTools 的返回格式兼容。
func (m *MockMCPServer) ListToolsAsMCP() []*mcp.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tools := make([]*mcp.Tool, 0, len(m.tools))
	for _, tool := range m.tools {
		tools = append(tools, &mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}
	return tools
}

// CallTool 调用指定的模拟工具。
//
// 参数:
//   - ctx: 上下文
//   - name: 工具名称
//   - args: 工具参数
//
// 如果工具不存在，返回错误。
func (m *MockMCPServer) CallTool(ctx context.Context, name string, args map[string]any) (*MockToolResult, error) {
	m.mu.RLock()
	tool, ok := m.tools[name]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}

	if tool.Handler == nil {
		// 默认返回成功结果
		return &MockToolResult{
			Content: []any{map[string]any{
				"type": "text",
				"text": fmt.Sprintf("Mock tool %s executed successfully", name),
			}},
			IsError: false,
		}, nil
	}

	return tool.Handler(ctx, args)
}

// Clear 清除所有已注册的工具。
func (m *MockMCPServer) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools = make(map[string]*MockTool)
}

// ToolCount 返回已注册的工具数量。
func (m *MockMCPServer) ToolCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tools)
}

// MockManager 提供用于测试的模拟 MCP Manager。
//
// 它封装了 Manager 的核心功能，但使用 MockMCPServer 作为后端，
// 适用于不需要真实 MCP 连接的测试场景。
type MockManager struct {
	mu          sync.RWMutex
	server      *MockMCPServer
	enabled     bool
	userContext map[id.UserID]id.RoomID
}

// NewMockManager 创建新的模拟 MCP 管理器。
//
// 参数:
//   - enabled: 是否启用 MCP 功能
//
// 返回配置好的 MockManager 实例。
func NewMockManager(enabled bool) *MockManager {
	return &MockManager{
		server:      NewMockMCPServer(),
		enabled:     enabled,
		userContext: make(map[id.UserID]id.RoomID),
	}
}

// IsEnabled 检查 MCP 功能是否启用。
func (m *MockManager) IsEnabled() bool {
	return m.enabled
}

// SetEnabled 设置 MCP 功能的启用状态。
func (m *MockManager) SetEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
}

// GetServer 获取底层的 MockMCPServer。
func (m *MockManager) GetServer() *MockMCPServer {
	return m.server
}

// RegisterTool 在模拟服务器上注册工具。
func (m *MockManager) RegisterTool(tool *MockTool) {
	m.server.RegisterTool(tool)
}

// ListTools 返回所有已注册的工具列表。
func (m *MockManager) ListTools() []*mcp.Tool {
	return m.server.ListToolsAsMCP()
}

// ListServers 返回模拟的服务器信息列表。
func (m *MockManager) ListServers() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 返回一个模拟服务器
	return []ServerInfo{
		{Name: "mock-server", Type: "mock", Enabled: m.enabled},
	}
}

// CallTool 使用用户上下文调用指定的 MCP 工具。
//
// 它模拟 Manager.CallTool 的行为，包括用户上下文验证。
func (m *MockManager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (any, error) {
	if !m.enabled {
		return nil, fmt.Errorf("MCP 功能未启用")
	}

	// 提取用户上下文
	userID, ok := appcontext.GetUserFromContext(ctx)
	if !ok || userID == "" {
		return nil, fmt.Errorf("缺少用户上下文：userID 必须通过 WithUserContext 设置")
	}
	roomID, ok := appcontext.GetRoomFromContext(ctx)
	if !ok || roomID == "" {
		return nil, fmt.Errorf("缺少用户上下文：roomID 必须通过 WithUserContext 设置")
	}

	// 调用模拟工具
	result, err := m.server.CallTool(ctx, toolName, args)
	if err != nil {
		return nil, fmt.Errorf("调用工具 %s 失败: %w", toolName, err)
	}

	return result, nil
}

// TestFixtures 提供常用的测试工具定义。
var TestFixtures = struct {
	// EchoTool 是一个简单的回显工具。
	EchoTool *MockTool
	// ErrorTool 是一个总是返回错误的工具。
	ErrorTool *MockTool
	// SlowTool 是一个模拟延迟的工具。
	SlowTool *MockTool
	// ContextTool 是一个返回上下文信息的工具。
	ContextTool *MockTool
}{
	EchoTool: &MockTool{
		Name:        "echo",
		Description: "返回输入的消息",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "要回显的消息",
				},
			},
			"required": []string{"message"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*MockToolResult, error) {
			message, _ := args["message"].(string)
			return &MockToolResult{
				Content: []any{map[string]any{
					"type": "text",
					"text": message,
				}},
				IsError: false,
			}, nil
		},
	},
	ErrorTool: &MockTool{
		Name:        "error_tool",
		Description: "总是返回错误的工具",
		InputSchema: map[string]any{
			"type": "object",
		},
		Handler: func(ctx context.Context, args map[string]any) (*MockToolResult, error) {
			return nil, fmt.Errorf("intentional error for testing")
		},
	},
	SlowTool: &MockTool{
		Name:        "slow_tool",
		Description: "模拟慢速响应的工具",
		InputSchema: map[string]any{
			"type": "object",
		},
		Handler: func(ctx context.Context, args map[string]any) (*MockToolResult, error) {
			// 模拟延迟（实际测试中应使用可控制的延迟）
			return &MockToolResult{
				Content: []any{map[string]any{
					"type": "text",
					"text": "slow response",
				}},
				IsError: false,
			}, nil
		},
	},
	ContextTool: &MockTool{
		Name:        "context_tool",
		Description: "返回调用上下文信息的工具",
		InputSchema: map[string]any{
			"type": "object",
		},
		Handler: func(ctx context.Context, args map[string]any) (*MockToolResult, error) {
			userID, _ := appcontext.GetUserFromContext(ctx)
			roomID, _ := appcontext.GetRoomFromContext(ctx)

			return &MockToolResult{
				Content: []any{map[string]any{
					"type": "text",
					"text": fmt.Sprintf("User: %s, Room: %s", userID, roomID),
				}},
				IsError: false,
			}, nil
		},
	},
}

// NewTestMCPServerWithFixtures 创建包含常用测试工具的模拟服务器。
//
// 参数:
//   - tools: 要注册的工具列表，如果为空则注册所有默认工具
//
// 返回配置好的 MockMCPServer 实例。
func NewTestMCPServerWithFixtures(tools ...*MockTool) *MockMCPServer {
	server := NewMockMCPServer()

	if len(tools) == 0 {
		// 注册所有默认工具
		server.RegisterTool(TestFixtures.EchoTool)
		server.RegisterTool(TestFixtures.ErrorTool)
		server.RegisterTool(TestFixtures.SlowTool)
		server.RegisterTool(TestFixtures.ContextTool)
	} else {
		for _, tool := range tools {
			server.RegisterTool(tool)
		}
	}

	return server
}

// NewTestUserContext 创建用于测试的用户上下文。
//
// 参数:
//   - userNum: 用户序号（用于生成测试 ID）
//   - roomNum: 房间序号（用于生成测试 ID）
//
// 返回带有用户上下文的 context.Context。
func NewTestUserContext(userNum, roomNum int) context.Context {
	ctx := context.Background()
	userID := id.UserID(fmt.Sprintf("@user%d:example.com", userNum))
	roomID := id.RoomID(fmt.Sprintf("!room%d:example.com", roomNum))
	return appcontext.WithUserContext(ctx, userID, roomID)
}

// NewTestMCPServerConfig 创建用于测试的 MCP 配置。
//
// 参数:
//   - enabled: 是否启用 MCP
//
// 返回配置好的 MCPConfig 实例。
func NewTestMCPServerConfig(enabled bool) *config.MCPConfig {
	return &config.MCPConfig{
		Enabled: enabled,
		Servers: make(map[string]config.ServerConfig),
	}
}
