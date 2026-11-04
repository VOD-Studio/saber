package ai

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/chat"
)

// emojiToolName 是表情目录与收藏工具的稳定名称；它也登记在内置聊天工具集合中，
// 因此纯聊天计划可以冻结并携带它，而不会因此申请执行授权。
const emojiToolName = "saber_emoji"

const (
	// emojiDefaultLimit 是单次列出表情的默认条数。
	emojiDefaultLimit = 30
	// emojiMaxLimit 是单次列出表情的硬上限，防止目录把对话预算挤爆。
	emojiMaxLimit = 100
	// emojiCustomGroupLabel 是自定义表情在列表里的展示分组名，也参与 group 过滤。
	emojiCustomGroupLabel = "自定义表情"
)

// emojiReplyInstruction 是普通 Violet 聊天回合追加到系统提示末尾的表情使用说明。
//
// 表情只能靠 token 发送，而模型看不到未收藏的自定义表情；把「先查、再收藏、
// 原文照抄」写成明确步骤，比让它凭印象拼一个 [doge] 更可靠。
const emojiReplyInstruction = `当回复里适合用表情时，先调用 saber_emoji 查询或确认表情 token，再把返回的 token 原样写进回复正文。
系统表情可以直接使用；自定义表情必须先 saber_emoji favorite，未收藏的 token 不会被渲染。
绝不臆造 token，也不要改写 token 的形状或加空格。`

// emojiTool 返回 saber_emoji 的工具定义。
//
// 动作保持最小：查询目录、收藏、取消收藏。发送表情不单独建动作——token 写进
// 普通回复正文即可，平台负责校验与渲染，新增消息类型只会让两端先坏。
func emojiTool() openai.Tool {
	return openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{
		Name: emojiToolName,
		Description: "查询并使用 Violet 站内表情。表情 token 是发送表情的唯一方式：把工具返回的 token 原样写进回复正文，不要改写、加空格或臆造。" +
			"list 返回系统表情分组与 bot 已收藏的自定义表情，可按 query/group 过滤。" +
			"favorite/unfavorite 收藏或取消收藏自定义表情，传 emoji_id 或看到过的 [name:uuid] token。" +
			"自定义表情默认私有，必须先 favorite 才能使用；系统表情无需收藏。不确定时先 list 查询，绝不臆造 token。",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":   map[string]any{"type": "string", "enum": []string{"list", "favorite", "unfavorite"}, "description": "要执行的动作"},
				"query":    map[string]any{"type": "string", "description": "list：按表情名称、token 或分组名过滤，大小写不敏感"},
				"group":    map[string]any{"type": "string", "description": "list：只保留分组名包含该值的表情"},
				"emoji_id": map[string]any{"type": "string", "description": "favorite/unfavorite：自定义表情 UUID"},
				"token":    map[string]any{"type": "string", "description": "favorite/unfavorite：自定义表情 token，形如 [name:uuid]"},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": emojiMaxLimit, "description": "list：返回条数，默认 30，最多 100"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}}
}

// emojiToolAvailable 判断当前身份是否可以看见 saber_emoji。
//
// 只需要「Violet 账号身份 + 平台启用 + 账号匹配 + 平台实现表情端口」：
// 使用表情不会公开内容，也没有工作区副作用，因此不要求管理员授权。
// 执行阶段会再次调用本方法，旧工具快照不能绕过之后关闭的开关或换掉的账号。
func (s *Service) emojiToolAvailable(ctx context.Context) bool {
	if s == nil || s.emojiAdapter == nil || s.config == nil {
		return false
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok || identity.Session.Platform != "violet" {
		return false
	}
	violet := s.config.Platforms.Violet
	if !violet.Enabled {
		return false
	}
	return identity.Session.Account == violet.EffectiveAccount()
}

// executeEmojiTool 执行 saber_emoji。
//
// 与推文工具的差别：这里没有写公开内容的动作，收藏只是 bot 自己的可见性，
// 因此不要求 commands.admins；但公开互动回合仍然整体拒绝，保持入口一致。
func (s *Service) executeEmojiTool(ctx context.Context, args map[string]any) (any, error) {
	if isPublicThread(ctx) {
		return nil, errors.New("公开互动回合不允许调用工具")
	}
	if !s.emojiToolAvailable(ctx) {
		return nil, errors.New("表情工具当前不可用：需要启用 platforms.violet，并使用与配置匹配的 Violet 账号身份")
	}
	action := stringArg(args, "action")
	switch action {
	case "list":
		return s.emojiList(ctx, args)
	case "favorite", "unfavorite":
		return s.emojiFavorite(ctx, action, args)
	default:
		return nil, fmt.Errorf("未知的表情动作 %q", action)
	}
}

// emojiList 读取目录并按 query/group 过滤，把 token 与分组渲染成紧凑文本。
func (s *Service) emojiList(ctx context.Context, args map[string]any) (any, error) {
	limit, err := emojiLimit(args)
	if err != nil {
		return nil, err
	}
	catalog, err := s.emojiAdapter.ListEmojis(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取 Violet 表情目录失败: %w", err)
	}
	query := strings.ToLower(stringArg(args, "query"))
	groupFilter := strings.ToLower(stringArg(args, "group"))
	var builder strings.Builder
	shown := 0
	for _, group := range catalog.Groups {
		if groupFilter != "" && !strings.Contains(strings.ToLower(group.Name), groupFilter) {
			continue
		}
		var lines []string
		for _, emoji := range group.Emojis {
			if shown >= limit {
				break
			}
			if !emojiMatches(query, emoji.Name, emoji.Token, group.Name, group.Type) {
				continue
			}
			lines = append(lines, "- "+emoji.Name+" token="+emoji.Token)
			shown++
		}
		if len(lines) == 0 {
			continue
		}
		if builder.Len() == 0 {
			builder.WriteString("系统表情：\n")
		}
		builder.WriteString("[" + group.Name + "]\n")
		for _, line := range lines {
			builder.WriteString(line + "\n")
		}
	}
	var customLines []string
	for _, custom := range catalog.Custom {
		if shown >= limit {
			break
		}
		if groupFilter != "" && !strings.Contains(strings.ToLower(emojiCustomGroupLabel), groupFilter) && !strings.Contains(strings.ToLower(custom.Source), groupFilter) {
			continue
		}
		if !emojiMatches(query, custom.Name, custom.Token, emojiCustomGroupLabel, custom.Source) {
			continue
		}
		customLines = append(customLines, "- "+custom.Name+" token="+custom.Token+" source="+custom.Source)
		shown++
	}
	if len(customLines) > 0 {
		builder.WriteString("我的自定义表情：\n")
		for _, line := range customLines {
			builder.WriteString(line + "\n")
		}
	}
	if shown == 0 {
		return "没有找到匹配的表情。请换一个关键词或 group 再试；不传 query/group 会列出全部表情。不要臆造 token。", nil
	}
	builder.WriteString("用法：把 token 原样写进回复正文即可发送表情；系统表情无需收藏，自定义表情需先 favorite；不要臆造 token。")
	return builder.String(), nil
}

// emojiFavorite 收藏或取消收藏一个自定义表情。
//
// 只接受 emoji_id 或 [name:uuid] token：按表情名字收藏会命中多个同名表情，
// 让模型猜 ID 与让服务端回 400 没有区别。
func (s *Service) emojiFavorite(ctx context.Context, action string, args map[string]any) (any, error) {
	id, err := emojiTargetID(args)
	if err != nil {
		return nil, err
	}
	if action == "favorite" {
		if err := s.emojiAdapter.FavoriteEmoji(ctx, id); err != nil {
			return nil, fmt.Errorf("收藏自定义表情失败: %w", err)
		}
		return fmt.Sprintf("已收藏自定义表情 %s；现在可以把它的 token 原样写进回复。", id), nil
	}
	if err := s.emojiAdapter.UnfavoriteEmoji(ctx, id); err != nil {
		return nil, fmt.Errorf("取消收藏自定义表情失败: %w", err)
	}
	return fmt.Sprintf("已取消收藏自定义表情 %s。", id), nil
}

// emojiTargetID 解析收藏目标：emoji_id 优先，其次取 token 最后一个冒号后的 UUID。
func emojiTargetID(args map[string]any) (string, error) {
	if raw := stringArg(args, "emoji_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("emoji_id 必须是自定义表情 UUID，收到 %q", raw)
		}
		return id.String(), nil
	}
	token := stringArg(args, "token")
	if token == "" {
		return "", errors.New("favorite/unfavorite 需要 emoji_id 或 [name:uuid] 形式的 token")
	}
	colon := strings.LastIndex(token, ":")
	if colon < 0 {
		return "", fmt.Errorf("token %q 是系统表情，无需收藏；收藏只接受 [name:uuid] 形式的自定义表情", token)
	}
	id, err := uuid.Parse(strings.TrimSuffix(token[colon+1:], "]"))
	if err != nil {
		return "", fmt.Errorf("token %q 里没有有效的自定义表情 UUID", token)
	}
	return id.String(), nil
}

// emojiMatches 判断一个条目是否命中关键词；空关键词视为全部命中。
func emojiMatches(query string, fields ...string) bool {
	if query == "" {
		return true
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// emojiLimit 解析列出条数：缺省 30，超过上限按上限截断。
func emojiLimit(args map[string]any) (int, error) {
	value, ok := args["limit"]
	if !ok || value == nil {
		return emojiDefaultLimit, nil
	}
	var limit int
	switch typed := value.(type) {
	case int:
		limit = typed
	case int64:
		limit = int(typed)
	case float64:
		limit = int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, fmt.Errorf("limit 必须是整数: %w", err)
		}
		limit = parsed
	default:
		return 0, errors.New("limit 必须是整数")
	}
	if limit <= 0 {
		return emojiDefaultLimit, nil
	}
	if limit > emojiMaxLimit {
		return emojiMaxLimit, nil
	}
	return limit, nil
}
