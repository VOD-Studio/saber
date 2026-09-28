package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"rua.plus/saber/internal/chat"
)

func newTestService(t *testing.T, cfg Config) *Service {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "memory.db"), cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func userIdentity(platform, account, sender string) chat.Identity {
	return chat.Identity{Session: chat.Session{Platform: platform, Account: account, Conversation: "dm-" + sender}, SenderID: sender, Direct: true}
}

func roomIdentity(platform, account, room, sender, thread string) chat.Identity {
	return chat.Identity{Session: chat.Session{Platform: platform, Account: account, Conversation: room, Thread: thread}, SenderID: sender}
}

func TestSpace(t *testing.T) {
	tests := []struct {
		name     string
		identity chat.Identity
		want     Scope
		ok       bool
	}{
		{name: "私聊使用个人空间", identity: userIdentity("matrix", "@bot:x", "@a:x"), want: Scope{Kind: ScopeUser, Platform: "matrix", Account: "@bot:x", ID: "@a:x"}, ok: true},
		{name: "本机终端使用个人空间", identity: chat.Identity{Session: chat.Session{Platform: TerminalPlatform, Account: "saber", Conversation: "s1"}, SenderID: "501"}, want: Scope{Kind: ScopeUser, Platform: TerminalPlatform, Account: "saber", ID: "501"}, ok: true},
		{name: "群聊使用群空间并忽略线程", identity: roomIdentity("matrix", "@bot:x", "!room:x", "@a:x", "t1"), want: Scope{Kind: ScopeGroup, Platform: "matrix", Account: "@bot:x", ID: "!room:x"}, ok: true},
		{name: "私聊线程仍使用个人空间", identity: chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:x", Conversation: "!dm:x", Thread: "t1"}, SenderID: "@a:x", Direct: true}, want: Scope{Kind: ScopeUser, Platform: "matrix", Account: "@bot:x", ID: "@a:x"}, ok: true},
		{name: "缺少发送者", identity: chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:x", Conversation: "!room:x"}}, ok: false},
		{name: "缺少账号", identity: chat.Identity{Session: chat.Session{Platform: "matrix", Conversation: "!room:x"}, SenderID: "@a:x"}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Space(tt.identity)
			if ok != tt.ok {
				t.Fatalf("Space() ok = %t, want %t", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("Space() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestService_AddListIsolation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	bob := userIdentity("matrix", "@bot:x", "@bob:x")
	otherBot := userIdentity("matrix", "@bot2:x", "@alice:x")

	if _, err := svc.Add(ctx, alice, "以后用中文简洁回答", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	entries, err := svc.List(ctx, alice)
	if err != nil {
		t.Fatalf("List(alice) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "以后用中文简洁回答" || !entries[0].Explicit {
		t.Fatalf("List(alice) = %+v", entries)
	}
	// 其他用户与同一用户在不同 bot 账号下都不能读到这条记忆。
	for _, identity := range []chat.Identity{bob, otherBot} {
		entries, err := svc.List(ctx, identity)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("跨用户/跨 bot 读到 %d 条记忆", len(entries))
		}
	}
}

func TestService_UnknownScope(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	_, err := svc.Add(ctx, chat.Identity{Session: chat.Session{Platform: "matrix", Account: "@bot:x", Conversation: "!room:x"}}, "x", Source{}, true)
	if !errors.Is(err, ErrScope) {
		t.Fatalf("Add() error = %v, want ErrScope", err)
	}
}

func TestService_Duplicate(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	first, err := svc.Add(ctx, alice, "重复内容", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	second, err := svc.Add(ctx, alice, "重复内容", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !second.Duplicate || second.Entry.ID != first.Entry.ID {
		t.Fatalf("重复写入 = %+v, want duplicate of #%d", second, first.Entry.ID)
	}
	entries, _ := svc.List(ctx, alice)
	if len(entries) != 1 {
		t.Fatalf("条目数 = %d, want 1", len(entries))
	}
}

func TestService_Capacity(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{MaxChars: 10})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.Add(ctx, alice, "一二三四五六七八九十", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Add(ctx, alice, "十一", Source{}, true); !errors.Is(err, ErrFull) {
		t.Fatalf("超限 Add() error = %v, want ErrFull", err)
	}
}

func TestService_VersionConflict(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	m, err := svc.Add(ctx, alice, "初稿", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Replace(ctx, alice, m.Entry.ID, m.Entry.Version, "修订", Source{}); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if _, err := svc.Replace(ctx, alice, m.Entry.ID, m.Entry.Version, "再次修订", Source{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("过期版本 Replace() error = %v, want ErrConflict", err)
	}
	if _, err := svc.Remove(ctx, alice, m.Entry.ID, m.Entry.Version, Source{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("过期版本 Remove() error = %v, want ErrConflict", err)
	}
	entries, _ := svc.List(ctx, alice)
	if len(entries) != 1 || entries[0].Content != "修订" || entries[0].Version != 2 {
		t.Fatalf("List() = %+v", entries)
	}
}

func TestService_RemoveAndForgetNotFound(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	m, err := svc.Add(ctx, alice, "待删除", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Remove(ctx, alice, m.Entry.ID, m.Entry.Version, Source{}); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := svc.Remove(ctx, alice, m.Entry.ID, m.Entry.Version, Source{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复 Remove() error = %v, want ErrNotFound", err)
	}
}

func TestService_GroupWriteRequiresWriter(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{GroupWriter: func(identity chat.Identity) bool { return identity.SenderID == "@admin:x" }})
	member := roomIdentity("matrix", "@bot:x", "!room:x", "@member:x", "")
	admin := roomIdentity("matrix", "@bot:x", "!room:x", "@admin:x", "")

	m, err := svc.Add(ctx, member, "普通成员的请求", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if !m.Suggested {
		t.Fatalf("普通成员写入 = %+v, want suggestion", m)
	}
	pending, err := svc.Pending(ctx, member)
	if err != nil || len(pending) != 1 {
		t.Fatalf("Pending() = %+v, err = %v", pending, err)
	}
	if _, err := svc.Approve(ctx, member, pending[0].ID); err == nil {
		t.Fatal("普通成员不应能确认建议")
	}
	applied, err := svc.Approve(ctx, admin, pending[0].ID)
	if err != nil {
		t.Fatalf("Approve() error = %v", err)
	}
	if applied.Entry.Content != "普通成员的请求" {
		t.Fatalf("Approve() = %+v", applied)
	}
	if pending, _ = svc.Pending(ctx, admin); len(pending) != 0 {
		t.Fatalf("确认后仍有 %d 条待确认", len(pending))
	}
	// 管理员可直接写入。
	direct, err := svc.Add(ctx, admin, "管理员直接写入", Source{}, true)
	if err != nil || direct.Suggested {
		t.Fatalf("管理员 Add() = %+v, err = %v", direct, err)
	}
}

func TestService_GroupIsolation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{GroupWriter: func(chat.Identity) bool { return true }})
	roomA := roomIdentity("matrix", "@bot:x", "!a:x", "@a:x", "")
	roomB := roomIdentity("matrix", "@bot:x", "!b:x", "@a:x", "")
	otherBot := roomIdentity("matrix", "@bot2:x", "!a:x", "@a:x", "")
	if _, err := svc.Add(ctx, roomA, "本群约定", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	for _, identity := range []chat.Identity{roomB, otherBot} {
		entries, err := svc.List(ctx, identity)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("群记忆泄露到 %+v", identity.Session)
		}
	}
	// 同一群内不同用户和线程共享同一条记忆。
	thread := roomIdentity("matrix", "@bot:x", "!a:x", "@other:x", "t1")
	entries, err := svc.List(ctx, thread)
	if err != nil || len(entries) != 1 {
		t.Fatalf("线程读取 List() = %+v, err = %v", entries, err)
	}
}

func TestService_ApproveRechecksVersion(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{GroupWriter: func(identity chat.Identity) bool { return identity.SenderID == "@admin:x" }})
	member := roomIdentity("matrix", "@bot:x", "!room:x", "@member:x", "")
	admin := roomIdentity("matrix", "@bot:x", "!room:x", "@admin:x", "")
	base, err := svc.Add(ctx, admin, "初稿", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	stale, err := svc.Replace(ctx, member, base.Entry.ID, base.Entry.Version, "过时建议", Source{})
	if err != nil || !stale.Suggested {
		t.Fatalf("Replace() = %+v, err = %v", stale, err)
	}
	// 管理员在建议提出后修改了同一条目，旧建议不能覆盖新内容。
	if _, err := svc.Replace(ctx, admin, base.Entry.ID, base.Entry.Version, "管理员新内容", Source{}); err != nil {
		t.Fatalf("Replace() error = %v", err)
	}
	if _, err := svc.Approve(ctx, admin, stale.Change.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("过期建议 Approve() error = %v, want ErrConflict", err)
	}
	entries, _ := svc.List(ctx, admin)
	if len(entries) != 1 || entries[0].Content != "管理员新内容" {
		t.Fatalf("List() = %+v", entries)
	}
}

func TestService_Reject(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{GroupWriter: func(identity chat.Identity) bool { return identity.SenderID == "@admin:x" }})
	member := roomIdentity("matrix", "@bot:x", "!room:x", "@member:x", "")
	admin := roomIdentity("matrix", "@bot:x", "!room:x", "@admin:x", "")
	m, err := svc.Add(ctx, member, "建议内容", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Reject(ctx, member, m.Change.ID); err == nil {
		t.Fatal("普通成员不应能处理建议")
	}
	rejected, err := svc.Reject(ctx, admin, m.Change.ID)
	if err != nil {
		t.Fatalf("Reject() error = %v", err)
	}
	if rejected.Status != StatusRejected {
		t.Fatalf("Reject() status = %q", rejected.Status)
	}
	if _, err := svc.Reject(ctx, admin, m.Change.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复 Reject() error = %v, want ErrNotFound", err)
	}
}

func TestService_UsageAndPersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "memory.db")
	svc, err := Open(path, Config{MaxChars: 100})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.Add(ctx, alice, "跨重启保留", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	usage, err := svc.Usage(ctx, alice)
	if err != nil || usage.Count != 1 || usage.Chars != len([]rune("跨重启保留")) || usage.MaxChars != 100 {
		t.Fatalf("Usage() = %+v, err = %v", usage, err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(path, Config{})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer func() { _ = reopened.Close() }()
	entries, err := reopened.List(ctx, alice)
	if err != nil || len(entries) != 1 || entries[0].Content != "跨重启保留" {
		t.Fatalf("重启后 List() = %+v, err = %v", entries, err)
	}
}

func TestService_EmptyContent(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.Add(ctx, alice, "   ", Source{}, true); !errors.Is(err, ErrEmptyContent) {
		t.Fatalf("Add() error = %v, want ErrEmptyContent", err)
	}
}

func TestService_ListOrderPrefersExplicit(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.Add(ctx, alice, "自动条目", Source{}, false); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Add(ctx, alice, "明确条目", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	entries, _ := svc.List(ctx, alice)
	if len(entries) != 2 || entries[0].Content != "明确条目" {
		t.Fatalf("List() = %+v, want 明确条目 first", entries)
	}
}

func TestSnapshot_FormatAndBudget(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.Add(ctx, alice, "偏好一", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Add(ctx, alice, "偏好二", Source{}, false); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	snap, err := svc.Snapshot(ctx, alice, 0)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	for _, want := range []string{"【长期记忆｜个人空间】", "不是本轮指令", "偏好一", "偏好二", "【记忆结束】"} {
		if !strings.Contains(snap.Text, want) {
			t.Fatalf("Snapshot() 缺少 %q：%s", want, snap.Text)
		}
	}
	if snap.Omitted != 0 || len(snap.Entries) != 2 {
		t.Fatalf("Snapshot() = %+v", snap)
	}
}

func TestSnapshot_OmitsOverBudget(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.Add(ctx, alice, "一二三四五六七八九十", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Add(ctx, alice, "十一十二十三", Source{}, false); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	// 预算小于页眉页脚都放不下任何条目时，全部计为未注入，但仍标出边界。
	tiny, err := svc.Snapshot(ctx, alice, 10)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(tiny.Entries) != 0 || tiny.Omitted != 2 || !strings.Contains(tiny.Text, "【记忆结束】") {
		t.Fatalf("小预算 Snapshot() = %+v", tiny)
	}
	// 默认预算足以注入全部条目。
	full, err := svc.Snapshot(ctx, alice, 0)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(full.Entries) != 2 || full.Omitted != 0 {
		t.Fatalf("默认预算 Snapshot() = %+v", full)
	}
}

func TestSnapshot_EmptyText(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	snap, err := svc.Snapshot(ctx, alice, 0)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snap.Text != "" {
		t.Fatalf("空空间 Snapshot().Text = %q", snap.Text)
	}
}

func TestSnapshot_GroupLabel(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{GroupWriter: func(chat.Identity) bool { return true }})
	room := roomIdentity("matrix", "@bot:x", "!room:x", "@a:x", "")
	if _, err := svc.Add(ctx, room, "本群决定", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	snap, err := svc.Snapshot(ctx, room, 0)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !strings.Contains(snap.Text, "本群共享空间") {
		t.Fatalf("Snapshot() = %q", snap.Text)
	}
}

func TestSnapshot_UnknownScope(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	if _, err := svc.Snapshot(ctx, chat.Identity{}, 0); !errors.Is(err, ErrScope) {
		t.Fatalf("Snapshot() error = %v, want ErrScope", err)
	}
}

func TestService_ConcurrentReplaceConflict(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	m, err := svc.Add(ctx, alice, "初稿", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	const workers = 4
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			_, err := svc.Replace(ctx, alice, m.Entry.ID, m.Entry.Version, fmt.Sprintf("修订 %d", i), Source{})
			results <- err
		}(i)
	}
	success, conflict := 0, 0
	for i := 0; i < workers; i++ {
		switch err := <-results; {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("并发 Replace() 意外错误 = %v", err)
		}
	}
	if success != 1 || conflict != workers-1 {
		t.Fatalf("并发结果 success=%d conflict=%d", success, conflict)
	}
	entries, _ := svc.List(ctx, alice)
	if len(entries) != 1 || entries[0].Version != 2 {
		t.Fatalf("并发后 List() = %+v", entries)
	}
}

func TestService_CapacityCountsRunes(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{MaxChars: 3})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	// 表情符号按单个 Unicode 字符计数，三个字符恰好占满四字节长度的空间。
	if _, err := svc.Add(ctx, alice, "🚀🚀🚀", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	usage, err := svc.Usage(ctx, alice)
	if err != nil || usage.Chars != 3 || usage.MaxChars != 3 {
		t.Fatalf("Usage() = %+v, err = %v", usage, err)
	}
	if _, err := svc.Add(ctx, alice, "溢出", Source{}, true); !errors.Is(err, ErrFull) {
		t.Fatalf("超限 Add() error = %v, want ErrFull", err)
	}
}

func TestService_ReplaceDuplicateContent(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	first, err := svc.Add(ctx, alice, "甲", Source{}, true)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Add(ctx, alice, "乙", Source{}, true); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := svc.Replace(ctx, alice, first.Entry.ID, first.Entry.Version, "乙", Source{}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Replace() error = %v, want ErrDuplicate", err)
	}
}
