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
	"maunium.net/go/mautrix/format"
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

// ParseMention 解析消息中的机器人提及并清理消息前缀。
//
// 该方法按照优先级顺序检测多种 mention 格式：
//  1. MSC 3952 结构化 mentions
//  2. HTML pills (Element Web)
//  3. 显示名称文本匹配
//  4. 用户 ID 文本匹配
//
// 参数:
//   - body: 消息的纯文本内容
//   - content: 消息的完整内容对象
//
// 返回:
//   - cleanedMsg: 清理后的消息内容（移除 mention 前缀）
//   - isMentioned: 是否提及了机器人
func (s *MentionService) ParseMention(body string, content *event.MessageEventContent) (cleanedMsg string, isMentioned bool) {
	// 处理 nil content 的情况
	if content == nil {
		return body, false
	}

	// 1. MSC 3952 结构化 mentions 检测
	if content.Mentions != nil && content.Mentions.Has(s.botID) {
		return s.StripMentionPrefix(body), true
	}

	// 2. HTML Pills 检测
	if content.Format == event.FormatHTML && content.FormattedBody != "" {
		_, mentions := format.HTMLToMarkdownFull(nil, content.FormattedBody)
		if mentions != nil && mentions.Has(s.botID) {
			return s.StripMentionPrefix(body), true
		}
	}

	// 3. 显示名称文本匹配
	if s.checkDisplayNameMention(body) {
		return s.stripDisplayNameMention(body), true
	}

	// 4. 用户 ID 文本匹配
	if s.checkUserIDMention(body) {
		return s.stripUserIDMention(body), true
	}

	// 未提及机器人
	return body, false
}

// StripMentionPrefix 移除消息开头的标准 mention 前缀。
//
// 该方法处理常见的 mention 前缀格式，如 "@bot hello" -> "hello"。
//
// 参数:
//   - msg: 原始消息内容
//
// 返回:
//   - 清理后的消息内容
func (s *MentionService) StripMentionPrefix(msg string) string {
	if len(msg) == 0 {
		return msg
	}

	originalMsg := msg // 保存原始消息用于返回

	// 移除开头的空格
	msg = strings.TrimSpace(msg)
	if len(msg) == 0 {
		return originalMsg // 如果只有空白，返回原始消息
	}

	// 尝试移除显示名称前缀
	if displayName := s.GetDisplayName(); displayName != "" {
		prefix := "@" + displayName
		if len(msg) >= len(prefix) && strings.EqualFold(msg[:len(prefix)], prefix) {
			remaining := strings.TrimSpace(msg[len(prefix):])
			if len(remaining) > 0 {
				return remaining
			}
		}
	}

	// 尝试移除用户 ID 前缀
	botIDStr := s.botID.String()
	if len(msg) >= len(botIDStr) && strings.EqualFold(msg[:len(botIDStr)], botIDStr) {
		remaining := strings.TrimSpace(msg[len(botIDStr):])
		if len(remaining) > 0 {
			return remaining
		}
	}

	// 如果没有匹配的前缀，返回原消息
	return msg
}

// checkDisplayNameMention 检查消息是否包含机器人的显示名称。
//
// 该方法执行不区分大小写的子字符串匹配。
//
// 参数:
//   - msg: 要检查的消息内容
//
// 返回:
//   - 如果消息包含显示名称则返回 true
func (s *MentionService) checkDisplayNameMention(msg string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.displayName == "" {
		return false
	}

	return strings.Contains(strings.ToLower(msg), strings.ToLower(s.displayName))
}

// stripDisplayNameMention 从消息中移除显示名称提及。
//
// 该方法查找并移除显示名称的第一次出现。
//
// 参数:
//   - msg: 包含显示名称的消息
//
// 返回:
//   - 移除显示名称后的消息
func (s *MentionService) stripDisplayNameMention(msg string) string {
	displayName := s.GetDisplayName()
	if displayName == "" {
		return msg
	}

	// 查找显示名称的位置（不区分大小写）
	lowerMsg := strings.ToLower(msg)
	lowerName := strings.ToLower(displayName)
	idx := strings.Index(lowerMsg, lowerName)
	if idx == -1 {
		return msg
	}

	// 移除匹配的部分
	result := msg[:idx] + msg[idx+len(displayName):]
	// 只在实际有内容时才修剪空白，避免将纯空白消息变成空字符串
	if strings.TrimSpace(result) == "" {
		return result
	}
	return strings.TrimSpace(result)
}

// checkUserIDMention 检查消息是否包含机器人的用户 ID。
//
// 该方法执行子字符串匹配。
//
// 参数:
//   - msg: 要检查的消息内容
//
// 返回:
//   - 如果消息包含用户 ID 则返回 true
func (s *MentionService) checkUserIDMention(msg string) bool {
	return strings.Contains(msg, s.botID.String())
}

// stripUserIDMention 从消息中移除用户 ID 提及。
//
// 该方法查找并移除用户 ID 的第一次出现。
//
// 参数:
//   - msg: 包含用户 ID 的消息
//
// 返回:
//   - 移除用户 ID 后的消息
func (s *MentionService) stripUserIDMention(msg string) string {
	botIDStr := s.botID.String()
	before, after, found := strings.Cut(msg, botIDStr)
	if !found {
		return msg
	}

	return strings.TrimSpace(before + after)
}
