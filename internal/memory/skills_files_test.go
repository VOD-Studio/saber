package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rua.plus/saber/internal/chat"
)

// writeSkillDir 在 root 下创建 `<name>/SKILL.md`，返回技能文件路径。
func writeSkillDir(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", name, err)
	}
	path := filepath.Join(dir, SkillFileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
	return path
}

func skillMarkdown(name, description, content string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + content + "\n"
}

func TestParseSkillFile(t *testing.T) {
	valid := "---\nname: deploy-service\ndescription: 部署服务并检查健康状态\n---\n\n1. 构建\n2. 发布\n"
	tests := []struct {
		name        string
		data        []byte
		wantErr     error
		wantName    string
		wantDesc    string
		wantContent string
	}{
		{
			name:        "标准格式",
			data:        []byte(valid),
			wantName:    "deploy-service",
			wantDesc:    "部署服务并检查健康状态",
			wantContent: "1. 构建\n2. 发布",
		},
		{
			name:        "BOM与CRLF换行",
			data:        append([]byte("\xef\xbb\xbf"), []byte("---\r\nname: crlf-skill\r\ndescription: Windows 换行\r\n---\r\n\r\n步骤\r\n")...),
			wantName:    "crlf-skill",
			wantDesc:    "Windows 换行",
			wantContent: "步骤",
		},
		{
			name:    "缺少frontmatter",
			data:    []byte("直接写正文\n"),
			wantErr: ErrSkillFileFrontmatter,
		},
		{
			name:    "frontmatter未闭合",
			data:    []byte("---\nname: no-close\ndescription: 缺失结束标记\n正文\n"),
			wantErr: ErrSkillFileFrontmatter,
		},
		{
			name:    "重复键",
			data:    []byte("---\nname: first\nname: second\ndescription: 重复\n---\n正文\n"),
			wantErr: ErrSkillFileFrontmatter,
		},
		{
			name:    "缺少描述",
			data:    []byte("---\nname: no-desc\n---\n正文\n"),
			wantErr: ErrSkillFileFrontmatter,
		},
		{
			name:    "非法名称",
			data:    []byte("---\nname: Bad/Name\ndescription: 非法\n---\n正文\n"),
			wantErr: ErrSkillFileFrontmatter,
		},
		{
			name:    "空正文",
			data:    []byte("---\nname: empty-body\ndescription: 空正文\n---\n\n   \n"),
			wantErr: ErrSkillFileBody,
		},
		{
			name:    "非UTF8内容",
			data:    []byte("---\nname: bad-utf8\ndescription: 编码\n---\n\xff\xfe\n"),
			wantErr: ErrSkillFileEncoding,
		},
		{
			name:    "非字符串描述",
			data:    []byte("---\nname: desc-map\ndescription: [a, b]\n---\n正文\n"),
			wantErr: ErrSkillFileFrontmatter,
		},
		{
			name:        "允许未知扩展键",
			data:        []byte("---\nname: ext-skill\ndescription: 兼容扩展\nlicense: MIT\nmetadata:\n  owner: sre\n---\n正文\n"),
			wantName:    "ext-skill",
			wantDesc:    "兼容扩展",
			wantContent: "正文",
		},
		{
			name:        "块状描述",
			data:        []byte("---\nname: block-desc\ndescription: |\n  第一行\n  第二行\n---\n正文\n"),
			wantName:    "block-desc",
			wantDesc:    "第一行\n第二行",
			wantContent: "正文",
		},
		{
			name:    "超过大小上限",
			data:    []byte("---\nname: too-large\ndescription: 超限\n---\n" + strings.Repeat("a", MaxSkillFileBytes)),
			wantErr: ErrSkillFileTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := ParseSkillFile(tt.data, "demo/SKILL.md")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseSkillFile() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSkillFile() error = %v", err)
			}
			if file.Name != tt.wantName || file.Description != tt.wantDesc || file.Content != tt.wantContent {
				t.Fatalf("ParseSkillFile() = %+v", file)
			}
		})
	}
}

func TestParseSkillFile_HidesAbsolutePath(t *testing.T) {
	secret := filepath.Join(string(filepath.Separator), "home", "alice", "secret", SkillFileName)
	_, err := ParseSkillFile([]byte("没有头\n"), secret)
	if err == nil {
		t.Fatal("ParseSkillFile() 未拒绝缺少 frontmatter 的内容")
	}
	if strings.Contains(err.Error(), "/home/alice") {
		t.Fatalf("错误信息泄露绝对路径: %v", err)
	}
	if !strings.Contains(err.Error(), SkillFileName) {
		t.Fatalf("错误信息应保留文件名: %v", err)
	}
}

func TestListSkillFiles(t *testing.T) {
	root := t.TempDir()
	writeSkillDir(t, root, "deploy", skillMarkdown("deploy", "发布服务", "步骤"))
	// 目录名合法但 frontmatter 名不一致时必须报错而不是静默改名。
	writeSkillDir(t, root, "mismatch", skillMarkdown("other-name", "名称不一致", "正文"))
	// 非技能子目录忽略，符号链接不跟随。
	writeSkillDir(t, root, "Not_A_Skill", skillMarkdown("Bad", "非法目录名", "正文"))
	outside := t.TempDir()
	writeSkillDir(t, outside, "linked", skillMarkdown("linked", "符号链接目标", "正文"))
	if err := os.Symlink(filepath.Join(outside, "linked"), filepath.Join(root, "linked")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	listings, errs := ListSkillFiles([]string{root})
	if len(listings) != 1 || listings[0].Name != "deploy" || listings[0].RelPath != "deploy/SKILL.md" {
		t.Fatalf("ListSkillFiles() = %+v", listings)
	}
	if listings[0].Dir != root {
		t.Fatalf("ListSkillFiles() Dir = %q, want %q", listings[0].Dir, root)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "mismatch/SKILL.md") {
		t.Fatalf("ListSkillFiles() errors = %v", errs)
	}
	if strings.Contains(errs[0].Error(), root) {
		t.Fatalf("错误信息泄露根目录: %v", errs[0])
	}
}

func TestService_ImportSkills(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")
	root := t.TempDir()
	writeSkillDir(t, root, "deploy", skillMarkdown("deploy", "发布服务", "步骤一"))
	writeSkillDir(t, root, "debug", skillMarkdown("debug", "排错流程", "步骤二"))

	result, err := svc.ImportSkills(ctx, user, root)
	if err != nil {
		t.Fatalf("ImportSkills() error = %v", err)
	}
	if result.Imported != 2 || result.Updated != 0 || result.Skipped != 0 || len(result.Errors) != 0 {
		t.Fatalf("首次导入结果 = %+v", result)
	}
	got, err := svc.GetSkill(ctx, user, "deploy")
	if err != nil || got.Description != "发布服务" || got.Content != "步骤一" {
		t.Fatalf("GetSkill() = %+v, err = %v", got, err)
	}

	result, err = svc.ImportSkills(ctx, user, root)
	if err != nil {
		t.Fatalf("ImportSkills() 第二次 error = %v", err)
	}
	if result.Imported != 0 || result.Updated != 0 || result.Skipped != 2 {
		t.Fatalf("重复导入结果 = %+v", result)
	}

	writeSkillDir(t, root, "deploy", skillMarkdown("deploy", "发布服务（新版）", "步骤一、二"))
	result, err = svc.ImportSkills(ctx, user, root)
	if err != nil {
		t.Fatalf("ImportSkills() 更新 error = %v", err)
	}
	if result.Imported != 0 || result.Updated != 1 || result.Skipped != 1 {
		t.Fatalf("更新导入结果 = %+v", result)
	}
	got, err = svc.GetSkill(ctx, user, "deploy")
	if err != nil || got.Version != 2 || got.Content != "步骤一、二" {
		t.Fatalf("更新后的技能 = %+v, err = %v", got, err)
	}
}

func TestService_ImportSkillsRequiresAbsoluteRoot(t *testing.T) {
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")
	for _, root := range []string{"", ".", "relative/skills"} {
		if _, err := svc.ImportSkills(context.Background(), user, root); err == nil {
			t.Fatalf("ImportSkills(%q) 未拒绝相对路径", root)
		}
	}
}

func TestService_ImportSkillsErrorMessageHidesRoot(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")
	root := t.TempDir()
	writeSkillDir(t, root, "broken", "缺少头部的正文\n")

	result, err := svc.ImportSkills(ctx, user, root)
	if err != nil {
		t.Fatalf("ImportSkills() error = %v", err)
	}
	if len(result.Errors) != 1 || result.Imported != 0 {
		t.Fatalf("导入结果 = %+v", result)
	}
	if strings.Contains(result.Errors[0].Error(), root) {
		t.Fatalf("错误信息泄露绝对路径: %v", result.Errors[0])
	}
}

func TestService_ImportSkillsGroupSuggestion(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{GroupWriter: func(chat.Identity) bool { return false }})
	room := roomIdentity("matrix", "@bot:x", "!room:x", "@alice:x", "")
	root := t.TempDir()
	writeSkillDir(t, root, "deploy", skillMarkdown("deploy", "发布服务", "步骤"))

	result, err := svc.ImportSkills(ctx, room, root)
	if err != nil {
		t.Fatalf("ImportSkills() error = %v", err)
	}
	if result.Imported != 0 || result.Suggested != 1 {
		t.Fatalf("群空间导入结果 = %+v", result)
	}
	if _, err := svc.GetSkill(ctx, room, "deploy"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("群空间技能不应直接生效: err = %v", err)
	}
	changes, err := svc.PendingSkillChanges(ctx, room)
	if err != nil || len(changes) != 1 || changes[0].Action != ActionAdd {
		t.Fatalf("PendingSkillChanges() = %+v, err = %v", changes, err)
	}
}

func TestService_SyncExternalSkills(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")
	first, second := t.TempDir(), t.TempDir()
	writeSkillDir(t, first, "deploy", skillMarkdown("deploy", "发布服务", "步骤"))
	writeSkillDir(t, second, "debug", skillMarkdown("debug", "排错流程", "步骤"))

	result, err := svc.SyncExternalSkills(ctx, user, []string{first, second})
	if err != nil {
		t.Fatalf("SyncExternalSkills() error = %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("SyncExternalSkills() 结果 = %+v", result)
	}
	skills, err := svc.ListSkills(ctx, user)
	if err != nil || len(skills) != 2 {
		t.Fatalf("ListSkills() = %+v, err = %v", skills, err)
	}
}

func TestService_ExportSkill(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.AddSkill(ctx, user, "deploy", "发布服务", "第一步\n第二步", Source{}); err != nil {
		t.Fatalf("AddSkill() error = %v", err)
	}
	root := t.TempDir()

	path, err := svc.ExportSkill(ctx, user, "deploy", root)
	if err != nil {
		t.Fatalf("ExportSkill() error = %v", err)
	}
	want := filepath.Join(root, "deploy", SkillFileName)
	if path != want {
		t.Fatalf("ExportSkill() path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	file, err := ParseSkillFile(data, "deploy/SKILL.md")
	if err != nil {
		t.Fatalf("ParseSkillFile(导出的文件) error = %v", err)
	}
	if file.Name != "deploy" || file.Description != "发布服务" || file.Content != "第一步\n第二步" {
		t.Fatalf("导出内容 = %+v", file)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("导出文件权限 = %v, err = %v", info.Mode().Perm(), err)
	}
	dirInfo, err := os.Stat(filepath.Join(root, "deploy"))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("导出目录权限 = %v, err = %v", dirInfo.Mode().Perm(), err)
	}

	// 导出的文件可以原样导入另一个空间。
	reader := userIdentity("matrix", "@bot:x", "@bob:x")
	result, err := svc.ImportSkills(ctx, reader, root)
	if err != nil || result.Imported != 1 {
		t.Fatalf("导出后导入结果 = %+v, err = %v", result, err)
	}
}

func TestService_ExportSkillRejectsTraversal(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, Config{})
	user := userIdentity("matrix", "@bot:x", "@alice:x")
	if _, err := svc.AddSkill(ctx, user, "deploy", "发布服务", "步骤", Source{}); err != nil {
		t.Fatalf("AddSkill() error = %v", err)
	}
	root := t.TempDir()
	tests := []struct {
		name string
		dir  string
	}{
		{name: "空目录", dir: ""},
		{name: "相对目录", dir: "relative/skills"},
		{name: "父目录穿越", dir: root + string(filepath.Separator) + ".."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.ExportSkill(ctx, user, "deploy", tt.dir); err == nil {
				t.Fatalf("ExportSkill(%q) 未拒绝非法目录", tt.dir)
			}
		})
	}
	for _, name := range []string{"../escape", "a/b", "", "..", "UPPER"} {
		if _, err := svc.ExportSkill(ctx, user, name, root); err == nil {
			t.Fatalf("ExportSkill(name=%q) 未拒绝非法名称", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("路径穿越写出了目录")
	}
}
