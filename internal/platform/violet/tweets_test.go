package violetplatform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"rua.plus/saber/internal/chat"
)

// newTweetPlatform 构造指向假服务端的平台并注入 bot 身份，不启动事件流。
func newTweetPlatform(t *testing.T, fake *fakeViolet) *Platform {
	t.Helper()
	p := New(newTestConfig(fake.endpoint()))
	p.setIdentity(fake.profile)
	return p
}

// TestTweetIdempotencyKey 固定三条规则：空键生成随机 UUID、合法 UUID 归一化、
// 非 UUID 直接拒绝（Violet 的服务端按 UUID 解析该头）。
func TestTweetIdempotencyKey(t *testing.T) {
	t.Parallel()
	generated, err := tweetIdempotencyKey("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(generated); err != nil {
		t.Fatalf("空键应生成 UUID，got %q", generated)
	}
	another, err := tweetIdempotencyKey("  ")
	if err != nil || another == generated {
		t.Fatalf("两次空键应各自独立: %q / %q, err=%v", generated, another, err)
	}
	key := uuid.NewString()
	normalized, err := tweetIdempotencyKey("  " + key + "  ")
	if err != nil || normalized != key {
		t.Fatalf("合法键被改写: %q -> %q, err=%v", key, normalized, err)
	}
	if _, err := tweetIdempotencyKey("saber-txn-1"); err == nil {
		t.Fatal("非 UUID 键应被拒绝")
	}
	if err := requireTweetKey(""); err == nil {
		t.Fatal("客户端层不接受空键：键必须由调用方先确定")
	}
}

// TestClient_CreateTweet 验证发推请求的路径、正文、图片、引用与 UUID 幂等键。
func TestClient_CreateTweet(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	quoted := fake.addTweet(tweetDTO{ID: "tweet-origin", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}, Content: "原创"})
	p := newTweetPlatform(t, fake)
	key := uuid.NewString()
	tweet, err := p.CreateTweet(context.Background(), chat.TweetDraft{
		Content: "  新推文  ",
		Images:  []string{"/uploads/a.png"},
		QuoteOf: quoted.ID,
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	if tweet.ID == "" || tweet.QuoteOf != quoted.ID || tweet.QuotedTweet == nil || tweet.QuotedTweet.ID != quoted.ID {
		t.Fatalf("返回的推文未映射引用关系: %+v", tweet)
	}
	if !tweet.Author.IsBot || tweet.Author.Username != testBotUsername || tweet.CreatedAt.IsZero() {
		t.Fatalf("作者与时间未映射: %+v", tweet)
	}
	posts := fake.tweetPostRecords()
	if len(posts) != 1 {
		t.Fatalf("发推记录 = %+v", posts)
	}
	post := posts[0]
	if post.Path != "/api/v1/tweets/bot" || post.Content != "新推文" || post.QuoteOf != quoted.ID || post.Idempotency != key {
		t.Fatalf("服务端收到的发推请求 = %+v", post)
	}
	if len(post.Images) != 1 || post.Images[0] != "/uploads/a.png" {
		t.Fatalf("图片未透传: %+v", post.Images)
	}
}

// TestClient_CreateTweetGeneratesUUIDWhenEmpty 验证空键由平台生成 UUID，而不是发一个非法头。
func TestClient_CreateTweetGeneratesUUIDWhenEmpty(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	p := newTweetPlatform(t, fake)
	if _, err := p.CreateTweet(context.Background(), chat.TweetDraft{Content: "随手一贴"}, ""); err != nil {
		t.Fatal(err)
	}
	posts := fake.tweetPostRecords()
	if len(posts) != 1 {
		t.Fatalf("发推记录 = %+v", posts)
	}
	if _, err := uuid.Parse(posts[0].Idempotency); err != nil {
		t.Fatalf("空键未生成 UUID: %q", posts[0].Idempotency)
	}
}

// TestPlatform_CreateTweetRejectsInvalid 验证客户端侧闸门：非 UUID 键、空草稿、
// 超出图片上限都在本地拦下，不产生任何请求。
func TestPlatform_CreateTweetRejectsInvalid(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	p := newTweetPlatform(t, fake)
	ctx := context.Background()
	if _, err := p.CreateTweet(ctx, chat.TweetDraft{Content: "内容"}, "not-a-uuid"); err == nil {
		t.Fatal("非 UUID 键应被拒绝")
	}
	if _, err := p.CreateTweet(ctx, chat.TweetDraft{}, uuid.NewString()); err == nil {
		t.Fatal("空草稿应被拒绝")
	}
	images := make([]string, 0, p.violet.Tweets.MaxImagesPerTweet+1)
	for index := 0; index <= p.violet.Tweets.MaxImagesPerTweet; index++ {
		images = append(images, "/uploads/img.png")
	}
	if _, err := p.CreateTweet(ctx, chat.TweetDraft{Content: "图太多", Images: images}, uuid.NewString()); err == nil {
		t.Fatal("超过配图上限应被拒绝")
	}
	if calls := fake.tweetCallRecords(); len(calls) != 0 {
		t.Fatalf("被拒的请求不该打到服务端: %+v", calls)
	}
}

// TestClient_CreateComment 验证评论请求落到推文路径、带父评论与图片，并映射返回值。
func TestClient_CreateComment(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.addTweet(tweetDTO{ID: "tweet-1", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}, Content: "推文"})
	parent := fake.addTweetComment(commentDTO{ID: "comment-parent", TweetID: "tweet-1", Author: tweetAuthorDTO{ID: "user-1", Username: "alice"}, Body: "原评论"})
	p := newTweetPlatform(t, fake)
	key := uuid.NewString()
	comment, err := p.CreateComment(context.Background(), "tweet-1", chat.TweetCommentDraft{
		Body:     "  回复内容  ",
		ParentID: parent.ID,
		Pictures: []chat.TweetPicture{{URL: "/uploads/p.png", Width: 2, Height: 3, Size: 4}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	if comment.ID == "" || comment.TweetID != "tweet-1" || comment.ParentID != parent.ID || comment.Depth != 1 {
		t.Fatalf("返回的评论未映射父级与层级: %+v", comment)
	}
	if !comment.Author.IsBot || len(comment.Pictures) != 1 || comment.Pictures[0].Size != 4 {
		t.Fatalf("评论作者与图片未映射: %+v", comment)
	}
	posts := fake.tweetCommentRecords()
	if len(posts) != 1 {
		t.Fatalf("评论记录 = %+v", posts)
	}
	post := posts[0]
	if post.Path != "/api/v1/tweets/bot/tweet-1/comments" || post.TweetID != "tweet-1" || post.ParentID != parent.ID || post.Body != "回复内容" || post.Idempotency != key {
		t.Fatalf("服务端收到的评论请求 = %+v", post)
	}
	if len(post.Pictures) != 1 || post.Pictures[0].URL != "/uploads/p.png" {
		t.Fatalf("评论图片未透传: %+v", post.Pictures)
	}
	if _, err := p.CreateComment(context.Background(), "tweet-1", chat.TweetCommentDraft{}, uuid.NewString()); err == nil {
		t.Fatal("空评论应被拒绝")
	}
}

// TestPlatform_LikeTweet 验证点赞与取消点赞分别落到 POST/DELETE。
func TestPlatform_LikeTweet(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.addTweet(tweetDTO{ID: "tweet-1", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}})
	p := newTweetPlatform(t, fake)
	ctx := context.Background()
	if err := p.LikeTweet(ctx, "tweet-1", true); err != nil {
		t.Fatal(err)
	}
	if !fake.tweetLikeState("tweet-1") {
		t.Fatal("点赞状态未生效")
	}
	if err := p.LikeTweet(ctx, "tweet-1", false); err != nil {
		t.Fatal(err)
	}
	if fake.tweetLikeState("tweet-1") {
		t.Fatal("取消点赞未生效")
	}
	calls := fake.tweetCallRecords()
	want := []string{"POST /api/v1/tweets/bot/tweet-1/like", "DELETE /api/v1/tweets/bot/tweet-1/like"}
	if len(calls) != len(want) {
		t.Fatalf("点赞调用 = %+v", calls)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("点赞调用[%d] = %q, want %q", index, calls[index], want[index])
		}
	}
	if err := p.LikeTweet(ctx, "tweet-nope", true); clientStatus(err) != http.StatusNotFound {
		t.Fatalf("未知推文点赞应报 404，got %v", err)
	}
}

// TestPlatform_DeleteTweetAndComment 验证删除路径与幂等空操作语义。
func TestPlatform_DeleteTweetAndComment(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.addTweet(tweetDTO{ID: "tweet-1", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}})
	comment := fake.addTweetComment(commentDTO{TweetID: "tweet-1", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}, Body: "待删"})
	p := newTweetPlatform(t, fake)
	ctx := context.Background()
	if err := p.DeleteComment(ctx, "tweet-1", comment.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.commentsFor("tweet-1")) != 0 {
		t.Fatalf("评论未被删除: %+v", fake.commentsFor("tweet-1"))
	}
	if err := p.DeleteTweet(ctx, "tweet-1"); err != nil {
		t.Fatal(err)
	}
	if deleted := fake.tweetDeletedRecords(); len(deleted) != 1 || deleted[0] != "tweet-1" {
		t.Fatalf("删除记录 = %+v", deleted)
	}
	if err := p.DeleteTweet(ctx, "tweet-1"); clientStatus(err) != http.StatusNotFound {
		t.Fatalf("重复删除应报 404，got %v", err)
	}
	calls := fake.tweetCallRecords()
	want := []string{
		"DELETE /api/v1/tweets/bot/tweet-1/comments/" + comment.ID,
		"DELETE /api/v1/tweets/bot/tweet-1",
		"DELETE /api/v1/tweets/bot/tweet-1",
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Fatalf("删除调用[%d] = %q, want %q", index, calls[index], want[index])
		}
	}
}

// TestPlatform_UploadImage 验证 multipart 上传与本地闸门（MIME、大小、空数据）。
func TestPlatform_UploadImage(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	p := newTweetPlatform(t, fake)
	media, err := p.UploadImage(context.Background(), []byte("\x89PNG\r\n\x1a\nfake-png"), "image/png", "pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if media.ID == "" || media.URL == "" || media.MIMEType != "image/png" || media.Size == 0 {
		t.Fatalf("上传结果 = %+v", media)
	}
	uploads := fake.tweetUploadedRecords()
	if len(uploads) != 1 || uploads[0].Filename != "pic.png" || uploads[0].MIMEType != "image/png" {
		t.Fatalf("上传记录 = %+v", uploads)
	}
	for _, tc := range []struct {
		name     string
		data     []byte
		mimeType string
	}{
		{"空数据", nil, "image/png"},
		{"非图片", []byte("x"), "text/plain"},
		{"超大", make([]byte, maxMediaBytes+1), "image/png"},
	} {
		if _, err := p.UploadImage(context.Background(), tc.data, tc.mimeType, "x"); err == nil {
			t.Fatalf("%s 应被拒绝", tc.name)
		}
	}
	if got := len(fake.tweetUploadedRecords()); got != 1 {
		t.Fatalf("被拒的上传打到了服务端: %d", got)
	}
}

// TestPlatform_GetTweet 验证公开读不带 Bot Token，且作者与时间正确映射。
func TestPlatform_GetTweet(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	created := time.Now().Add(-time.Hour).UTC()
	fake.addTweet(tweetDTO{
		ID:      "tweet-1",
		Author:  tweetAuthorDTO{ID: "user-1", Username: "alice", DisplayName: "Alice"},
		Content: "公开推文", LikeCount: 3, IsLiked: true, CommentCount: 2, QuoteCount: 1,
		CreatedAt: created.Format(time.RFC3339Nano),
	})
	p := newTweetPlatform(t, fake)
	tweet, err := p.GetTweet(context.Background(), "tweet-1")
	if err != nil {
		t.Fatal(err)
	}
	if tweet.ID != "tweet-1" || tweet.Content != "公开推文" || tweet.Author.Username != "alice" || tweet.Author.DisplayName != "Alice" {
		t.Fatalf("推文映射 = %+v", tweet)
	}
	if tweet.Author.IsBot || tweet.LikeCount != 3 || !tweet.IsLiked || tweet.CommentCount != 2 || tweet.QuoteCount != 1 {
		t.Fatalf("统计或 bot 标记不符: %+v", tweet)
	}
	if !tweet.CreatedAt.Equal(created) {
		t.Fatalf("时间映射 = %v, want %v", tweet.CreatedAt, created)
	}
	for _, auth := range fake.tweetPublicAuthRecords() {
		if auth != "" {
			t.Fatalf("公开读不该携带鉴权头: %q", auth)
		}
	}
	if _, err := p.GetTweet(context.Background(), "tweet-nope"); clientStatus(err) != http.StatusNotFound {
		t.Fatalf("未知推文应报 404，got %v", err)
	}
}

// TestPlatform_ListComments 验证顶层评论与回复走不同路径，且 limit 透传。
func TestPlatform_ListComments(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.addTweet(tweetDTO{ID: "tweet-1", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}})
	top := fake.addTweetComment(commentDTO{TweetID: "tweet-1", Author: tweetAuthorDTO{ID: "user-1", Username: "alice"}, Body: "顶层"})
	fake.addTweetComment(commentDTO{TweetID: "tweet-1", Author: tweetAuthorDTO{ID: "user-2", Username: "bob"}, Body: "回复", ParentID: top.ID, Depth: 1})
	p := newTweetPlatform(t, fake)
	ctx := context.Background()
	comments, err := p.ListComments(ctx, "tweet-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID != top.ID || comments[0].Author.Username != "alice" {
		t.Fatalf("顶层评论 = %+v", comments)
	}
	replies, err := p.ListComments(ctx, "tweet-1", top.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 1 || replies[0].ParentID != top.ID || replies[0].Depth != 1 {
		t.Fatalf("回复 = %+v", replies)
	}
	calls := fake.tweetCallRecords()
	if len(calls) != 2 ||
		!strings.HasPrefix(calls[0], "GET /api/v1/tweets/tweet-1/comments?") ||
		!strings.Contains(calls[0], "limit=10") ||
		!strings.HasPrefix(calls[1], "GET /api/v1/tweets/tweet-1/comments/"+top.ID+"/replies?") ||
		!strings.Contains(calls[1], "limit=5") {
		t.Fatalf("评论读取路径 = %+v", calls)
	}
	// limit 非正时客户端按 20 条取首页。
	if _, err := p.ListComments(ctx, "tweet-1", "", 0); err != nil {
		t.Fatal(err)
	}
	if calls := fake.tweetCallRecords(); !strings.Contains(calls[len(calls)-1], "limit=20") {
		t.Fatalf("零 limit 未回退 20: %q", calls[len(calls)-1])
	}
}

// TestPlatform_BotUsername 验证用户名来自缓存的 bot 身份。
func TestPlatform_BotUsername(t *testing.T) {
	t.Parallel()
	p := newTweetPlatform(t, newFakeViolet(t))
	if got := p.BotUsername(); got != testBotUsername {
		t.Fatalf("BotUsername = %q, want %q", got, testBotUsername)
	}
	if got := New(nil).BotUsername(); got != "" {
		t.Fatalf("未取到身份时应为空串，got %q", got)
	}
}

// TestClient_TweetAuthErrorKeepsStatus 验证推文写端点的 401 保留状态码。
func TestClient_TweetAuthErrorKeepsStatus(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	api := newClient(fake.endpoint(), "violet_bot_wrong", 5)
	_, err := api.createTweet(context.Background(), newTweetRequest{Content: "x"}, uuid.NewString())
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || apiErr.Code == "" {
		t.Fatalf("鉴权错误 = %v", err)
	}
}

// TestClient_TweetRateLimitKeepsRetryAfter 验证推文端点的 Retry-After 提示不被吞掉。
func TestClient_TweetRateLimitKeepsRetryAfter(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate_limit_exceeded","message":"发帖过于频繁"}`))
	}))
	defer server.Close()
	_, err := newClient(server.URL, testToken, 5).createTweet(context.Background(), newTweetRequest{Content: "x"}, uuid.NewString())
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.RetryDelay() != time.Minute {
		t.Fatalf("限流等待提示丢失: %v", err)
	}
}

// TestClient_TweetConflict 验证评论冲突（409）保留状态码，供上层决定是否重试。
func TestClient_TweetConflict(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	p := newTweetPlatform(t, fake)
	_, err := p.CreateComment(context.Background(), "tweet-missing", chat.TweetCommentDraft{Body: "x"}, uuid.NewString())
	if clientStatus(err) != http.StatusNotFound {
		t.Fatalf("未知推文评论应报 404，got %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"CONFLICT","message":"评论已存在"}`))
	}))
	defer server.Close()
	p = New(newTestConfig(server.URL))
	p.setIdentity(botProfileDTO{UserID: testBotUserID, Username: testBotUsername})
	_, err = p.CreateComment(context.Background(), "tweet-1", chat.TweetCommentDraft{Body: "x"}, uuid.NewString())
	if clientStatus(err) != http.StatusConflict {
		t.Fatalf("冲突应报 409，got %v", err)
	}
}

// TestTweetReplyAdapter_CapabilitiesAndEdit 验证零能力与显式不支持编辑：
// 能力为零时展示层不会走流式路径，Edit 一旦被误用必须立刻报错。
func TestTweetReplyAdapter_CapabilitiesAndEdit(t *testing.T) {
	t.Parallel()
	adapter := &tweetReplyAdapter{platform: newTweetPlatform(t, newFakeViolet(t)), tweetID: "tweet-1", eventID: "comment-1"}
	if capabilities := adapter.Capabilities(); capabilities != (chat.Capabilities{}) {
		t.Fatalf("推文回复能力应为零值: %+v", capabilities)
	}
	if err := adapter.Edit(context.Background(), "comment-1", chat.Reply{Text: "x"}); err == nil {
		t.Fatal("推文评论不支持编辑")
	}
	if err := adapter.SetTyping(context.Background(), chat.Session{Platform: platformName, Account: "a", Conversation: "tweet:tweet-1"}, true); err != nil {
		t.Fatalf("输入状态应为空操作: %v", err)
	}
}

// TestTweetReplyAdapter_DeterministicIdempotency 验证同一事件重复发送落到同一条评论：
// 键必须由事件 ID 稳定派生且是 UUID，否则重试会公开刷出两条一样的回复。
func TestTweetReplyAdapter_DeterministicIdempotency(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	fake.addTweet(tweetDTO{ID: "tweet-1", Author: tweetAuthorDTO{ID: testBotUserID, Username: testBotUsername, IsBot: true}})
	fake.addTweetComment(commentDTO{ID: "comment-1", TweetID: "tweet-1", Author: tweetAuthorDTO{ID: "user-1", Username: "alice"}, Body: "原评论"})
	p := newTweetPlatform(t, fake)
	adapter := &tweetReplyAdapter{platform: p, tweetID: "tweet-1", parentCommentID: "comment-1", eventID: "comment-1", limit: 480}
	ctx := context.Background()
	first, err := adapter.Send(ctx, chat.Reply{Text: "  答一句  "})
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Send(ctx, chat.Reply{Text: "答一句"})
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("同事件的重复发送应是同一条评论: %q vs %q", first, second)
	}
	posts := fake.tweetCommentRecords()
	if len(posts) != 1 || posts[0].Body != "答一句" {
		t.Fatalf("重复发送刷出了多条评论: %+v", posts)
	}
	want := uuid.NewSHA1(uuid.NameSpaceURL, []byte("saber:violet:tweet-reply:comment-1")).String()
	if posts[0].Idempotency != want {
		t.Fatalf("幂等键 = %q, want %q", posts[0].Idempotency, want)
	}
	if _, err := uuid.Parse(posts[0].Idempotency); err != nil {
		t.Fatalf("幂等键不是 UUID: %q", posts[0].Idempotency)
	}
	if empty, err := adapter.Send(ctx, chat.Reply{Text: "   "}); err != nil || empty != "" {
		t.Fatalf("空正文应为空操作: %q, %v", empty, err)
	}
	if got := len(fake.tweetCommentRecords()); got != 1 {
		t.Fatalf("空正文不该发评论: %d", got)
	}
}
