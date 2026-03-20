// Package mcp 提供 MCP (Model Context Protocol) 集成功能。
package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/mcp/servers"
)

// contextKey 定义上下文键类型，避免键冲突。
type contextKey string

const (
	// UserContextKey 是上下文中用户 ID 的键。
	UserContextKey contextKey = "userID"
	// RoomContextKey 是上下文中房间 ID 的键。
	RoomContextKey contextKey = "roomID"
)

// ServerInfo 包含 MCP 服务器的信息。
type ServerInfo struct {
	Name    string
	Type    string
	Enabled bool
}

// Manager 管理所有 MCP 服务器连接和工具调用。
type Manager struct {
	mu             sync.RWMutex
	config         *config.MCPConfig
	clients        map[string]*mcp.Client
	sessions       map[string]*mcp.ClientSession
	rateLimiter    *RateLimiter
	enabled        bool
	toolCache      []*mcp.Tool
	toolCacheValid bool
	toolToServer   map[string]string
	factories      map[string]MCPServerFactory
}

// NewManager 创建新的 MCP 管理器。
func NewManager(cfg *config.MCPConfig) *Manager {
	return &Manager{
		config:       cfg,
		clients:      make(map[string]*mcp.Client),
		sessions:     make(map[string]*mcp.ClientSession),
		rateLimiter:  NewRateLimiter(60), // 默认每分钟 60 次
		enabled:      cfg != nil && cfg.Enabled,
		toolToServer: make(map[string]string),
		factories:    DefaultFactories(cfg),
	}
}

// NewManagerWithBuiltin 创建管理器并自动启用内置服务器。
// 即使 cfg 为 nil 或 MCP.Enabled 为 false，也会初始化内置工具。
func NewManagerWithBuiltin(cfg *config.MCPConfig) *Manager {
	mgr := &Manager{
		config:       cfg,
		clients:      make(map[string]*mcp.Client),
		sessions:     make(map[string]*mcp.ClientSession),
		rateLimiter:  NewRateLimiter(60),
		enabled:      true,
		toolToServer: make(map[string]string),
		factories:    DefaultFactories(cfg),
	}
	return mgr
}

// InitBuiltinServers 初始化所有内置 MCP 服务器。
// 此方法不依赖配置文件，直接启用所有内置工具。
func (m *Manager) InitBuiltinServers(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var builtinCfg config.BuiltinConfig
	if m.config != nil {
		builtinCfg = m.config.Builtin
	}

	for _, name := range servers.BuiltinServers {
		if _, exists := m.sessions[name]; exists {
			continue
		}

		client, session, err := servers.CreateBuiltinServer(ctx, name, &builtinCfg)
		if err != nil {
			slog.Error("创建内置 MCP 服务器失败", "server", name, "error", err)
			continue
		}

		m.clients[name] = client
		m.sessions[name] = session
		slog.Info("内置 MCP 服务器已连接", "server", name)
	}

	m.doRefreshToolCache()
	return nil
}

// RegisterFactory 注册自定义的服务器工厂。
//
// 如果已存在相同类型的工厂，新的工厂将覆盖旧的。
// 这允许用户自定义或扩展服务器创建逻辑。
func (m *Manager) RegisterFactory(factory MCPServerFactory) {
	if m.factories == nil {
		m.factories = make(map[string]MCPServerFactory)
	}
	m.factories[factory.Type()] = factory
}

// Init 初始化所有配置的 MCP 服务器。
//
// 它遍历配置中的所有服务器，根据类型使用对应的工厂创建连接，
// 并将客户端和会话存储在内部映射中。
// 单个服务器初始化失败不会阻止其他服务器的初始化。
func (m *Manager) Init(ctx context.Context) error {
	if !m.enabled {
		slog.Info("MCP 功能已禁用，跳过初始化")
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for name, serverCfg := range m.config.Servers {
		if !serverCfg.Enabled {
			slog.Debug("MCP 服务器已禁用，跳过", "server", name)
			continue
		}

		factory, ok := m.factories[serverCfg.Type]
		if !ok {
			slog.Error("未知的 MCP 服务器类型", "server", name, "type", serverCfg.Type)
			continue
		}

		client, session, err := factory.Create(ctx, name, &serverCfg)
		if err != nil {
			slog.Error("创建 MCP 服务器失败", "server", name, "error", err)
			continue
		}

		m.clients[name] = client
		m.sessions[name] = session
		slog.Info("MCP 服务器已连接", "server", name, "type", serverCfg.Type)
	}

	if len(m.config.Servers) == 0 {
		slog.Debug("未配置 MCP 服务器，跳过")
		return nil
	}

	if len(m.sessions) == 0 {
		slog.Warn("没有成功连接的 MCP 服务器，请检查服务器配置")
	}

	// 初始化工具缓存
	m.doRefreshToolCache()

	return nil
}

// Close 关闭所有 MCP 连接。
//
// 它关闭所有会话并清空内部映射。
// 关闭过程中遇到的错误会被记录但不会阻止其他会话的关闭。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var lastErr error

	for name, session := range m.sessions {
		if err := session.Close(); err != nil {
			slog.Error("关闭 MCP 会话失败", "server", name, "error", err)
			lastErr = err
		}
		slog.Debug("MCP 会话已关闭", "server", name)
	}

	// 清空映射
	m.clients = make(map[string]*mcp.Client)
	m.sessions = make(map[string]*mcp.ClientSession)

	slog.Info("所有 MCP 连接已关闭")
	return lastErr
}

// GetSession 获取指定名称的 MCP 会话。
//
// 如果会话不存在或 MCP 功能未启用，返回 nil。
func (m *Manager) GetSession(name string) *mcp.ClientSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.sessions[name]
}

// GetClient 获取指定名称的 MCP 客户端。
//
// 如果客户端不存在或 MCP 功能未启用，返回 nil。
func (m *Manager) GetClient(name string) *mcp.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.clients[name]
}

// ListServers 返回所有配置的 MCP 服务器信息。
func (m *Manager) ListServers() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var infos []ServerInfo
	if m.config == nil || m.config.Servers == nil {
		return infos
	}

	for name, serverCfg := range m.config.Servers {
		infos = append(infos, ServerInfo{
			Name:    name,
			Type:    serverCfg.Type,
			Enabled: serverCfg.Enabled,
		})
	}

	return infos
}

// IsEnabled 检查 MCP 功能是否启用。
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// WithUserContext 创建包含用户上下文的新上下文。
//
// 它将用户 ID 和房间 ID 存储在上下文中，用于后续的工具调用授权和审计。
func WithUserContext(ctx context.Context, userID id.UserID, roomID id.RoomID) context.Context {
	ctx = context.WithValue(ctx, UserContextKey, userID)
	ctx = context.WithValue(ctx, RoomContextKey, roomID)
	return ctx
}

// GetUserFromContext 从上下文中提取用户 ID。
//
// 如果上下文中不存在用户 ID，返回空字符串。
func GetUserFromContext(ctx context.Context) id.UserID {
	if userID, ok := ctx.Value(UserContextKey).(id.UserID); ok {
		return userID
	}
	return ""
}

// GetRoomFromContext 从上下文中提取房间 ID。
//
// 如果上下文中不存在房间 ID，返回空字符串。
func GetRoomFromContext(ctx context.Context) id.RoomID {
	if roomID, ok := ctx.Value(RoomContextKey).(id.RoomID); ok {
		return roomID
	}
	return ""
}

// CallTool 使用用户上下文调用指定的 MCP 工具。
//
// 它会验证用户上下文并检查速率限制。
// 如果速率限制超过或用户上下文无效，返回错误。
func (m *Manager) CallTool(ctx context.Context, serverName, toolName string, args map[string]any) (any, error) {
	if !m.enabled {
		return nil, fmt.Errorf("MCP 功能未启用")
	}

	// 提取用户上下文
	userID := GetUserFromContext(ctx)
	roomID := GetRoomFromContext(ctx)

	if userID == "" || roomID == "" {
		return nil, fmt.Errorf("缺少用户上下文：userID 和 roomID 必须通过 WithUserContext 设置")
	}

	// 检查速率限制
	if !m.rateLimiter.Allow(userID, roomID) {
		return nil, fmt.Errorf("速率限制超过：用户 %s 在房间 %s", userID, roomID)
	}

	// 获取会话
	session := m.GetSession(serverName)
	if session == nil {
		return nil, fmt.Errorf("MCP 服务器不存在或未连接: %s", serverName)
	}

	// 调用工具
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("调用工具 %s 失败: %w", toolName, err)
	}

	return result, nil
}

// ListTools 返回所有 MCP 服务器提供的工具列表。
//
// 如果缓存有效，直接返回缓存结果；否则刷新缓存后返回。
func (m *Manager) ListTools() []*mcp.Tool {
	m.mu.RLock()
	if m.toolCacheValid {
		defer m.mu.RUnlock()
		// 返回缓存的副本，避免外部修改
		result := make([]*mcp.Tool, len(m.toolCache))
		copy(result, m.toolCache)
		return result
	}
	m.mu.RUnlock()

	m.refreshToolCache()

	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*mcp.Tool, len(m.toolCache))
	copy(result, m.toolCache)
	return result
}

// GetServerForTool 返回提供指定工具的服务器名称。
//
// 如果工具不存在，返回空字符串。
func (m *Manager) GetServerForTool(toolName string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.toolCacheValid {
		m.mu.RUnlock()
		m.refreshToolCache()
		m.mu.RLock()
	}

	return m.toolToServer[toolName]
}

// refreshToolCache 刷新工具缓存。
//
// 它遍历所有已连接的 MCP 服务器，收集所有可用工具。
// 单个服务器获取工具列表失败不会阻止其他服务器的处理。
func (m *Manager) refreshToolCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.doRefreshToolCache()
}

// doRefreshToolCache 执行工具缓存刷新，调用者必须持有锁。
func (m *Manager) doRefreshToolCache() {
	var allTools []*mcp.Tool
	toolMap := make(map[string]string)

	for name, session := range m.sessions {
		resp, err := session.ListTools(context.Background(), nil)
		if err != nil {
			slog.Warn("获取工具列表失败", "server", name, "error", err)
			continue
		}
		allTools = append(allTools, resp.Tools...)
		for _, tool := range resp.Tools {
			toolMap[tool.Name] = name
		}
	}

	m.toolCache = allTools
	m.toolToServer = toolMap
	m.toolCacheValid = true
	slog.Debug("工具缓存已刷新", "tool_count", len(allTools), "mapping_count", len(toolMap))
}

// InvalidateToolCache 使工具缓存失效。
//
// 当 MCP 服务器配置变更或工具列表可能发生变化时应调用此方法。
func (m *Manager) InvalidateToolCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCacheValid = false
	slog.Debug("工具缓存已失效")
}
