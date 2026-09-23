package violetplatform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestClient 构造指向假服务端的客户端。
func newTestClient(fake *fakeViolet) *client {
	return newClient(fake.endpoint(), testToken, 5)
}

// TestClient_Profile 验证身份接口把 user_id 与 username 带回来——自回声判定与被 @ 判定全靠它。
func TestClient_Profile(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	profile, err := newTestClient(fake).profile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if profile.UserID != testBotUserID || profile.Username != testBotUsername {
		t.Fatalf("profile = %+v", profile)
	}
}

// TestClient_AuthErrorKeepsStatus 验证鉴权失败保留状态码与 Violet 消息：
// 401 意味着凭据问题（重试无用），不能被压成一句「请求失败」。
func TestClient_AuthErrorKeepsStatus(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	_, err := newClient(fake.endpoint(), "violet_bot_wrong", 5).profile(context.Background())
	if err == nil {
		t.Fatal("错误 token 应被拒绝")
	}
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		t.Fatalf("错误类型 = %T, want *apiError", err)
	}
	if apiErr.Status != 401 || apiErr.Code == "" {
		t.Fatalf("apiError = %+v", apiErr)
	}
	if !strings.Contains(apiErr.Error(), "缺少或无效的 Bot Token") {
		t.Fatalf("错误消息丢了服务端原因: %q", apiErr)
	}
	if got := clientStatus(err); got != 401 {
		t.Fatalf("clientStatus = %d, want 401", got)
	}
	if got := clientStatus(errors.New("普通错误")); got != 0 {
		t.Fatalf("非 API 错误的 clientStatus = %d, want 0", got)
	}
}

// TestClient_Conversation 验证会话形态可读，未知会话按 404 报错而不是返回零值。
func TestClient_Conversation(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	api := newTestClient(fake)
	ctx := context.Background()
	direct, err := api.conversation(ctx, testDirectRoom)
	if err != nil || direct.Kind != kindDirect {
		t.Fatalf("direct 会话 = %+v, err = %v", direct, err)
	}
	room, err := api.conversation(ctx, testGroupRoom)
	if err != nil || room.Kind != kindRoom {
		t.Fatalf("room 会话 = %+v, err = %v", room, err)
	}
	if _, err := api.conversation(ctx, "conv-nope"); clientStatus(err) != 404 {
		t.Fatalf("未知会话应报 404，got %v", err)
	}
	fake.setKindFailure(true)
	if _, err := api.conversation(ctx, testDirectRoom); clientStatus(err) != 500 {
		t.Fatalf("服务端故障应报 500，got %v", err)
	}
}

// TestClient_Messages 验证历史按新→旧返回并带游标翻页——补拉的正确性依赖这两件事。
func TestClient_Messages(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	total := backfillPageSize + 3
	for index := 0; index < total; index++ {
		fake.pushMessage(testDirectRoom, "user-1", "alice", fmt.Sprintf("第 %d 条", index), base.Add(time.Duration(index)*time.Second))
	}
	api := newTestClient(fake)
	first, cursor, hasMore, err := api.messages(context.Background(), testDirectRoom, "", backfillPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != backfillPageSize || !hasMore || cursor == "" {
		t.Fatalf("第一页 = %d 条, hasMore = %v, cursor = %q", len(first), hasMore, cursor)
	}
	if first[0].ID != fmt.Sprintf("msg-%s-%d", testDirectRoom, total) {
		t.Fatalf("历史首位应是最新消息，got %q", first[0].ID)
	}
	second, _, hasMore, err := api.messages(context.Background(), testDirectRoom, cursor, backfillPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 || hasMore {
		t.Fatalf("第二页 = %d 条, hasMore = %v, want 3/false", len(second), hasMore)
	}
}

// TestClient_Send 验证出站发送：幂等键走必填头、引用关系进正文、返回的消息 ID 可用于编辑。
func TestClient_Send(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	api := newTestClient(fake)
	ctx := context.Background()
	created, err := api.send(ctx, testDirectRoom, outgoingMessage{Content: "你好", ReplyToID: "msg-ref"}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.ReplyTo == nil || created.ReplyTo.ID != "msg-ref" {
		t.Fatalf("发送响应 = %+v", created)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].Idempotency != "key-1" || sent[0].ReplyTo != "msg-ref" {
		t.Fatalf("服务端收到的请求 = %+v", sent)
	}
	again, err := api.send(ctx, testDirectRoom, outgoingMessage{Content: "你好", ReplyToID: "msg-ref"}, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != created.ID {
		t.Fatalf("同幂等键重发应是同一条消息: %q vs %q", again.ID, created.ID)
	}
	if len(fake.sentMessages()) != 1 {
		t.Fatalf("重发刷出了第二条消息: %+v", fake.sentMessages())
	}
	if _, err := api.send(ctx, testDirectRoom, outgoingMessage{Content: "   "}, "key-2"); clientStatus(err) != 400 {
		t.Fatalf("空白正文应被服务端拒绝，got %v", err)
	}
}

func TestClient_RateLimitKeepsRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":"rate_limit_exceeded","message":"请求过于频繁"}`)
	}))
	defer server.Close()
	_, err := newClient(server.URL, testToken, 5).send(context.Background(), testDirectRoom, outgoingMessage{Status: "pending"}, "key")
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.RetryDelay() != time.Minute {
		t.Fatalf("限流等待提示丢失: %v", err)
	}
}

// TestClient_Edit 验证编辑会替换正文并留下编辑时间，且只能改自己发的消息。
func TestClient_Edit(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	mine := fake.pushMessage(testDirectRoom, testBotUserID, testBotUsername, "占位", time.Now())
	theirs := fake.pushMessage(testDirectRoom, "user-1", "alice", "别人的消息", time.Now())
	api := newTestClient(fake)
	ctx := context.Background()
	if _, err := api.edit(ctx, testDirectRoom, mine.ID, outgoingMessage{Content: "最终答案"}); err != nil {
		t.Fatal(err)
	}
	if records := fake.editRecords(); len(records) != 1 || records[0].Content != "最终答案" {
		t.Fatalf("编辑记录 = %+v", records)
	}
	if _, err := api.edit(ctx, testDirectRoom, theirs.ID, outgoingMessage{Content: "篡改"}); clientStatus(err) != 403 {
		t.Fatalf("编辑他人消息应报 403，got %v", err)
	}
	if _, err := api.edit(ctx, testDirectRoom, "msg-nope", outgoingMessage{Content: "内容"}); clientStatus(err) != 404 {
		t.Fatalf("编辑不存在消息应报 404，got %v", err)
	}
}

// TestClient_SetTyping 验证输入状态上报（204 无响应体也要当成成功）。
func TestClient_SetTyping(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	api := newTestClient(fake)
	if err := api.setTyping(context.Background(), testDirectRoom, true); err != nil {
		t.Fatal(err)
	}
	records := fake.typingRecords()
	if len(records) != 1 || !records[0].IsTyping || records[0].Conversation != testDirectRoom {
		t.Fatalf("typing 记录 = %+v", records)
	}
	if err := api.setTyping(context.Background(), testDirectRoom, false); err != nil {
		t.Fatal(err)
	}
	if len(fake.typingRecords()) != 2 {
		t.Fatalf("结束输入状态未上报: %+v", fake.typingRecords())
	}
}

// TestClient_Conversations 验证会话列表可读，供首次连接打水位基线。
func TestClient_Conversations(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	list, err := newTestClient(fake).conversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, conversation := range list {
		kinds[conversation.ID] = conversation.Kind
	}
	if kinds[testDirectRoom] != kindDirect || kinds[testGroupRoom] != kindRoom {
		t.Fatalf("会话列表 = %+v", list)
	}
}

// TestClient_OpenEvents 验证事件流可用，且凭据不对时在建立流之前就报错。
func TestClient_OpenEvents(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	body, err := newTestClient(fake).openEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newClient(fake.endpoint(), "wrong", 5).openEvents(context.Background()); clientStatus(err) != 401 {
		t.Fatalf("事件流鉴权失败应报 401，got %v", err)
	}
}

// TestClient_NetworkError 验证站点不可达时错误可判读（状态 0 表示该重试而不是放弃）。
func TestClient_NetworkError(t *testing.T) {
	t.Parallel()
	api := newClient("http://127.0.0.1:1", testToken, 2)
	_, err := api.profile(context.Background())
	if err == nil {
		t.Fatal("不可达地址应报错")
	}
	if got := clientStatus(err); got != 0 {
		t.Fatalf("clientStatus = %d, want 0", got)
	}
}
