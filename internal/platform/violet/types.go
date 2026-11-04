package violetplatform

import "encoding/json"

// Violet Bot API 的读模型。
//
// 字段只声明 Saber 真正用到的部分：Violet 的 DTO 是给人和 bot 共用的读模型，
// 会随前端需求增删，多余字段在此处忽略即可，反向要求字段稳定只会让接入端先坏。
// 命名与 JSON 键一一对应，避免映射层再解释一遍。

// botProfileDTO 对应 GET /chat/bot/profile。
type botProfileDTO struct {
	// ID 是 bot 凭证 ID。
	ID string `json:"id"`
	// UserID 是 bot 对应的虚拟用户 ID，入站消息的 sender.id 即此值。
	UserID string `json:"user_id"`
	// Username 是虚拟用户名，@提及 token 里的寻址值。
	Username string `json:"username"`
	// Name 是显示名。
	Name string `json:"name"`
}

// userDTO 是消息作者与成员的公开资料。
type userDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// messageDTO 对应消息快照：SSE 的 message.created 与历史接口共用同一形状。
type messageDTO struct {
	ID             string         `json:"id"`
	ConversationID string         `json:"conversation_id"`
	Sender         userDTO        `json:"sender"`
	Type           string         `json:"type"`
	Content        string         `json:"content"`
	SenderKind     string         `json:"sender_kind"`
	BotReply       *botReplyDTO   `json:"bot_reply,omitempty"`
	Mentions       map[string]any `json:"mentions"`
	ReplyTo        *messageRefDTO `json:"reply_to"`
	IsDeleted      bool           `json:"is_deleted"`
	EditedAt       string         `json:"edited_at,omitempty"`
	CreatedAt      string         `json:"created_at"`
	// Media 是图片消息携带的媒体定位信息，文本消息为空。
	Media []messageMediaDTO `json:"media,omitempty"`
	// CustomEmote 是正文内联自定义表情的快照，键为正文里出现的 [name:id] token。
	CustomEmote map[string]customEmoteDTO `json:"custom_emote,omitempty"`
}

// customEmoteDTO 是自定义表情的定位信息：正文只留 token，图片要靠这个 URL 下载。
type customEmoteDTO struct {
	URL           string `json:"url"`
	CustomEmojiID string `json:"custom_emoji_id"`
	Relation      string `json:"relation"`
}

// emojiMetaDTO 是表情的展示元数据；size 的形态跟随前端，保留原始值由映射层
// 归一化，缺字段时为零值。type 与 id 一样是数字枚举，不是可读字符串。
type emojiMetaDTO struct {
	Size any   `json:"size"`
	Type int32 `json:"type"`
}

// emojiDTO 对应 EmojiDTO：系统表情与分组内表情共用同一形状。
// id 是数字主键；token 才是写进正文的引用串。
type emojiDTO struct {
	ID          int32         `json:"id"`
	Name        string        `json:"name"`
	URL         string        `json:"url"`
	Token       string        `json:"token"`
	GIFURL      string        `json:"gif_url"`
	TextContent string        `json:"text_content"`
	Meta        *emojiMetaDTO `json:"meta"`
}

// emojiGroupDTO 对应 EmojiGroupDTO：系统表情按组下发，组内表情自带 token。
// id 与 type 都是数字枚举，type 的语义在映射层翻译成可读标签。
type emojiGroupDTO struct {
	ID     int32      `json:"id"`
	Name   string     `json:"name"`
	Type   int32      `json:"type"`
	Emojis []emojiDTO `json:"emojis"`
}

// customEmojiDTO 对应 bot 自有或已收藏的自定义表情；source 取 owned 或 favorited。
type customEmojiDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Token  string `json:"token"`
	URL    string `json:"url"`
	Source string `json:"source"`
}

// emojiCatalogDTO 对应 GET /chat/bot/emojis 的 data：系统分组与 bot 私有收藏。
type emojiCatalogDTO struct {
	Groups []emojiGroupDTO  `json:"groups"`
	Custom []customEmojiDTO `json:"custom"`
}

// messageMediaDTO 是消息内嵌的媒体快照，URL 供 saber 侧入站下载。
type messageMediaDTO struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	MIMEType string `json:"mime_type"`
	Width    *int   `json:"width,omitempty"`
	Height   *int   `json:"height,omitempty"`
}

// botReplyDTO 是 Violet 生成状态的读模型，revision 与写请求的递增版本一致。
type botReplyDTO struct {
	Status    string `json:"status"`
	Thinking  string `json:"thinking,omitempty"`
	Revision  int64  `json:"revision"`
	UpdatedAt string `json:"updated_at,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
}

type outgoingMessage struct {
	Content   string `json:"content"`
	ReplyToID string `json:"reply_to_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Revision  int64  `json:"revision,omitempty"`
	// Type 仅在图片消息时设为 "image"，留空时 Violet 侧回退 text。
	Type string `json:"type,omitempty"`
	// MediaIDs 是图片消息引用的上传 file_id 列表，type=image 时必填。
	MediaIDs []string `json:"media_ids,omitempty"`
}

// mediaUploadDTO 是 POST /chat/bot/media 的响应：上传后的文件定位信息。
// id 供后续 SendMessage 的 media_ids 引用；其余字段 violet 服务端返回，saber 暂未消费。
type mediaUploadDTO struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	MIMEType string `json:"mime_type"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Size     int64  `json:"size"`
}

// messageRefDTO 是被引用消息的紧凑预览，这里只取定位所需的 ID。
type messageRefDTO struct {
	ID string `json:"id"`
}

// conversationDTO 对应会话详情，kind 决定私聊与群聊的响应策略。
type conversationDTO struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	Title   string      `json:"title"`
	Members []memberDTO `json:"members"`
}

// memberDTO 是会话成员。
type memberDTO struct {
	User userDTO `json:"user"`
}

// 会话形态取值，与 Violet domain 的 ConversationKind 一致。
const (
	kindDirect = "direct"
	kindRoom   = "room"
)

// envelope 是 Violet 的统一响应信封：成功时数据在 data，游标在 meta.pagination。
type envelope struct {
	Data envelopeData  `json:"data"`
	Meta *envelopeMeta `json:"meta"`
}

// envelopeData 用原始 JSON 承载，让调用方按端点决定解成对象还是数组。
type envelopeData = json.RawMessage

// envelopeMeta 只解析接入端需要的分页信息。
type envelopeMeta struct {
	Message    string      `json:"message"`
	Pagination *pagination `json:"pagination"`
}

type pagination struct {
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// errorBody 是失败响应：错误码与人类可读消息分开，日志里两者都留才查得出原因。
type errorBody struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// eventFrame 是 SSE 事件信封。ID 恒为空：bot 流不写 id: 行，也不支持续传。
//
// Data 保留原始 JSON：聊天事件与推文事件的负载形状完全不同，
// 先解成统一结构会让其中一类永远读不到字段；由 handleFrame 按事件名分别解码。
type eventFrame struct {
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// eventPayload 合并两类聊天事件的字段：message.created 用 conversation_id + message，
// typing.updated 用 conversation_id + user_id + is_typing。
type eventPayload struct {
	ConversationID string     `json:"conversation_id"`
	Message        messageDTO `json:"message"`
	UserID         string     `json:"user_id"`
	IsTyping       bool       `json:"is_typing"`
}

// tweetAuthorDTO 是推文与评论作者的公开资料；is_bot 用于跳过自己或同类机器人。
type tweetAuthorDTO struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	IsBot       bool   `json:"is_bot"`
}

// tweetImageDTO 是推文配图的定位信息。
type tweetImageDTO struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// tweetPictureDTO 是评论配图，额外带字节大小。
type tweetPictureDTO struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Size   int64  `json:"size"`
}

// tweetDTO 对应 TweetDTO：SSE 事件、公开读接口与写接口共用同一形状。
// 可选字段（显示名、头像、引用快照）缺失时保持零值，不视为解析失败。
type tweetDTO struct {
	ID           string          `json:"id"`
	Author       tweetAuthorDTO  `json:"author"`
	Content      string          `json:"content"`
	Images       []tweetImageDTO `json:"images,omitempty"`
	LikeCount    int             `json:"like_count"`
	IsLiked      bool            `json:"is_liked"`
	CommentCount int             `json:"comment_count"`
	QuoteCount   int             `json:"quote_count"`
	QuoteOf      string          `json:"quote_of"`
	QuotedTweet  *tweetDTO       `json:"quoted_tweet,omitempty"`
	CreatedAt    string          `json:"created_at"`
}

// commentDTO 对应 CommentDTO：parent_id 非空表示对另一条评论的回复。
type commentDTO struct {
	ID           string            `json:"id"`
	TweetID      string            `json:"tweet_id"`
	Author       tweetAuthorDTO    `json:"author"`
	Body         string            `json:"body"`
	Pictures     []tweetPictureDTO `json:"pictures,omitempty"`
	ParentID     string            `json:"parent_id"`
	Depth        int               `json:"depth"`
	RepliesCount int               `json:"replies_count"`
	CreatedAt    string            `json:"created_at"`
}

// tweetCommentedPayload 对应 tweet.commented：
// reason 区分「评论了 bot 的推文」与「回复了推文下的某条评论」。
type tweetCommentedPayload struct {
	Reason  string     `json:"reason"`
	Tweet   tweetDTO   `json:"tweet"`
	Comment commentDTO `json:"comment"`
}

// tweetQuotedPayload 对应 tweet.quoted：quoted_tweet 是本人被引用的推文，
// quote 是那条新发布的引用推文（也是回复要落到的目标）。
type tweetQuotedPayload struct {
	QuotedTweet tweetDTO `json:"quoted_tweet"`
	Quote       tweetDTO `json:"quote"`
}

// tweetLikedPayload 对应 tweet.liked：只用于记录，不触发模型回合。
type tweetLikedPayload struct {
	Tweet tweetDTO       `json:"tweet"`
	Actor tweetAuthorDTO `json:"actor"`
}

// newTweetRequest 是 POST /tweets/bot 的请求体。
type newTweetRequest struct {
	Content string   `json:"content,omitempty"`
	Images  []string `json:"images,omitempty"`
	QuoteOf string   `json:"quote_of,omitempty"`
}

// newCommentRequest 是 POST /tweets/bot/{id}/comments 的请求体。
type newCommentRequest struct {
	Body     string            `json:"body,omitempty"`
	Pictures []tweetPictureDTO `json:"pictures,omitempty"`
	ParentID string            `json:"parent_id,omitempty"`
}

// SSE 事件名，与 Violet 写帧用的事件类型一致。
const (
	eventMessageCreated = "message.created"
	eventTypingUpdated  = "typing.updated"

	eventTweetCommented = "tweet.commented"
	eventTweetQuoted    = "tweet.quoted"
	eventTweetLiked     = "tweet.liked"
)

// tweet.commented 的 reason 取值，与 Violet 的事件契约一致。
const (
	tweetReasonCommentOnTweet = "comment_on_tweet"
	tweetReasonReplyToComment = "reply_to_comment"
)
