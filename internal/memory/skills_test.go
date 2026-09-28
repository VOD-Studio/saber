package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rua.plus/saber/internal/chat"
)

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "常规连字符命名", input: "k8s-pod-troubleshoot", wantErr: false},
		{name: "包含下划线与点", input: "git_rebase.flow", wantErr: false},
		{name: "包含数字", input: "v1.2.3", wantErr: false},
		{name: "纯英文字母", input: "deploy", wantErr: false},
		{name: "空名称", input: "", wantErr: true},
		{name: "超长名称", input: strings.Repeat("a", 65), wantErr: true},
		{name: "首字符为连字符", input: "-deploy", wantErr: true},
		{name: "首字符为点", input: ".deploy", wantErr: true},
		{name: "首字符为下划线", input: "_deploy", wantErr: true},
		{name: "包含大写字母", input: "DeployApp", wantErr: true},
		{name: "包含空格", input: "deploy app", wantErr: true},
		{name: "包含特殊符号", input: "deploy#app", wantErr: true},
		{name: "包含中文", input: "部署服务", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSkillName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSkillName(%q) err = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestService_SkillScopeIsolation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})

	alice := userIdentity("matrix", "@bot:x", "@alice:x")
	bob := userIdentity("matrix", "@bot:x", "@bob:x")
	room1 := roomIdentity("matrix", "@bot:x", "!room1:x", "@alice:x", "")
	room2 := roomIdentity("matrix", "@bot:x", "!room2:x", "@alice:x", "")

	// Alice 在个人空间添加技能
	mut, err := svc.AddSkill(ctx, alice, "k8s-debug", "排查 Pod 重启与 CrashLoopBackOff", "1. 查看状态\n2. 检查日志", Source{Task: 101})
	if err != nil {
		t.Fatalf("AddSkill(alice) error = %v", err)
	}
	if mut.Suggested || mut.Duplicate || mut.Skill.Version != 1 {
		t.Fatalf("AddSkill(alice) unexpected mutation: %+v", mut)
	}

	// Alice 能够读取该技能
	skill, err := svc.GetSkill(ctx, alice, "k8s-debug")
	if err != nil {
		t.Fatalf("GetSkill(alice) error = %v", err)
	}
	if skill.Name != "k8s-debug" || skill.Description != "排查 Pod 重启与 CrashLoopBackOff" {
		t.Fatalf("GetSkill(alice) = %+v", skill)
	}

	// Bob 在个人空间无法读取 Alice 的技能
	_, err = svc.GetSkill(ctx, bob, "k8s-debug")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("GetSkill(bob) err = %v, want ErrSkillNotFound", err)
	}
	bobList, err := svc.ListSkills(ctx, bob)
	if err != nil || len(bobList) != 0 {
		t.Fatalf("ListSkills(bob) = %+v, err = %v", bobList, err)
	}

	// Alice 在群聊 room1 也读不到私聊个人技能
	_, err = svc.GetSkill(ctx, room1, "k8s-debug")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("GetSkill(room1) err = %v, want ErrSkillNotFound", err)
	}

	// 带管理员权限的服务：为 room1 添加群技能
	adminSvc := newTestService(t, Config{
		GroupWriter: func(id chat.Identity) bool {
			return id.Session.Conversation == "!room1:x"
		},
	})
	if _, err := adminSvc.AddSkill(ctx, room1, "deploy-ci", "群专属部署发布流程", "CI/CD 步骤", Source{Task: 102}); err != nil {
		t.Fatalf("adminSvc.AddSkill(room1) error = %v", err)
	}

	// room1 可以读取 deploy-ci
	if _, err := adminSvc.GetSkill(ctx, room1, "deploy-ci"); err != nil {
		t.Fatalf("adminSvc.GetSkill(room1) error = %v", err)
	}

	// room2 无法读取 room1 的技能
	if _, err := adminSvc.GetSkill(ctx, room2, "deploy-ci"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("adminSvc.GetSkill(room2) err = %v, want ErrSkillNotFound", err)
	}
}

func TestService_SkillOptimisticLockingAndCAS(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@dev:x")

	// 1. 添加技能 (v1)
	mut, err := svc.AddSkill(ctx, user, "git-rebase", "Git 变基排错", "步骤 1", Source{Task: 201})
	if err != nil {
		t.Fatalf("AddSkill error = %v", err)
	}
	if mut.Skill.Version != 1 {
		t.Fatalf("expected v1, got v%d", mut.Skill.Version)
	}

	// 2. 相同内容重复添加，返回 Duplicate
	dupMut, err := svc.AddSkill(ctx, user, "git-rebase", "Git 变基排错", "步骤 1", Source{Task: 202})
	if err != nil {
		t.Fatalf("AddSkill duplicate error = %v", err)
	}
	if !dupMut.Duplicate || dupMut.Skill.ID != mut.Skill.ID {
		t.Fatalf("expected duplicate, got %+v", dupMut)
	}

	// 3. 不同内容但同名添加，返回 ErrSkillDuplicate
	_, err = svc.AddSkill(ctx, user, "git-rebase", "不同描述", "步骤 1", Source{Task: 203})
	if !errors.Is(err, ErrSkillDuplicate) {
		t.Fatalf("expected ErrSkillDuplicate, got %v", err)
	}

	// 4. 正确版本修改 (v1 -> v2)
	mut2, err := svc.ReplaceSkill(ctx, user, "git-rebase", 1, "Git 变基与冲突解决", "步骤 1\n步骤 2", Source{Task: 204})
	if err != nil {
		t.Fatalf("ReplaceSkill error = %v", err)
	}
	if mut2.Skill.Version != 2 {
		t.Fatalf("expected v2, got v%d", mut2.Skill.Version)
	}

	// 5. 过期版本修改 (仍用 v1 尝试修改)，触发 CAS 冲突
	_, err = svc.ReplaceSkill(ctx, user, "git-rebase", 1, "试图用旧版本覆盖", "步骤 3", Source{Task: 205})
	if !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("expected ErrSkillConflict, got %v", err)
	}

	// 6. 过期版本删除 (用 v1 尝试删除)，触发 CAS 冲突
	_, err = svc.RemoveSkill(ctx, user, "git-rebase", 1, Source{Task: 206})
	if !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("expected ErrSkillConflict, got %v", err)
	}

	// 7. 正确版本删除 (v2)
	remMut, err := svc.RemoveSkill(ctx, user, "git-rebase", 2, Source{Task: 207})
	if err != nil {
		t.Fatalf("RemoveSkill error = %v", err)
	}
	if remMut.Skill.Version != 2 {
		t.Fatalf("expected removed skill v2, got v%d", remMut.Skill.Version)
	}

	// 8. 确认技能已删除
	_, err = svc.GetSkill(ctx, user, "git-rebase")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound, got %v", err)
	}

	// 9. 确认被删除的 source_task 被记入排除黑名单
	var excludedCount int
	err = svc.store.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_excluded_sources WHERE scope_key=? AND task_id=201`, mut.Skill.Scope.Key()).Scan(&excludedCount)
	if err != nil || excludedCount != 1 {
		t.Fatalf("expected source_task 201 in memory_excluded_sources, count = %d, err = %v", excludedCount, err)
	}
}

func TestService_SkillGroupProposalsAndApproval(t *testing.T) {
	ctx := context.Background()
	room := roomIdentity("matrix", "@bot:x", "!group:x", "@member:x", "")
	admin := roomIdentity("matrix", "@bot:x", "!group:x", "@admin:x", "")

	// 仅 @admin:x 拥有写权限
	svc := newTestService(t, Config{
		GroupWriter: func(id chat.Identity) bool {
			return id.SenderID == "@admin:x"
		},
	})

	// 1. 普通成员添加技能，自动降级为待确认建议
	mut, err := svc.AddSkill(ctx, room, "ci-test", "自动化测试流", "go test -tags goolm", Source{Task: 301})
	if err != nil {
		t.Fatalf("AddSkill(member) error = %v", err)
	}
	if !mut.Suggested || mut.Change.Status != StatusPending || mut.Change.ID == 0 {
		t.Fatalf("expected pending change, got %+v", mut)
	}

	// 此时技能尚未生效
	_, err = svc.GetSkill(ctx, room, "ci-test")
	if !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("expected ErrSkillNotFound before approve, got %v", err)
	}

	// 查看待确认列表
	pending, err := svc.PendingSkillChanges(ctx, room)
	if err != nil || len(pending) != 1 || pending[0].ID != mut.Change.ID {
		t.Fatalf("PendingSkillChanges = %+v, err = %v", pending, err)
	}

	// 普通成员无权审批
	_, err = svc.ApproveSkillChange(ctx, room, mut.Change.ID)
	if err == nil {
		t.Fatal("expected unauthorized error when member approves")
	}

	// 管理员审批通过
	approvedSkill, err := svc.ApproveSkillChange(ctx, admin, mut.Change.ID)
	if err != nil {
		t.Fatalf("ApproveSkillChange error = %v", err)
	}
	if approvedSkill.Name != "ci-test" || approvedSkill.Version != 1 {
		t.Fatalf("approvedSkill = %+v", approvedSkill)
	}

	// 此时技能已生效
	skill, err := svc.GetSkill(ctx, room, "ci-test")
	if err != nil || skill.Version != 1 {
		t.Fatalf("GetSkill = %+v, err = %v", skill, err)
	}

	// 2. 普通成员提议修改 (ExpectedVersion = 1)
	editMut, err := svc.ReplaceSkill(ctx, room, "ci-test", 1, "更新测试流", "go test -v -tags goolm", Source{Task: 302})
	if err != nil || !editMut.Suggested {
		t.Fatalf("ReplaceSkill(member) error = %v, mut = %+v", err, editMut)
	}

	// 在管理员审批 editMut 之前，管理员在群里直接修改了技能 (v1 -> v2)
	adminMut, err := svc.ReplaceSkill(ctx, admin, "ci-test", 1, "管理员直接优化", "make test", Source{Task: 303})
	if err != nil || adminMut.Skill.Version != 2 {
		t.Fatalf("ReplaceSkill(admin) error = %v, mut = %+v", err, adminMut)
	}

	// 管理员现在尝试审批之前期望 v1 的 editMut -> 应该触发版本冲突被拒绝！
	_, err = svc.ApproveSkillChange(ctx, admin, editMut.Change.ID)
	if !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("expected ErrSkillConflict when approving stale proposal, got %v", err)
	}

	// 3. 普通成员提议删除，管理员予以驳回
	delMut, err := svc.RemoveSkill(ctx, room, "ci-test", 2, Source{Task: 304})
	if err != nil || !delMut.Suggested {
		t.Fatalf("RemoveSkill(member) error = %v, mut = %+v", err, delMut)
	}

	rejectedChange, err := svc.RejectSkillChange(ctx, admin, delMut.Change.ID)
	if err != nil {
		t.Fatalf("RejectSkillChange error = %v", err)
	}
	if rejectedChange.Status != StatusRejected {
		t.Fatalf("expected rejected status, got %s", rejectedChange.Status)
	}

	// 被驳回的任务也应被记入排除黑名单
	var rejectedCount int
	roomScope, _ := Space(room)
	err = svc.store.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_excluded_sources WHERE scope_key=? AND task_id=304`, roomScope.Key()).Scan(&rejectedCount)
	if err != nil || rejectedCount != 1 {
		t.Fatalf("expected task 304 in excluded sources, count = %d, err = %v", rejectedCount, err)
	}
}

func TestService_SkillAutoMethods(t *testing.T) {
	ctx := context.Background()
	user := userIdentity("matrix", "@bot:x", "@user:x")
	room := roomIdentity("matrix", "@bot:x", "!room:x", "@user:x", "")
	svc := newTestService(t, Config{})

	// 个人空间：AutoAddSkill 直接生效
	uMut, err := svc.AutoAddSkill(ctx, user, "debug-flow", "排错流程", "排错步骤", Source{Task: 401})
	if err != nil || uMut.Suggested || uMut.Skill.Version != 1 {
		t.Fatalf("AutoAddSkill(user) error = %v, mut = %+v", err, uMut)
	}

	// 个人空间：AutoReplaceSkill 转为待确认建议（防静默破坏已有流程）
	uRepMut, err := svc.AutoReplaceSkill(ctx, user, "debug-flow", 1, "新排错流程", "新步骤", Source{Task: 402})
	if err != nil || !uRepMut.Suggested {
		t.Fatalf("AutoReplaceSkill(user) error = %v, mut = %+v", err, uRepMut)
	}

	// 个人空间：AutoRemoveSkill 转为待确认建议
	uDelMut, err := svc.AutoRemoveSkill(ctx, user, "debug-flow", 1, Source{Task: 403})
	if err != nil || !uDelMut.Suggested {
		t.Fatalf("AutoRemoveSkill(user) error = %v, mut = %+v", err, uDelMut)
	}

	// 群空间：所有 Auto 方法均转为待确认建议
	gMut, err := svc.AutoAddSkill(ctx, room, "group-workflow", "团队流程", "步骤", Source{Task: 404})
	if err != nil || !gMut.Suggested {
		t.Fatalf("AutoAddSkill(room) error = %v, mut = %+v", err, gMut)
	}
}

func TestService_SkillCatalogSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")

	// 1. 无技能时返回空字符串
	snap, err := svc.SkillCatalogSnapshot(ctx, user, 4096)
	if err != nil || snap != "" {
		t.Fatalf("empty snapshot = %q, err = %v", snap, err)
	}

	// 2. 添加若干技能
	_, _ = svc.AddSkill(ctx, user, "alpha", "Alpha 部署流程", "内容 A", Source{})
	_, _ = svc.AddSkill(ctx, user, "beta", "Beta 排错指南", "内容 B", Source{})

	// 3. 正常预算注入快照
	snap, err = svc.SkillCatalogSnapshot(ctx, user, 4096)
	if err != nil {
		t.Fatalf("SkillCatalogSnapshot error = %v", err)
	}
	if !strings.Contains(snap, "【可用技能目录") || !strings.Contains(snap, "【技能目录结束】") {
		t.Fatalf("snapshot missing header or footer: %s", snap)
	}
	if !strings.Contains(snap, "- alpha: Alpha 部署流程 (v1)") || !strings.Contains(snap, "- beta: Beta 排错指南 (v1)") {
		t.Fatalf("snapshot missing skill items: %s", snap)
	}
	// 正文绝不出现在目录快照中
	if strings.Contains(snap, "内容 A") || strings.Contains(snap, "内容 B") {
		t.Fatalf("snapshot should not contain full content: %s", snap)
	}

	// 4. 极端预算测试：预算小于页眉页脚最小长度时返回空
	snapTiny, err := svc.SkillCatalogSnapshot(ctx, user, 10)
	if err != nil || snapTiny != "" {
		t.Fatalf("tiny budget snapshot = %q, err = %v", snapTiny, err)
	}

	// 5. 预算截断测试：刚好容纳第一个技能，容不下第二个
	singleLineLen := len("- alpha: Alpha 部署流程 (v1)")
	headerFooterLen := len("【可用技能目录（私聊 @alice:x，按需使用 saber_skill 读取详情）】\n") + len("\n【技能目录结束】")
	budget := headerFooterLen + singleLineLen + 2
	snapTrunc, err := svc.SkillCatalogSnapshot(ctx, user, budget)
	if err != nil {
		t.Fatalf("truncated snapshot error = %v", err)
	}
	if !strings.Contains(snapTrunc, "alpha") {
		t.Fatalf("truncated snapshot should contain alpha: %s", snapTrunc)
	}
	if strings.Contains(snapTrunc, "beta") {
		t.Fatalf("truncated snapshot should not contain beta due to budget: %s", snapTrunc)
	}
}
