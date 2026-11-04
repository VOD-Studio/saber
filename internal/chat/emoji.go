package chat

import "context"

// 表情契约是回复正文之外的一条轻量通道：平台提供系统表情目录与 bot 私有的
// 自定义表情收藏，模型只能靠 token 在正文里引用表情。这里的类型只描述接入端
// 与上层的边界，字段命名与平台原生 DTO 解耦。

// Emoji 是系统表情的读模型，token 是写进正文的唯一引用方式。
type Emoji struct {
	// ID 是平台表情 ID。
	ID string
	// Name 是表情名称，检索与展示都用它。
	Name string
	// Token 是写进正文的引用串，例如 [doge]。
	Token string
	// URL 是静态图片地址，可能为空。
	URL string
	// GIFURL 是动图地址，可能为空。
	GIFURL string
	// TextContent 是文字型表情的替代内容，图片表情为空。
	TextContent string
	// Size 是平台给出的展示尺寸，未提供时为 0。
	Size int
	// GroupType 是所属表情组的类型，取值由平台定义。
	GroupType string
}

// EmojiGroup 是系统表情分组。
type EmojiGroup struct {
	// ID 是分组 ID。
	ID string
	// Name 是分组名称，检索与展示都用它。
	Name string
	// Type 是分组类型，取值由平台定义。
	Type string
	// Emojis 是组内表情，顺序与平台给出的一致。
	Emojis []Emoji
}

// CustomEmoji 是 bot 自有或已收藏的自定义表情；未收藏的自定义表情不可使用。
type CustomEmoji struct {
	// ID 是自定义表情 ID，收藏接口用它定位。
	ID string
	// Name 是表情名称。
	Name string
	// Token 是写进正文的引用串，例如 [cat:uuid]。
	Token string
	// URL 是表情图片地址。
	URL string
	// Source 是来源：owned 表示 bot 自制，favorited 表示已收藏。
	Source string
}

// EmojiCatalog 是一次表情目录快照。
type EmojiCatalog struct {
	// Groups 是系统表情分组。
	Groups []EmojiGroup
	// Custom 是 bot 自制与已收藏的自定义表情。
	Custom []CustomEmoji
}

// EmojiAdapter 是表情目录与收藏的读写端口，由支持表情的平台实现。
//
// 收藏只影响 bot 自己的可见性：自定义表情默认私有，收藏后才能在下一条回复里
// 使用；系统表情无需收藏。正文里的 token 由平台负责校验、持久化与渲染。
type EmojiAdapter interface {
	// ListEmojis 读取系统表情与 bot 的收藏目录。
	ListEmojis(ctx context.Context) (EmojiCatalog, error)
	// FavoriteEmoji 收藏一个自定义表情；重复收藏是幂等操作。
	FavoriteEmoji(ctx context.Context, emojiID string) error
	// UnfavoriteEmoji 取消收藏一个自定义表情；取消后该 token 不能再使用。
	UnfavoriteEmoji(ctx context.Context, emojiID string) error
}
