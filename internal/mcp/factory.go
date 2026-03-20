// Package mcp 提供 MCP (Model Context Protocol) 集成功能。
package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/mcp/servers"
)

// MCPServerFactory 定义 MCP 服务器创建工厂接口。
//
// 实现此接口的类型可以创建特定类型的 MCP 服务器连接。
// 使用工厂模式可以轻松扩展支持新的服务器类型，
// 而无需修改 Manager 的核心逻辑。
type MCPServerFactory interface {
	// Create 创建并连接 MCP 服务器。
	//
	// 参数:
	//   - ctx: 上下文，用于控制连接超时和取消
	//   - name: 服务器名称（用于日志和标识）
	//   - cfg: 服务器配置
	//
	// 返回值:
	//   - *mcp.Client: MCP 客户端实例
	//   - *mcp.ClientSession: 客户端会话
	//   - error: 创建过程中的错误
	Create(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error)

	// Type 返回此工厂支持的服务器类型标识符。
	//
	// 返回值应与 config.ServerConfig.Type 字段匹配，
	// 例如 "builtin"、"stdio"、"http"。
	Type() string
}

// BuiltinFactory 创建内置 MCP 服务器的工厂。
//
// 内置服务器不依赖外部进程或网络连接，
// 直接在进程内运行，适用于内置工具如 web_fetch、web_search 等。
type BuiltinFactory struct {
	builtinCfg *config.BuiltinConfig
}

// NewBuiltinFactory 创建新的内置服务器工厂。
//
// 参数:
//   - builtinCfg: 内置工具的配置（如 web_search、js_sandbox 配置）
func NewBuiltinFactory(builtinCfg *config.BuiltinConfig) *BuiltinFactory {
	return &BuiltinFactory{builtinCfg: builtinCfg}
}

// Create 实现 MCPServerFactory 接口，创建内置 MCP 服务器。
func (f *BuiltinFactory) Create(ctx context.Context, name string, _ *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	return servers.CreateBuiltinServer(ctx, name, f.builtinCfg)
}

// Type 返回 "builtin"，表示这是内置服务器工厂。
func (f *BuiltinFactory) Type() string {
	return ServerTypeBuiltin
}

// StdioFactory 创建 stdio 类型 MCP 服务器的工厂。
//
// Stdio 服务器通过启动子进程并通过 stdin/stdout 进行 JSON-RPC 通信。
// 适用于本地 MCP 服务器实现。
type StdioFactory struct{}

// NewStdioFactory 创建新的 stdio 服务器工厂。
func NewStdioFactory() *StdioFactory {
	return &StdioFactory{}
}

// Create 实现 MCPServerFactory 接口，创建 stdio MCP 服务器。
func (f *StdioFactory) Create(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	return servers.CreateStdioServer(ctx, name, cfg)
}

// Type 返回 "stdio"，表示这是 stdio 服务器工厂。
func (f *StdioFactory) Type() string {
	return ServerTypeStdio
}

// HTTPFactory 创建 HTTP 类型 MCP 服务器的工厂。
//
// HTTP 服务器通过网络连接远程 MCP 服务，
// 使用 Bearer 令牌进行认证。
type HTTPFactory struct{}

// NewHTTPFactory 创建新的 HTTP 服务器工厂。
func NewHTTPFactory() *HTTPFactory {
	return &HTTPFactory{}
}

// Create 实现 MCPServerFactory 接口，创建 HTTP MCP 服务器。
func (f *HTTPFactory) Create(ctx context.Context, name string, cfg *config.ServerConfig) (*mcp.Client, *mcp.ClientSession, error) {
	return servers.CreateHTTPServer(ctx, name, cfg)
}

// Type 返回 "http"，表示这是 HTTP 服务器工厂。
func (f *HTTPFactory) Type() string {
	return ServerTypeHTTP
}

// DefaultFactories 返回默认的工厂集合。
//
// 包含内置、stdio 和 HTTP 三种类型的工厂。
// 参数 mcpCfg 用于为内置工厂提供配置。
func DefaultFactories(mcpCfg *config.MCPConfig) map[string]MCPServerFactory {
	var builtinCfg *config.BuiltinConfig
	if mcpCfg != nil {
		builtinCfg = &mcpCfg.Builtin
	}

	return map[string]MCPServerFactory{
		ServerTypeBuiltin: NewBuiltinFactory(builtinCfg),
		ServerTypeStdio:   NewStdioFactory(),
		ServerTypeHTTP:    NewHTTPFactory(),
	}
}
