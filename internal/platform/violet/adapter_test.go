package violetplatform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"rua.plus/saber/internal/chat"
)

// newTestAdapterViaPlatform 用生产构造路径拿 adapter，顺带验证 account 解析一致。
func newTestAdapterViaPlatform(t *testing.T, fake *fakeViolet) (*Platform, *Adapter) {
	t.Helper()
	platform := New(newTestConfig(fake.endpoint()))
	if platform.adapter == nil {
		t.Fatal("adapter 未就绪")
	}
	return platform, platform.adapter
}

// testSession 返回属于本接入端的会话。
func testSession(adapter *Adapter, conversation string) chat.Session {
	return chat.Session{Platform: platformName, Account: adapter.account, Conversation: conversation}
}

// TestAdapter_Capabilities 声明的能力必须与真实现一致：说支持编辑却不能编辑，
// Presenter 就会只发一条占位消息再也不会更新。
func TestAdapter_Capabilities(t *testing.T) {
	t.Parallel()
	_, adapter := newTestAdapterViaPlatform(t, newFakeViolet(t))
	capabilities := adapter.Capabilities()
	if !capabilities.Edit || !capabilities.Typing || !capabilities.Reply {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}

// TestAdapter_Send 验证出站发送落到正确会话并带回可编辑的消息 ID。
func TestAdapter_Send(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	_, adapter := newTestAdapterViaPlatform(t, fake)
	messageID, err := adapter.Send(context.Background(), chat.Reply{
		Session:       testSession(adapter, testDirectRoom),
		Text:          "答案",
		ReplyTo:       "msg-1",
		TransactionID: "txn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if messageID == "" {
		t.Fatal("Send 必须返回平台消息 ID 供后续编辑")
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].Content != "答案" || sent[0].ReplyTo != "msg-1" || sent[0].Idempotency != "txn-1" {
		t.Fatalf("服务端收到 = %+v", sent)
	}
}

// TestAdapter_SendRejectsForeignSession 验证跨平台/跨账号的回复被拒：
// 拿别的平台的会话 ID 往 Violet 发，轻则发不进去，重则把答案投进陌生会话。
func TestAdapter_SendRejectsForeignSession(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	_, adapter := newTestAdapterViaPlatform(t, fake)
	ctx := context.Background()
	for _, session := range []chat.Session{
		{Platform: "matrix", Account: adapter.account, Conversation: testDirectRoom},
		{Platform: platformName, Account: "other.example.com", Conversation: testDirectRoom},
		{Platform: platformName, Account: adapter.account},
	} {
		if _, err := adapter.Send(ctx, chat.Reply{Session: session, Text: "答案"}); err == nil {
			t.Fatalf("陌生会话被接受: %+v", session)
		}
	}
	if _, err := adapter.Send(ctx, chat.Reply{Session: testSession(adapter, testDirectRoom), Text: "  \n "}); err == nil {
		t.Fatal("空白正文应被拒，而不是换一个服务端 400")
	}
	if err := adapter.Edit(ctx, "", chat.Reply{Session: testSession(adapter, testDirectRoom), Text: "答案"}); err == nil {
		t.Fatal("缺少消息 ID 的编辑应被拒")
	}
	if len(fake.sentMessages()) != 0 {
		t.Fatalf("被拒的请求不该打到服务端: %+v", fake.sentMessages())
	}
}

// TestAdapter_TruncatesOversizedContent 验证超长正文按字符截断后再发：
// 假服务端对超过上限的正文直接 400，能发成功就说明截断先起了作用。
func TestAdapter_TruncatesOversizedContent(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	_, adapter := newTestAdapterViaPlatform(t, fake)
	if _, err := adapter.Send(context.Background(), chat.Reply{
		Session: testSession(adapter, testDirectRoom),
		Text:    strings.Repeat("汉", maxContentRunes+3000),
	}); err != nil {
		t.Fatal(err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("出站记录 = %+v", sent)
	}
	if got := len([]rune(sent[0].Content)); got > maxContentRunes {
		t.Fatalf("截断后仍有 %d 个字符", got)
	}
	if !strings.HasSuffix(sent[0].Content, contentTruncatedSuffix) {
		t.Fatalf("缺少截断标记: %q", sent[0].Content[len(sent[0].Content)-24:])
	}
}

// TestAdapter_EditIsThrottled 验证出站编辑被压到最小间隔以上：Violet 的编辑配额按
// bot 用户全局计，超频的代价是整个平台被限流，比慢半拍严重得多。
func TestAdapter_EditIsThrottled(t *testing.T) {
	t.Parallel()
	fake := newFakeViolet(t)
	_, adapter := newTestAdapterViaPlatform(t, fake)
	created, err := adapter.Send(context.Background(), chat.Reply{Session: testSession(adapter, testDirectRoom), Text: "占位"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	started := time.Now()
	for round := 0; round < 3; round++ {
		if err := adapter.Edit(ctx, created, chat.Reply{Session: testSession(adapter, testDirectRoom), Text: "更新"}); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(started); elapsed < 2*testEditInterval {
		t.Fatalf("三次编辑只用了 %s，节流未生效（期望 ≥ %s）", elapsed, 2*testEditInterval)
	}
	if got := len(fake.editRecords()); got != 3 {
		t.Fatalf("编辑次数 = %d, want 3（节流应等待而非丢编辑，丢了就丢最终答案）", got)
	}
}

// TestEditThrottle 验证取消时机：等待中的编辑必须随 ctx 立刻退出，否则 Stop 会被拖住。
func TestEditThrottle(t *testing.T) {
	t.Parallel()
	throttle := &editThrottle{interval: time.Minute}
	if err := throttle.wait(context.Background()); err != nil {
		t.Fatal(err) // 首次编辑不受限
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := throttle.wait(cancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消的 ctx 应立刻返回错误，got %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("等待未被取消中断: %s", elapsed)
	}
	if err := (*editThrottle)(nil).wait(context.Background()); err != nil {
		t.Fatalf("零节流应直通: %v", err)
	}
}

// TestIdempotencyKey 验证幂等键的三条规则：上层给的键原样用（重发是同一条消息）、
// 缺省时自己生成、超长的键收敛到列宽内且稳定。
func TestIdempotencyKey(t *testing.T) {
	t.Parallel()
	if got := idempotencyKey(chat.Reply{TransactionID: "  txn-9  "}); got != "txn-9" {
		t.Fatalf("给定的事务 ID 被改写: %q", got)
	}
	first := idempotencyKey(chat.Reply{})
	second := idempotencyKey(chat.Reply{})
	if first == second || !strings.HasPrefix(first, "saber-") || len(first) > maxIdempotencyKeyBytes {
		t.Fatalf("随机幂等键不合要求: %q / %q", first, second)
	}
	long := strings.Repeat("x", maxIdempotencyKeyBytes*2)
	sum := sha256.Sum256([]byte(long))
	want := "saber-" + hex.EncodeToString(sum[:])
	if got := idempotencyKey(chat.Reply{TransactionID: long}); got != want || len(got) > maxIdempotencyKeyBytes {
		t.Fatalf("超长事务 ID 未收敛: %q", got)
	}
}
