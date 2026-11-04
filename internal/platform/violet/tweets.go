package violetplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"rua.plus/saber/internal/chat"
)

// 推文端点前缀：写端点挂在 bot 命名空间下并要求 Bearer token，
// 读端点公开免鉴权。两者与聊天端点同源，由 client 在构造时拼好。
const (
	tweetAPIPrefix       = "/api/v1/tweets/bot"
	publicTweetAPIPrefix = "/api/v1/tweets"
)

// maxTweetContextImages 是推文互动下载为附件的图片上限，与推文配图上限保持一致。
const maxTweetContextImages = 4

// 编译期断言：Platform 同时提供聊天接入与推文读写端口。
var _ chat.TweetAdapter = (*Platform)(nil)

// tweetIdempotencyKey 校验并归一化调用方给的幂等键。
//
// 空键生成随机 UUID：两次有意的相同发布必须彼此独立，键一旦由内容派生，
// 「再发一条一样的」就会被服务端当成重试吞掉。非 UUID 的键直接拒绝，
// 因为 Violet 的推文写端点按 UUID 解析该头，发过去只会换一个 400。
func tweetIdempotencyKey(key string) (string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		generated, err := uuid.NewRandom()
		if err != nil {
			return "", fmt.Errorf("生成推文幂等键失败: %w", err)
		}
		return generated.String(), nil
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("推文幂等键必须是 UUID，收到 %q", trimmed)
	}
	return parsed.String(), nil
}

// requireTweetKey 校验内部调用链给出的幂等键，空值同样拒绝：
// 走到客户端层时键必须已经由调用方决定，这里不再代为生成。
func requireTweetKey(key string) error {
	if _, err := uuid.Parse(strings.TrimSpace(key)); err != nil {
		return fmt.Errorf("推文幂等键必须是 UUID，收到 %q", key)
	}
	return nil
}

// truncateTweetRunes 把正文压到上限内；上限非正时原样返回。
// 按 Unicode 字符计数，与 Violet 服务端的校验口径一致。
func truncateTweetRunes(content string, limit int) string {
	if limit <= 0 {
		return content
	}
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	return string(runes[:limit])
}

// createTweet 发布一条推文。key 必须是已确定的 UUID，重试要复用同一个键。
func (c *client) createTweet(ctx context.Context, body newTweetRequest, key string) (tweetDTO, error) {
	if err := requireTweetKey(key); err != nil {
		return tweetDTO{}, err
	}
	var out tweetDTO
	if err := c.requestAt(ctx, c.tweetBase, true, http.MethodPost, "", nil, body, key, &out); err != nil {
		return tweetDTO{}, err
	}
	if out.ID == "" {
		return tweetDTO{}, errors.New("violet 创建推文未返回推文 ID")
	}
	return out, nil
}

// createTweetComment 在推文下发布评论，parentID 非空时作为回复落到该评论下。
func (c *client) createTweetComment(ctx context.Context, tweetID string, body newCommentRequest, key string) (commentDTO, error) {
	if tweetID == "" {
		return commentDTO{}, errors.New("violet 评论缺少推文 ID")
	}
	if err := requireTweetKey(key); err != nil {
		return commentDTO{}, err
	}
	path := "/" + url.PathEscape(tweetID) + "/comments"
	var out commentDTO
	if err := c.requestAt(ctx, c.tweetBase, true, http.MethodPost, path, nil, body, key, &out); err != nil {
		return commentDTO{}, err
	}
	if out.ID == "" {
		return commentDTO{}, errors.New("violet 发布评论未返回评论 ID")
	}
	return out, nil
}

// likeTweet 设置或取消点赞，端点无响应体。
func (c *client) likeTweet(ctx context.Context, tweetID string, on bool) error {
	if tweetID == "" {
		return errors.New("violet 点赞缺少推文 ID")
	}
	method := http.MethodPost
	if !on {
		method = http.MethodDelete
	}
	return c.requestAt(ctx, c.tweetBase, true, method, "/"+url.PathEscape(tweetID)+"/like", nil, nil, "", nil)
}

// deleteTweet 删除自己发布的推文。
func (c *client) deleteTweet(ctx context.Context, tweetID string) error {
	if tweetID == "" {
		return errors.New("violet 删除推文缺少推文 ID")
	}
	return c.requestAt(ctx, c.tweetBase, true, http.MethodDelete, "/"+url.PathEscape(tweetID), nil, nil, "", nil)
}

// deleteTweetComment 删除自己发布的评论。
func (c *client) deleteTweetComment(ctx context.Context, tweetID, commentID string) error {
	if tweetID == "" || commentID == "" {
		return errors.New("violet 删除评论缺少推文或评论 ID")
	}
	path := "/" + url.PathEscape(tweetID) + "/comments/" + url.PathEscape(commentID)
	return c.requestAt(ctx, c.tweetBase, true, http.MethodDelete, path, nil, nil, "", nil)
}

// uploadTweetMedia 上传一张推文图片。与聊天媒体上传共用 multipart 构造，
// 但落在推文命名空间下：两套端点的媒体 ID 不通用。
func (c *client) uploadTweetMedia(ctx context.Context, data []byte, mimeType, filename string) (mediaUploadDTO, error) {
	if len(data) == 0 {
		return mediaUploadDTO{}, errors.New("violet 推文图片内容为空")
	}
	if int64(len(data)) > maxMediaBytes {
		return mediaUploadDTO{}, fmt.Errorf("violet 推文图片 %d 字节超过 %d 上限", len(data), maxMediaBytes)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return mediaUploadDTO{}, fmt.Errorf("violet 推文图片类型 %q 不是图片", mimeType)
	}
	body, contentType, err := multipartImageBody(data, mimeType, filename)
	if err != nil {
		return mediaUploadDTO{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tweetBase+"/media", body)
	if err != nil {
		return mediaUploadDTO{}, fmt.Errorf("构造 violet 推文图片上传请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", contentType)
	resp, err := c.api.Do(req)
	if err != nil {
		return mediaUploadDTO{}, fmt.Errorf("请求 violet 推文图片上传失败: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("关闭 violet 推文图片上传响应失败", "error", closeErr)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return mediaUploadDTO{}, statusError(resp)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return mediaUploadDTO{}, fmt.Errorf("读取 violet 推文图片上传响应失败: %w", err)
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return mediaUploadDTO{}, errors.New("violet 推文图片上传响应为空")
	}
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return mediaUploadDTO{}, fmt.Errorf("解析 violet 推文图片上传响应失败: %w", err)
	}
	var out mediaUploadDTO
	if err := json.Unmarshal(env.Data, &out); err != nil {
		return mediaUploadDTO{}, fmt.Errorf("解析 violet 推文图片上传数据失败: %w", err)
	}
	if out.ID == "" || out.URL == "" {
		return mediaUploadDTO{}, errors.New("violet 推文图片上传未返回可用的图片定位")
	}
	return out, nil
}

// getTweet 读取单条推文；该端点公开，不携带 Bot Token。
func (c *client) getTweet(ctx context.Context, tweetID string) (tweetDTO, error) {
	if tweetID == "" {
		return tweetDTO{}, errors.New("violet 读取推文缺少推文 ID")
	}
	var out tweetDTO
	if err := c.requestAt(ctx, c.publicTweetBase, false, http.MethodGet, "/"+url.PathEscape(tweetID), nil, nil, "", &out); err != nil {
		return tweetDTO{}, err
	}
	return out, nil
}

// listTweetComments 读取评论列表。parentCommentID 为空取顶层评论，否则取该评论的回复。
// 该端点公开，不携带 Bot Token。
func (c *client) listTweetComments(ctx context.Context, tweetID, parentCommentID string, limit int) ([]commentDTO, error) {
	if tweetID == "" {
		return nil, errors.New("violet 读取评论缺少推文 ID")
	}
	path := "/" + url.PathEscape(tweetID) + "/comments"
	if parentCommentID != "" {
		path += "/" + url.PathEscape(parentCommentID) + "/replies"
	}
	query := url.Values{"limit": []string{fmt.Sprint(limit)}}
	var out []commentDTO
	if err := c.requestAt(ctx, c.publicTweetBase, false, http.MethodGet, path, query, nil, "", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTweet 实现 chat.TweetAdapter：发布推文并映射为平台无关的读模型。
func (p *Platform) CreateTweet(ctx context.Context, draft chat.TweetDraft, idempotencyKey string) (chat.Tweet, error) {
	if p.api == nil {
		return chat.Tweet{}, errors.New("violet 平台缺少配置")
	}
	key, err := tweetIdempotencyKey(idempotencyKey)
	if err != nil {
		return chat.Tweet{}, err
	}
	content := truncateTweetRunes(strings.TrimSpace(draft.Content), p.violet.Tweets.EffectiveMaxContentRunes())
	quoteOf := strings.TrimSpace(draft.QuoteOf)
	if content == "" && len(draft.Images) == 0 && quoteOf == "" {
		return chat.Tweet{}, errors.New("violet 推文缺少正文、图片与引用对象")
	}
	if limit := p.violet.Tweets.MaxImagesPerTweet; limit >= 0 && len(draft.Images) > limit {
		return chat.Tweet{}, fmt.Errorf("violet 推文图片 %d 张超过上限 %d", len(draft.Images), limit)
	}
	created, err := p.api.createTweet(ctx, newTweetRequest{Content: content, Images: draft.Images, QuoteOf: quoteOf}, key)
	if err != nil {
		return chat.Tweet{}, fmt.Errorf("violet 创建推文失败: %w", err)
	}
	return p.chatTweet(created), nil
}

// CreateComment 实现 chat.TweetAdapter：在推文下发布评论并可指定父评论。
func (p *Platform) CreateComment(ctx context.Context, tweetID string, draft chat.TweetCommentDraft, idempotencyKey string) (chat.TweetComment, error) {
	if p.api == nil {
		return chat.TweetComment{}, errors.New("violet 平台缺少配置")
	}
	key, err := tweetIdempotencyKey(idempotencyKey)
	if err != nil {
		return chat.TweetComment{}, err
	}
	tweetID = strings.TrimSpace(tweetID)
	if tweetID == "" {
		return chat.TweetComment{}, errors.New("violet 评论缺少推文 ID")
	}
	body := truncateTweetRunes(strings.TrimSpace(draft.Body), p.violet.Tweets.EffectiveMaxContentRunes())
	pictures := make([]tweetPictureDTO, 0, len(draft.Pictures))
	for _, picture := range draft.Pictures {
		if strings.TrimSpace(picture.URL) == "" {
			return chat.TweetComment{}, errors.New("violet 评论图片缺少地址")
		}
		pictures = append(pictures, tweetPictureDTO{URL: picture.URL, Width: picture.Width, Height: picture.Height, Size: picture.Size})
	}
	if body == "" && len(pictures) == 0 {
		return chat.TweetComment{}, errors.New("violet 评论缺少正文与图片")
	}
	created, err := p.api.createTweetComment(ctx, tweetID, newCommentRequest{Body: body, Pictures: pictures, ParentID: strings.TrimSpace(draft.ParentID)}, key)
	if err != nil {
		return chat.TweetComment{}, fmt.Errorf("violet 发布评论失败: %w", err)
	}
	return p.chatComment(created), nil
}

// LikeTweet 实现 chat.TweetAdapter。
func (p *Platform) LikeTweet(ctx context.Context, tweetID string, on bool) error {
	if p.api == nil {
		return errors.New("violet 平台缺少配置")
	}
	if err := p.api.likeTweet(ctx, strings.TrimSpace(tweetID), on); err != nil {
		return fmt.Errorf("violet 点赞操作失败: %w", err)
	}
	return nil
}

// DeleteTweet 实现 chat.TweetAdapter。
func (p *Platform) DeleteTweet(ctx context.Context, tweetID string) error {
	if p.api == nil {
		return errors.New("violet 平台缺少配置")
	}
	if err := p.api.deleteTweet(ctx, strings.TrimSpace(tweetID)); err != nil {
		return fmt.Errorf("violet 删除推文失败: %w", err)
	}
	return nil
}

// DeleteComment 实现 chat.TweetAdapter。
func (p *Platform) DeleteComment(ctx context.Context, tweetID, commentID string) error {
	if p.api == nil {
		return errors.New("violet 平台缺少配置")
	}
	if err := p.api.deleteTweetComment(ctx, strings.TrimSpace(tweetID), strings.TrimSpace(commentID)); err != nil {
		return fmt.Errorf("violet 删除评论失败: %w", err)
	}
	return nil
}

// UploadImage 实现 chat.TweetAdapter。
func (p *Platform) UploadImage(ctx context.Context, data []byte, mimeType, filename string) (chat.TweetMedia, error) {
	if p.api == nil {
		return chat.TweetMedia{}, errors.New("violet 平台缺少配置")
	}
	uploaded, err := p.api.uploadTweetMedia(ctx, data, mimeType, filename)
	if err != nil {
		return chat.TweetMedia{}, fmt.Errorf("violet 上传推文图片失败: %w", err)
	}
	return chat.TweetMedia{
		ID:       uploaded.ID,
		URL:      uploaded.URL,
		MIMEType: uploaded.MIMEType,
		Width:    uploaded.Width,
		Height:   uploaded.Height,
		Size:     uploaded.Size,
	}, nil
}

// GetTweet 实现 chat.TweetAdapter。
func (p *Platform) GetTweet(ctx context.Context, tweetID string) (chat.Tweet, error) {
	if p.api == nil {
		return chat.Tweet{}, errors.New("violet 平台缺少配置")
	}
	tweet, err := p.api.getTweet(ctx, strings.TrimSpace(tweetID))
	if err != nil {
		return chat.Tweet{}, fmt.Errorf("violet 读取推文失败: %w", err)
	}
	return p.chatTweet(tweet), nil
}

// ListComments 实现 chat.TweetAdapter；limit 非正时按 20 条取首页。
func (p *Platform) ListComments(ctx context.Context, tweetID, parentCommentID string, limit int) ([]chat.TweetComment, error) {
	if p.api == nil {
		return nil, errors.New("violet 平台缺少配置")
	}
	if limit <= 0 {
		limit = 20
	}
	comments, err := p.api.listTweetComments(ctx, strings.TrimSpace(tweetID), strings.TrimSpace(parentCommentID), limit)
	if err != nil {
		return nil, fmt.Errorf("violet 读取评论失败: %w", err)
	}
	out := make([]chat.TweetComment, 0, len(comments))
	for _, comment := range comments {
		out = append(out, p.chatComment(comment))
	}
	return out, nil
}

// BotUsername 实现 chat.TweetAdapter；身份还没取到时返回空串。
func (p *Platform) BotUsername() string {
	return p.currentIdentity().Username
}

// chatTweet 把推文 DTO 映射为 chat.Tweet；时间戳解析失败时保留零值。
func (p *Platform) chatTweet(dto tweetDTO) chat.Tweet {
	tweet := chat.Tweet{
		ID:           dto.ID,
		Author:       p.chatTweetAuthor(dto.Author),
		Content:      dto.Content,
		Images:       make([]chat.TweetImage, 0, len(dto.Images)),
		LikeCount:    dto.LikeCount,
		CommentCount: dto.CommentCount,
		QuoteCount:   dto.QuoteCount,
		IsLiked:      dto.IsLiked,
		QuoteOf:      dto.QuoteOf,
	}
	for _, image := range dto.Images {
		tweet.Images = append(tweet.Images, chat.TweetImage{URL: image.URL, Width: image.Width, Height: image.Height})
	}
	if created, ok := parseTimestamp(dto.CreatedAt); ok {
		tweet.CreatedAt = created
	}
	if dto.QuotedTweet != nil {
		quoted := p.chatTweet(*dto.QuotedTweet)
		tweet.QuotedTweet = &quoted
	}
	return tweet
}

// chatComment 把评论 DTO 映射为 chat.TweetComment。
func (p *Platform) chatComment(dto commentDTO) chat.TweetComment {
	comment := chat.TweetComment{
		ID:           dto.ID,
		TweetID:      dto.TweetID,
		Author:       p.chatTweetAuthor(dto.Author),
		Body:         dto.Body,
		Pictures:     make([]chat.TweetPicture, 0, len(dto.Pictures)),
		ParentID:     dto.ParentID,
		Depth:        dto.Depth,
		RepliesCount: dto.RepliesCount,
	}
	for _, picture := range dto.Pictures {
		comment.Pictures = append(comment.Pictures, chat.TweetPicture{URL: picture.URL, Width: picture.Width, Height: picture.Height, Size: picture.Size})
	}
	if created, ok := parseTimestamp(dto.CreatedAt); ok {
		comment.CreatedAt = created
	}
	return comment
}

// chatTweetAuthor 映射作者资料；is_bot 同时参考服务端标记与本 bot 身份，
// 后者能挡住服务端漏标 is_bot 时的自回声。
func (p *Platform) chatTweetAuthor(dto tweetAuthorDTO) chat.TweetAuthor {
	botUserID := p.botUserID()
	return chat.TweetAuthor{
		ID:          dto.ID,
		Username:    dto.Username,
		DisplayName: dto.DisplayName,
		AvatarURL:   dto.AvatarURL,
		IsBot:       dto.IsBot || (botUserID != "" && dto.ID == botUserID),
	}
}
