// Package mcp 提供 MCP (Model Context Protocol) 集成功能。
package mcp

import (
	"log/slog"
	"time"

	"maunium.net/go/mautrix/id"
)

// debugMode 控制是否启用详细日志记录。
var debugMode bool

// SetDebugMode 设置调试模式。
// 启用后会记录更详细的工具调用参数信息。
func SetDebugMode(enabled bool) {
	debugMode = enabled
}

// LogToolCall 记录工具调用。
//
// 记录工具名称、服务器名称、用户 ID、房间 ID 等基本信息。
// 在调试模式下，还会记录经过脱敏处理的参数。
func LogToolCall(serverName, toolName string, userID id.UserID, roomID id.RoomID, args map[string]any) {
	slog.Info("MCP tool call",
		"server", serverName,
		"tool", toolName,
		"user", userID,
		"room", roomID,
	)

	if debugMode && args != nil {
		slog.Debug("tool arguments", "args", sanitizeArgs(args))
	}
}

// LogToolResult 记录工具执行结果。
//
// 记录工具执行耗时和结果状态（成功/失败）。
// 失败时会记录错误信息。
func LogToolResult(serverName, toolName string, duration time.Duration, err error) {
	if err != nil {
		slog.Error("MCP tool failed",
			"server", serverName,
			"tool", toolName,
			"duration", duration,
			"error", err,
		)
		return
	}

	slog.Info("MCP tool success",
		"server", serverName,
		"tool", toolName,
		"duration", duration,
	)
}

// sanitizeArgs 移除参数中的敏感数据。
//
// 将密码、令牌、API 密钥等敏感字段替换为 [REDACTED]。
func sanitizeArgs(args map[string]any) map[string]any {
	sensitiveFields := []string{
		"password",
		"token",
		"api_key",
		"apikey",
		"secret",
		"authorization",
		"auth",
		"credential",
		"private_key",
		"privatekey",
		"access_token",
		"refresh_token",
	}

	result := make(map[string]any, len(args))

	for key, value := range args {
		isSensitive := false
		for _, sensitive := range sensitiveFields {
			if key == sensitive {
				isSensitive = true
				break
			}
		}

		if isSensitive {
			result[key] = "[REDACTED]"
		} else {
			result[key] = value
		}
	}

	return result
}
