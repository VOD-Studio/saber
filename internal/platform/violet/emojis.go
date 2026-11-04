package violetplatform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"rua.plus/saber/internal/chat"
)

// 编译期断言：Platform 同时提供表情目录与收藏端口。
var _ chat.EmojiAdapter = (*Platform)(nil)

// listBotEmojis 读取系统表情分组与 bot 私有收藏。
//
// 自定义表情默认私有，catalog 里的 custom 就是「本 bot 现在能用的全部自定义表情」，
// 没有全站目录可查。
func (c *client) listBotEmojis(ctx context.Context) (emojiCatalogDTO, error) {
	var out emojiCatalogDTO
	if err := c.request(ctx, http.MethodGet, "/emojis", nil, nil, "", &out); err != nil {
		return emojiCatalogDTO{}, err
	}
	return out, nil
}

// favoriteBotEmoji 收藏一个自定义表情，端点成功时返回 204。
func (c *client) favoriteBotEmoji(ctx context.Context, emojiID string) error {
	return c.request(ctx, http.MethodPost, emojiFavoritePath(emojiID), nil, nil, "", nil)
}

// unfavoriteBotEmoji 取消收藏一个自定义表情，端点成功时返回 204。
func (c *client) unfavoriteBotEmoji(ctx context.Context, emojiID string) error {
	return c.request(ctx, http.MethodDelete, emojiFavoritePath(emojiID), nil, nil, "", nil)
}

// emojiFavoritePath 拼收藏端点；ID 只做路径转义，格式由调用方先行校验。
func emojiFavoritePath(emojiID string) string {
	return "/emojis/" + url.PathEscape(emojiID) + "/favorite"
}

// ListEmojis 读取系统表情与 bot 的收藏目录。
func (p *Platform) ListEmojis(ctx context.Context) (chat.EmojiCatalog, error) {
	if p.api == nil {
		return chat.EmojiCatalog{}, errors.New("violet 平台缺少配置")
	}
	dto, err := p.api.listBotEmojis(ctx)
	if err != nil {
		return chat.EmojiCatalog{}, err
	}
	return emojiCatalog(dto), nil
}

// FavoriteEmoji 收藏一个自定义表情。
//
// 收藏只改变 bot 自己的可见性；ID 在本地先校验成 UUID，避免把一个必然 400 的
// 请求发到服务端后只拿到一句含糊的校验错误。
func (p *Platform) FavoriteEmoji(ctx context.Context, emojiID string) error {
	if p.api == nil {
		return errors.New("violet 平台缺少配置")
	}
	id, err := normalizeEmojiID(emojiID)
	if err != nil {
		return err
	}
	return p.api.favoriteBotEmoji(ctx, id)
}

// UnfavoriteEmoji 取消收藏一个自定义表情。
func (p *Platform) UnfavoriteEmoji(ctx context.Context, emojiID string) error {
	if p.api == nil {
		return errors.New("violet 平台缺少配置")
	}
	id, err := normalizeEmojiID(emojiID)
	if err != nil {
		return err
	}
	return p.api.unfavoriteBotEmoji(ctx, id)
}

// normalizeEmojiID 校验自定义表情 ID：Violet 的收藏端点按 UUID 定位。
func normalizeEmojiID(raw string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("自定义表情 ID 必须是 UUID，收到 %q", raw)
	}
	return id.String(), nil
}

// emojiCatalog 把平台 DTO 映射成平台无关契约；缺失的可选字段保持零值。
func emojiCatalog(dto emojiCatalogDTO) chat.EmojiCatalog {
	catalog := chat.EmojiCatalog{
		Groups: make([]chat.EmojiGroup, 0, len(dto.Groups)),
		Custom: make([]chat.CustomEmoji, 0, len(dto.Custom)),
	}
	for _, group := range dto.Groups {
		mapped := chat.EmojiGroup{
			ID:   emojiIDString(group.ID),
			Name: group.Name,
			Type: emojiGroupTypeLabel(group.Type),
		}
		for _, emoji := range group.Emojis {
			mapped.Emojis = append(mapped.Emojis, chat.Emoji{
				ID:          emojiIDString(emoji.ID),
				Name:        emoji.Name,
				Token:       emoji.Token,
				URL:         emoji.URL,
				GIFURL:      emoji.GIFURL,
				TextContent: emoji.TextContent,
				Size:        emojiMetaSize(emoji.Meta),
				GroupType:   mapped.Type,
			})
		}
		catalog.Groups = append(catalog.Groups, mapped)
	}
	for _, custom := range dto.Custom {
		catalog.Custom = append(catalog.Custom, chat.CustomEmoji{
			ID:     custom.ID,
			Name:   custom.Name,
			Token:  custom.Token,
			URL:    custom.URL,
			Source: custom.Source,
		})
	}
	return catalog
}

// emojiIDString 把平台数字主键转成契约里的字符串 ID。
//
// Violet 的 EmojiGroupDTO/EmojiDTO 用数字主键，而 chat.* 契约面向多平台，
// 数字字符串是最不意外的公共表示。
func emojiIDString(id int32) string {
	return strconv.Itoa(int(id))
}

// emojiGroupTypeLabel 把分组类型枚举翻译成可读标签。
//
// 目前只确认 1=文本、2=图片；未知取值保留数字串，既不丢信息也不臆造语义。
func emojiGroupTypeLabel(value int32) string {
	switch value {
	case 1:
		return "text"
	case 2:
		return "image"
	default:
		return strconv.Itoa(int(value))
	}
}

// emojiMetaSize 读取表情元数据里的尺寸。
//
// 该字段随前端形态变化（数字或数字字符串），解析失败按「未提供」处理，
// 尺寸只是展示建议，不该让整份目录读取失败。
func emojiMetaSize(meta *emojiMetaDTO) int {
	if meta == nil {
		return 0
	}
	switch typed := meta.Size.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return 0
}
