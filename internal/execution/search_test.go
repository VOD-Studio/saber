package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSearchFiles_PaginatesAndSkipsNoise(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\nbeta alpha\ngamma\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("alpha\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden"), []byte("alpha\n"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("alpha\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary"), []byte("alpha\x00"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "links"), 0700))
	require.NoError(t, os.Symlink(filepath.Join(dir, "a.txt"), filepath.Join(dir, "links", "l.txt")))

	var seen []string
	offset := 0
	for pages := 0; ; pages++ {
		require.Less(t, pages, 20)
		result, err := e.Run(ctx, "search_files", map[string]any{"pattern": "alpha", "limit": float64(2), "offset": float64(offset)})
		require.NoError(t, err)
		data := dataOf(t, result)
		raw, err := json.Marshal(data)
		require.NoError(t, err)
		require.LessOrEqual(t, len(raw), maxEncodedData)
		matches := data["matches"].([]map[string]any)
		for _, match := range matches {
			seen = append(seen, match["path"].(string)+":"+match["text"].(string))
		}
		if data["eof"].(bool) {
			break
		}
		offset = data["next_offset"].(int)
	}
	require.Equal(t, []string{"a.txt:alpha", "a.txt:beta alpha", "b.txt:alpha"}, seen)
	require.NotContains(t, strings.Join(seen, ","), ".hidden")
	require.NotContains(t, strings.Join(seen, ","), ".git")
	require.NotContains(t, strings.Join(seen, ","), "links")

	_, err := e.Run(ctx, "search_files", map[string]any{"pattern": "("})
	require.Error(t, err)
	_, err = e.Run(ctx, "search_files", map[string]any{"pattern": "alpha", "file_glob": "["})
	require.Error(t, err)
}

func TestSearchFiles_CaseInsensitiveAndGlobFilter(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc Handler() {}\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.md"), []byte("handler\n"), 0600))
	result, err := e.Run(ctx, "search_files", map[string]any{"pattern": "handler", "file_glob": "**/*.go", "case_sensitive": false})
	require.NoError(t, err)
	matches := dataOf(t, result)["matches"].([]map[string]any)
	require.Len(t, matches, 1)
	require.Equal(t, "main.go", matches[0]["path"])
	require.Equal(t, 2, matches[0]["line"])
}

func TestGlobFiles_MatchesSegments(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pkg", "deep"), 0700))
	for _, name := range []string{"top.go", "pkg/a.go", "pkg/deep/b.go", "readme.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0600))
	}
	collect := func(pattern string) []string {
		var result []string
		offset := 0
		for pages := 0; ; pages++ {
			require.Less(t, pages, 20)
			res, err := e.Run(ctx, "glob_files", map[string]any{"pattern": pattern, "offset": float64(offset)})
			require.NoError(t, err)
			data := dataOf(t, res)
			result = append(result, data["paths"].([]string)...)
			if data["eof"].(bool) {
				return result
			}
			offset = data["next_offset"].(int)
		}
	}
	require.Equal(t, []string{"pkg/a.go", "pkg/deep/b.go", "top.go"}, collect("**/*.go"))
	require.Equal(t, []string{"pkg", "pkg/a.go", "pkg/deep", "pkg/deep/b.go", "readme.md", "top.go"}, collect("**"))
	require.Equal(t, []string{"pkg/deep/b.go"}, collect("pkg/**/b.go"))
	require.Equal(t, []string{"top.go"}, collect("*.go"))
	_, err := e.Run(ctx, "glob_files", map[string]any{"pattern": "../*"})
	require.Error(t, err)
}

func TestSearch_DataStaysWithinEightKiB(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	var content strings.Builder
	for range 300 {
		content.WriteString("needle needle needle\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "many.txt"), []byte(content.String()), 0600))
	result, err := e.Run(ctx, "search_files", map[string]any{"pattern": "needle", "limit": float64(200)})
	require.NoError(t, err)
	data := dataOf(t, result)
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), maxEncodedData)
	require.NotEmpty(t, data["matches"])
	require.False(t, data["eof"].(bool))
	require.NotNil(t, data["next_offset"])
	glob, err := e.Run(ctx, "glob_files", map[string]any{"pattern": "**"})
	require.NoError(t, err)
	raw, err = json.Marshal(dataOf(t, glob))
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), maxEncodedData)
}
