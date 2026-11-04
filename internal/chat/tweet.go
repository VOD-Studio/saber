package chat

import (
	"context"
	"time"
)

// 推文契约是聊天之外的第二条互动通道：Violet 把「有人评论或引用 bot 的推文」
// 推给接入端，接入端组织一次模型回合，再把结果作为公开评论发回推文。
// 这里的类型只描述接入端与上层的边界，字段命名与平台原生 DTO 解耦。

// TweetAuthor 是推文或评论作者的公开资料快照。
type TweetAuthor struct {
	// ID 是平台用户 ID，用于自回声与去重判定。
	ID string
	// Username 是唯一用户名，展示与定位都用它。
	Username string
	// DisplayName 是可选的显示名，未设置时为空。
	DisplayName string
	// AvatarURL 是头像地址，可能为空。
	AvatarURL string
	// IsBot 标记作者是否为机器人，用于跳过自己或同类 bot 的内容。
	IsBot bool
}

// TweetImage 是推文配图的定位信息。
type TweetImage struct {
	// URL 是图片地址，可能为站内相对路径。
	URL string
	// Width 与 Height 是像素尺寸，平台未提供时为 0。
	Width  int
	Height int
}

// Tweet 是一条推文的读模型；引用关系通过 QuoteOf 与 QuotedTweet 表达。
type Tweet struct {
	// ID 是推文 ID。
	ID string
	// Author 是推文作者。
	Author TweetAuthor
	// Content 是正文，纯图片推文为空。
	Content string
	// Images 是随推文发布的图片。
	Images []TweetImage
	// LikeCount、CommentCount 和 QuoteCount 是平台统计值。
	LikeCount    int
	CommentCount int
	QuoteCount   int
	// IsLiked 表示当前 bot 是否已点赞；接入端不维护点赞状态时保持 false。
	IsLiked bool
	// QuoteOf 是所引用推文的 ID，普通推文为空。
	QuoteOf string
	// QuotedTweet 是所引用推文的完整快照，平台未展开时为空。
	QuotedTweet *Tweet
	// CreatedAt 是发布时间，平台时间戳无法解析时为零值。
	CreatedAt time.Time
}

// TweetPicture 是评论配图的定位信息，与推文图片相比多出字节大小。
type TweetPicture struct {
	// URL 是图片地址。
	URL string
	// Width 与 Height 是像素尺寸，平台未提供时为 0。
	Width  int
	Height int
	// Size 是字节数，平台未提供时为 0。
	Size int64
}

// TweetComment 是一条推文评论；ParentID 非空表示它是对另一条评论的回复。
type TweetComment struct {
	// ID 是评论 ID。
	ID string
	// TweetID 是所属推文 ID。
	TweetID string
	// Author 是评论作者。
	Author TweetAuthor
	// Body 是评论正文，纯图片评论为空。
	Body string
	// Pictures 是随评论发布的图片。
	Pictures []TweetPicture
	// ParentID 是被回复评论的 ID，顶层评论为空。
	ParentID string
	// Depth 是平台给出的嵌套层级。
	Depth int
	// RepliesCount 是该评论下的回复数。
	RepliesCount int
	// CreatedAt 是发布时间，平台时间戳无法解析时为零值。
	CreatedAt time.Time
}

// TweetDraft 描述一条待发布的推文；调用方负责把正文裁剪到平台上限。
type TweetDraft struct {
	// Content 是正文，可为空（纯图片或纯引用推文）。
	Content string
	// Images 是已上传图片的地址，需先经 TweetAdapter.UploadImage 取得。
	Images []string
	// QuoteOf 是被引用推文的 ID，空值表示普通推文。
	QuoteOf string
}

// TweetCommentDraft 描述一条待发布的评论。
type TweetCommentDraft struct {
	// Body 是正文，可为空（纯图片评论）。
	Body string
	// Pictures 是已上传图片的定位信息。
	Pictures []TweetPicture
	// ParentID 是被回复评论的 ID，顶层评论为空。
	ParentID string
}

// TweetMedia 是一次图片上传的结果，URL 可直接用于推文或评论草稿。
type TweetMedia struct {
	// ID 是平台媒体 ID。
	ID string
	// URL 是可供发布的图片地址。
	URL string
	// MIMEType 是平台确认的媒体类型。
	MIMEType string
	// Width 与 Height 是像素尺寸，无法解析时为 0。
	Width  int
	Height int
	// Size 是字节数。
	Size int64
}

// TweetAdapter 是推文与评论的写读端口，由支持推文的平台实现。
//
// 写方法显式接收幂等键：重试必须复用同一个 UUID 才能得到同一条结果，
// 而两次有意的相同发布必须彼此独立，因此键不允许由内容派生。
// 空键表示「这不是重试」，由实现生成一个随机 UUID。
type TweetAdapter interface {
	// CreateTweet 发布推文；idempotencyKey 必须是 UUID 或空串。
	CreateTweet(ctx context.Context, draft TweetDraft, idempotencyKey string) (Tweet, error)
	// CreateComment 在指定推文下发布评论；idempotencyKey 规则同 CreateTweet。
	CreateComment(ctx context.Context, tweetID string, draft TweetCommentDraft, idempotencyKey string) (TweetComment, error)
	// LikeTweet 设置或取消点赞；on 为 false 时取消。
	LikeTweet(ctx context.Context, tweetID string, on bool) error
	// DeleteTweet 删除自己发布的推文。
	DeleteTweet(ctx context.Context, tweetID string) error
	// DeleteComment 删除自己发布的评论。
	DeleteComment(ctx context.Context, tweetID, commentID string) error
	// UploadImage 上传一张图片，返回可写进草稿的定位信息。
	UploadImage(ctx context.Context, data []byte, mimeType, filename string) (TweetMedia, error)
	// GetTweet 读取单条推文。
	GetTweet(ctx context.Context, tweetID string) (Tweet, error)
	// ListComments 读取评论；parentCommentID 为空时取顶层评论，否则取该评论的回复。
	ListComments(ctx context.Context, tweetID, parentCommentID string, limit int) ([]TweetComment, error)
	// BotUsername 返回当前 bot 的用户名，供模型引用自身主页。
	BotUsername() string
}
