package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

func fileFixture(t *testing.T) (*Executor, context.Context, string) {
	t.Helper()
	cfg, identity := fixture(t)
	cfg.Grants[0].Tools = append(cfg.Grants[0].Tools, "read_output", "search_files", "glob_files")
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 7, dir)
	e.SetOutputAuthorizer(func(context.Context, int64) error { return nil })
	return e, ctx, dir
}

func dataOf(t *testing.T, result Result) map[string]any {
	t.Helper()
	data, ok := result.Data.(map[string]any)
	require.True(t, ok, "expected structured data, got %#v", result.Data)
	return data
}

func TestReadFile_PagesWholeFile(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	var content strings.Builder
	for i := range 50 {
		content.WriteString("line ")
		content.WriteString(strings.Repeat("x", i%7))
		content.WriteString("\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content.String()), 0600))
	var collected strings.Builder
	offset, byteOffset := 0, 0
	for pages := 0; ; pages++ {
		require.Less(t, pages, 100)
		result, err := e.Run(ctx, "read_file", map[string]any{"path": "f.txt", "offset": float64(offset), "byte_offset": float64(byteOffset), "limit": float64(7)})
		require.NoError(t, err)
		data := dataOf(t, result)
		collected.WriteString(data["text"].(string))
		offset = data["next_offset"].(int)
		byteOffset = data["next_byte_offset"].(int)
		if data["eof"].(bool) {
			break
		}
	}
	require.Equal(t, content.String(), collected.String())
}

func TestReadFile_TrailingAndMissingNewline(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb"), 0600))
	result, err := e.Run(ctx, "read_file", map[string]any{"path": "f.txt", "limit": float64(1)})
	require.NoError(t, err)
	data := dataOf(t, result)
	require.Equal(t, "a\n", data["text"])
	require.Equal(t, 1, data["next_offset"])
	require.False(t, data["eof"].(bool))
	result, err = e.Run(ctx, "read_file", map[string]any{"path": "f.txt", "offset": float64(1), "limit": float64(1)})
	require.NoError(t, err)
	data = dataOf(t, result)
	require.Equal(t, "b", data["text"])
	require.True(t, data["eof"].(bool))
	// 超 EOF 返回空正文与 eof，不生成虚构空行。
	result, err = e.Run(ctx, "read_file", map[string]any{"path": "f.txt", "offset": float64(2)})
	require.NoError(t, err)
	data = dataOf(t, result)
	require.Equal(t, "", data["text"])
	require.True(t, data["eof"].(bool))
}

func TestReadFile_EmptyFile(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty"), nil, 0600))
	result, err := e.Run(ctx, "read_file", map[string]any{"path": "empty"})
	require.NoError(t, err)
	data := dataOf(t, result)
	require.Equal(t, "", data["text"])
	require.True(t, data["eof"].(bool))
	require.Equal(t, 0, data["next_offset"])
}

func TestReadFile_LongLineAdvancesByteOffset(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "long"), []byte(strings.Repeat("x", 5000)+"\nnext\n"), 0600))
	result, err := e.Run(ctx, "read_file", map[string]any{"path": "long", "limit": float64(2)})
	require.NoError(t, err)
	data := dataOf(t, result)
	require.Len(t, data["text"], pageBytes)
	require.Equal(t, 0, data["next_offset"])
	require.Equal(t, pageBytes, data["next_byte_offset"])
	require.False(t, data["eof"].(bool))
	result, err = e.Run(ctx, "read_file", map[string]any{"path": "long", "limit": float64(2), "byte_offset": float64(pageBytes)})
	require.NoError(t, err)
	data = dataOf(t, result)
	require.Len(t, data["text"], 5000-pageBytes+1+len("next\n"))
	require.Equal(t, 2, data["next_offset"])
	require.True(t, data["eof"].(bool))
}

func TestReadFile_RejectsBinaryInvalidUTF8AndSymlinks(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary"), []byte("a\x00b"), 0600))
	_, err := e.Run(ctx, "read_file", map[string]any{"path": "binary"})
	require.ErrorContains(t, err, "binary")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "invalid"), []byte{0xff, 0xfe}, 0600))
	_, err = e.Run(ctx, "read_file", map[string]any{"path": "invalid"})
	require.ErrorContains(t, err, "UTF-8")
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "link")))
	_, err = e.Run(ctx, "read_file", map[string]any{"path": "link"})
	require.Error(t, err)
	_, err = e.Run(ctx, "read_file", map[string]any{"path": "../secret"})
	require.Error(t, err)
}

func TestReadFile_DeliverSnapshotIsImmutable(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	source := filepath.Join(dir, "report.txt")
	require.NoError(t, os.WriteFile(source, []byte("original"), 0600))
	result, err := e.Run(ctx, "read_file", map[string]any{"path": "report.txt", "deliver": true})
	require.NoError(t, err)
	require.NotEmpty(t, result.Artifact)
	require.Equal(t, "original", result.Summary)
	require.NoError(t, os.WriteFile(source, []byte("changed"), 0600))
	snapshot, err := os.ReadFile(result.Artifact)
	require.NoError(t, err)
	require.Equal(t, "original", string(snapshot))
	artifacts, err := e.Artifacts(7)
	require.NoError(t, err)
	require.Equal(t, "report.txt", artifacts[0].Name)
	_, err = e.Run(ctx, "read_file", map[string]any{"path": "report.txt", "deliver": true, "offset": float64(0)})
	require.Error(t, err)
}

func TestWriteFileAndPatch(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	_, err := e.Run(ctx, "write_file", map[string]any{"path": "sub/new.txt", "content": "x"})
	require.Error(t, err, "父目录必须已存在")
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0700))
	_, err = e.Run(ctx, "write_file", map[string]any{"path": "sub/new.txt", "content": "hello\n"})
	require.NoError(t, err)
	result, err := e.Run(ctx, "apply_patch", map[string]any{"path": "sub/new.txt", "edits": []any{map[string]any{"old": "hello", "new": "goodbye"}}})
	require.NoError(t, err)
	require.Equal(t, "patched: sub/new.txt", result.Summary)
	data, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "goodbye\n", string(data))
	_, err = e.Run(ctx, "apply_patch", map[string]any{"path": "sub/new.txt", "edits": []any{map[string]any{"old": "missing", "new": "x"}}})
	require.ErrorContains(t, err, "exactly once")
}

func TestListFiles_ReportsEntries(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), nil, 0600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "link")))
	result, err := e.Run(ctx, "list_files", map[string]any{"path": "."})
	require.NoError(t, err)
	entries := dataOf(t, result)["entries"].([]map[string]any)
	names := map[string]map[string]any{}
	for _, entry := range entries {
		names[entry["name"].(string)] = entry
	}
	require.True(t, names["sub"]["directory"].(bool))
	require.True(t, names["link"]["symlink"].(bool))
	require.False(t, names["a.txt"]["directory"].(bool))
}
