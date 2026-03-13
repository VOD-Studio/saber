// Package matrix 提供基于 mautrix-go 的 Matrix 客户端封装。
package matrix

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// MentionService 管理机器人显示名称和 mention 检测。
//
// 它支持多种 mention 格式的检测：
//   - MSC 3952 结构化 mentions
//   - HTML pills (Element Web)
//   - 显示名称文本匹配
//   - 用户 ID 文本匹配
type MentionService struct {
	client      *mautrix.Client
	botID       id.UserID
	displayName string
	mu          sync.RWMutex
}

// NewMentionService 创建一个新的 MentionService 实例。
//
// 参数:
//   - client: Matrix 客户端实例
//   - botID: 机器人的用户 ID
//
// 返回:
//   - 初始化好的 MentionService 实例
func NewMentionService(client *mautrix.Client, botID id.UserID) *MentionService {
	return &MentionService{
		client: client,
		botID:  botID,
	}
}

// Init 从 Matrix 服务器获取机器人的显示名称。
//
// 该方法应在服务启动时调用一次，以缓存显示名称用于后续的 mention 检测。
//
// 参数:
//   - ctx: 上下文用于取消和超时控制
//
// 返回:
//   - 错误（如果获取失败）
func (s *MentionService) Init(ctx context.Context) error {
	profile, err := s.client.GetProfile(ctx, s.botID)
	if err != nil {
		return fmt.Errorf("failed to get profile: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.displayName = profile.DisplayName
	slog.Info("MentionService initialized", "display_name", s.displayName)
	return nil
}

// GetDisplayName 获取机器人的显示名称。
//
// 返回:
//   - 显示名称字符串（如果未初始化则返回空字符串）
func (s *MentionService) GetDisplayName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.displayName
}

// IsMentioned 检查消息内容是否提及了机器人。
//
// 它检测多种 mention 格式：
//   - Matrix ID 匹配（如 @bot:matrix.org）
//   - 显示名称匹配（不区分大小写）
//   - 未来的扩展：结构化 mentions 和 HTML pills
//
// 参数:
//   - content: 消息内容
//
// 返回:
//   - 如果消息提及了机器人则返回 true
func (s *MentionService) IsMentioned(content string) bool {
	if content == "" {
		return false
	}

	s.mu.RLock()
	displayName := s.displayName
	s.mu.RUnlock()

	// 检查 Matrix ID
	if strings.Contains(content, s.botID.String()) {
		return true
	}

	// 检查显示名称（不区分大小写）
	if displayName != "" && strings.Contains(strings.ToLower(content), strings.ToLower(displayName)) {
		return true
	}

	return false
}

// ParseMentions 从消息中提取提及的用户列表（未来扩展用）。
//
// 该方法预留用于解析 MSC 3952 结构化 mentions 和 HTML pills。
// 当前版本仅作为框架占位。
//
// 参数:
//   - evt: Matrix 房间消息事件
//
// 返回:
//   - 提及的用户 ID 列表
func (s *MentionService) ParseMentions(evt *event.Event) []id.UserID {
	// TODO: 实现结构化 mention 解析
	return []id.UserID{}
}
