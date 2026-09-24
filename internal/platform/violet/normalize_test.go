package violetplatform

import (
	"strings"
	"testing"
	"time"
)

// TestStripMentions 验证提及 token 只保留可读用户名，其余正文一字不动。
func TestStripMentions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"点名提问", "@(saber:11111111-1111-1111-1111-111111111111) 今天天气如何", "@saber 今天天气如何"},
		{"全体提及", "@(all:all) 都来看", "@all 都来看"},
		{"多个提及", "@(a:1) 和 @(b:2) 说说", "@a 和 @b 说说"},
		{"无提及", "普通消息", "普通消息"},
		{"括号正文", "看这里 (不是 mention)", "看这里 (不是 mention)"},
		{"表情 token 保持原样", "好的 [mycat:2f][x:y]", "好的 [mycat:2f][x:y]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripMentions(tc.in); got != tc.want {
				t.Fatalf("stripMentions = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMentionsUser 验证用户名与用户 ID 任一命中即算点名本 bot，且大小写不敏感。
func TestMentionsUser(t *testing.T) {
	t.Parallel()
	const mentioned = "@(SaBeR:6f1e-uuid) 在吗"
	const plain = "今天天气如何"
	for _, tc := range []struct {
		name, content, username, userID string
		want                            bool
	}{
		{name: "用户名命中", content: mentioned, username: "saber", want: true},
		{name: "ID 命中", content: mentioned, username: "other", userID: "6F1E-UUID", want: true},
		{name: "都不命中", content: mentioned, username: "other", userID: "nope", want: false},
		{name: "空身份不算命中", content: mentioned, want: false},
		{name: "正文没有 token", content: plain, username: "saber", userID: "6f1e-uuid", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mentionsUser(tc.content, tc.username, tc.userID); got != tc.want {
				t.Fatalf("mentionsUser = %v, want %v", got, tc.want)
			}
		})
	}
	if !hasAnyMention(mentioned) {
		t.Fatal("hasAnyMention 应识别出提及")
	}
	if hasAnyMention(plain) {
		t.Fatal("hasAnyMention 误判")
	}
}

// TestMentionResidue 验证「只 @ 不提问」能被识别成空内容。
func TestMentionResidue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"只点名", "@(saber:6f1e) ", ""},
		{"点名带正文", "@(saber:6f1e) 讲个笑话", "讲个笑话"},
		{"全体提及带正文", "@(all:all) 都来看", "都来看"},
		{"无提及", "今天天气如何", "今天天气如何"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mentionResidue(tc.in); got != tc.want {
				t.Fatalf("mentionResidue(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTruncateContent 验证按 Unicode 字符截断并保留截断标记，未超长时原样返回。
func TestTruncateContent(t *testing.T) {
	t.Parallel()
	short, truncated := truncateContent("短正文")
	if truncated || short != "短正文" {
		t.Fatalf("短正文被改动: %q, %v", short, truncated)
	}
	long, truncated := truncateContent(strings.Repeat("汉", maxContentRunes+500))
	if !truncated {
		t.Fatal("超长正文未标记截断")
	}
	if got := len([]rune(long)); got > maxContentRunes {
		t.Fatalf("截断后仍有 %d 个字符，超过上限 %d", got, maxContentRunes)
	}
	if !strings.HasSuffix(long, contentTruncatedSuffix) {
		t.Fatalf("截断后缺少标记: %q", long[len(long)-20:])
	}
	if !strings.HasPrefix(long, "汉汉汉") {
		t.Fatal("截断把正文头部切坏了")
	}
}

// TestParseTimestamp 验证两种 RFC3339 形态都能解，垃圾输入不冒充有效时间。
func TestParseTimestamp(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"2026-09-22T10:00:00Z", "2026-09-22T18:00:00.123456+08:00"} {
		if _, ok := parseTimestamp(value); !ok {
			t.Fatalf("无法解析时间戳 %q", value)
		}
	}
	for _, value := range []string{"", "not-a-time", "2026-09-22 10:00:00"} {
		if _, ok := parseTimestamp(value); ok {
			t.Fatalf("无效时间戳被接受: %q", value)
		}
	}
}

// TestAddressedCommand 验证群聊命令只接受正文开头精确寻址的 bot 用户 ID。
func TestAddressedCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in, want     string
		command, addressed bool
	}{
		{name: "私聊裸命令", in: "/task list", want: "/task list", command: true},
		{name: "精确寻址", in: "@(saber:bot-id) /task status @(other:other-id)", want: "/task status @(other:other-id)", command: true, addressed: true},
		{name: "提及后的 NBSP", in: "@(saber:bot-id)\u00a0/task list", want: "/task list", command: true, addressed: true},
		{name: "提及后的转义斜杠", in: "@(saber:bot-id) //task list", want: "//task list", addressed: true},
		{name: "其他 bot", in: "@(other:other-id) /task list @(saber:bot-id)", want: "/task list @(saber:bot-id)", command: true},
		{name: "用户名冒充", in: "@(saber:other-id) !ai hello", want: "!ai hello", command: true},
		{name: "正文斜杠", in: "看一下 /task list"},
		{name: "转义斜杠", in: "//task list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, command, addressed := addressedCommand(tc.in, "bot-id")
			if command != tc.command || addressed != tc.addressed || got != tc.want {
				t.Fatalf("addressedCommand(%q) = %q,%v,%v, want %q,%v,%v", tc.in, got, command, addressed, tc.want, tc.command, tc.addressed)
			}
		})
	}
}

// TestSeenMessages 验证去重只放行首次出现，并且容量有界。
func TestSeenMessages(t *testing.T) {
	t.Parallel()
	seen := newSeenMessages(3)
	if !seen.add("a") {
		t.Fatal("首次出现应放行")
	}
	if seen.add("a") {
		t.Fatal("重复消息应被拦下")
	}
	if !seen.add("") {
		t.Fatal("空 ID 应交由调用方处理，不当作重复")
	}
	for _, id := range []string{"b", "c", "d"} {
		seen.add(id)
	}
	if got := seen.size(); got != 3 {
		t.Fatalf("去重集大小 = %d, want 3（最旧的应被淘汰）", got)
	}
	if !seen.add("a") {
		t.Fatal("被淘汰的 ID 应重新放行，容量有界优先于记住一切")
	}
}

// TestConversationStates 验证会话记忆按 ID 唯一、已知名单与容量淘汰。
func TestConversationStates(t *testing.T) {
	t.Parallel()
	states := newConversationStates(2)
	first := states.get("a")
	if again := states.get("a"); again != first {
		t.Fatal("同一会话应返回同一份记忆")
	}
	states.get("b")
	states.get("c")
	known := states.known()
	if len(known) != 2 {
		t.Fatalf("known = %v, want 2 项", known)
	}
	for _, id := range known {
		if id == "a" {
			t.Fatal("最旧的会话应已被淘汰")
		}
	}
}

// TestConversationMemory_Watermark 验证基线与水位推进：新会话只打基线、不回放的判定靠它。
func TestConversationMemory_Watermark(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	memory := &conversationMemory{}
	if since, baselined := memory.since(now); since != now || baselined {
		t.Fatalf("首次 since 应打基线并报告未基线化: %v, %v", since, baselined)
	}
	if since, baselined := memory.since(now); !baselined || since != now {
		t.Fatalf("第二次 since 应返回基线: %v, %v", since, baselined)
	}
	later := now.Add(time.Minute)
	memory.noteSeen(later)
	if since, _ := memory.since(now); !since.Equal(later) {
		t.Fatalf("水位未推进: %v", since)
	}
	memory.noteSeen(now) // 乱序的更早消息不该把水位拉回去
	if since, _ := memory.since(now); !since.Equal(later) {
		t.Fatalf("水位被更早消息回退: %v", since)
	}
}

// TestConversationMemory_CachedKind 验证形态缓存的有效期与过期后仍可读旧值。
func TestConversationMemory_CachedKind(t *testing.T) {
	t.Parallel()
	now := time.Now()
	memory := &conversationMemory{}
	if _, fresh := memory.cachedKind(now); fresh {
		t.Fatal("未写入时不应有缓存")
	}
	memory.rememberKind(kindDirect, now.Add(time.Minute))
	if kind, fresh := memory.cachedKind(now.Add(time.Second)); !fresh || kind != kindDirect {
		t.Fatalf("有效期内应命中缓存: %q, %v", kind, fresh)
	}
	if _, fresh := memory.cachedKind(now.Add(2 * time.Minute)); fresh {
		t.Fatal("过期后不应命中缓存")
	}
	if stored := memory.storedKind(); stored != kindDirect {
		t.Fatalf("storedKind = %q, want 仍可用于降级判断", stored)
	}
}
