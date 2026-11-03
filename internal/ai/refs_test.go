package ai

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/execution"
)

// referenceService 构造带真实 local 后端执行器的服务与工作区。
func referenceService(t *testing.T) (*Service, context.Context, string) {
	t.Helper()
	identity := chat.Identity{Session: chat.Session{Platform: "matrix", Account: "bot", Conversation: "room"}, SenderID: "alice"}
	dir := t.TempDir()
	cfg := config.ExecutionConfig{
		Enabled: true,
		Backend: "local",
		LogDir:  t.TempDir(),
		Workspaces: map[string]config.WorkspaceConfig{
			"project": {Path: dir},
		},
		Grants: []config.ExecutionGrant{{
			Platform: "matrix", Account: "bot", Room: "room", Users: []string{"alice"},
			Workspace: "project", Tools: []string{"read_file", "write_file"},
		}},
	}
	e, err := execution.New(cfg, nil, nil)
	require.NoError(t, err)
	ctx := chat.WithIdentity(context.Background(), identity)
	return &Service{executor: e}, ctx, dir
}

func TestReferenceTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"简单路径", "看 @notes.txt", []string{"notes.txt"}},
		{"行首引用", "@a/b.go 解释", []string{"a/b.go"}},
		{"带空格引号", `读 @"docs/my file.md" 并总结`, []string{"docs/my file.md"}},
		{"标点结尾", "看 @main.go，然后", []string{"main.go"}},
		{"括号包裹", "（@main.go）", []string{"main.go"}},
		{"邮箱不是引用", "mail@example.com", nil},
		{"未闭合引号保持原文", `看 @"unclosed`, nil},
		{"空路径", "只有 @ 和 @\"\"", nil},
		{"多个引用", "@a.go 与 @b.go", []string{"a.go", "b.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, token := range referenceTokens(tc.text) {
				got = append(got, token.Path)
			}
			require.Equal(t, tc.want, got)
		})
	}
}

func TestExpandReferences_AppendsWorkspaceFile(t *testing.T) {
	service, ctx, dir := referenceService(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello\n"), 0600))

	message := chat.Message{Text: "看下 @notes.txt 然后总结"}
	got := service.expandReferences(ctx, message)
	require.True(t, strings.HasPrefix(got.Text, "看下 @notes.txt 然后总结"), "必须保留原消息文本")
	require.Contains(t, got.Text, `<file path="notes.txt">`)
	require.Contains(t, got.Text, "hello")
}

func TestExpandReferences_KeepsOriginalOnSkip(t *testing.T) {
	service, ctx, dir := referenceService(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0x00, 0x01}, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.txt"), []byte{0xff, 0xfe}, 0600))

	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"不存在", "看 @missing.txt", ""},
		{"邮箱", "mail@example.com", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := service.expandReferences(ctx, chat.Message{Text: tc.text})
			require.Equal(t, tc.text, got.Text)
		})
	}
	for _, tc := range []struct {
		name string
		text string
		note string
	}{
		{"目录", "看 @sub", "跳过引用 @sub"},
		{"二进制", "看 @binary.bin", "跳过引用 @binary.bin"},
		{"非法 UTF-8", "看 @bad.txt", "跳过引用 @bad.txt"},
		{"越界", "看 @../escape.txt", "跳过引用 @../escape.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := service.expandReferences(ctx, chat.Message{Text: tc.text})
			require.True(t, strings.HasPrefix(got.Text, tc.text))
			require.Contains(t, got.Text, tc.note)
			require.NotContains(t, got.Text, "<file")
		})
	}
}

func TestExpandReferences_Limits(t *testing.T) {
	service, ctx, dir := referenceService(t)
	var refs []string
	for i := range 10 {
		name := "file" + string(rune('a'+i)) + ".txt"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0600))
		refs = append(refs, "@"+name)
	}
	got := service.expandReferences(ctx, chat.Message{Text: "读取 " + strings.Join(refs, " ")})
	require.Equal(t, maxReferenceFiles, strings.Count(got.Text, "<file path="))
	require.Contains(t, got.Text, "最多引用 8 个文件")

	// 合计上限：第三个 32 KiB 文件超出剩余额度时跳过并说明。
	for _, name := range []string{"big1.txt", "big2.txt", "big3.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("y", 32<<10)), 0600))
	}
	got = service.expandReferences(ctx, chat.Message{Text: "看 @big1.txt @big2.txt @big3.txt"})
	require.Equal(t, 2, strings.Count(got.Text, "<file path="))
	require.Contains(t, got.Text, "超出合计 64 KiB 限制")
}

func TestExpandReferences_RequiresWorkspace(c *testing.T) {
	service, _, _ := referenceService(c)
	other := chat.WithIdentity(context.Background(), chat.Identity{SenderID: "bob"})
	message := chat.Message{Text: "看 @notes.txt"}
	require.Equal(c, message.Text, service.expandReferences(other, message).Text)
	require.Equal(c, message.Text, (&Service{}).expandReferences(context.Background(), message).Text)
}
