package violetplatform

import (
	"strings"
	"testing"
)

// TestResolveMediaURL 验证入站图片地址解析与 SSRF 守卫：
// 相对路径拼 endpoint；绝对同 host 放行；绝对异 host 或非 http(s) scheme 拒绝。
func TestResolveMediaURL(t *testing.T) {
	t.Parallel()
	const endpoint = "https://blog.example.com"
	for _, tc := range []struct {
		name     string
		mediaURL string
		want     string
		wantErr  string
	}{
		{"相对路径拼 endpoint", "/uploads/pic-1", "https://blog.example.com/uploads/pic-1", ""},
		{"相对路径无前导斜杠", "uploads/pic-1", "https://blog.example.com/uploads/pic-1", ""},
		{"绝对同 host http", "http://blog.example.com/uploads/x", "http://blog.example.com/uploads/x", ""},
		{"绝对同 host https", "https://blog.example.com/uploads/x", "https://blog.example.com/uploads/x", ""},
		{"绝对异 host 拒绝", "https://evil.example.com/uploads/x", "", "不一致"},
		{"file scheme 拒绝", "file:///etc/passwd", "", "非允许的 http/https"},
		{"gopher scheme 拒绝", "gopher://blog.example.com/x", "", "非允许的 http/https"},
		{"空 url 拒绝", "", "", "为空"},
	} {
		got, err := resolveMediaURL(endpoint, tc.mediaURL)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%s: 期望含 %q 的错误，got %v", tc.name, tc.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: 意外错误 %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestEncodeImageAsDataURL 验证 data URL 形态与空 MIME 回退。
func TestEncodeImageAsDataURL(t *testing.T) {
	t.Parallel()
	if got := encodeImageAsDataURL([]byte("abc"), "image/png"); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("data URL 前缀不符: %q", got)
	}
	if got := encodeImageAsDataURL([]byte("abc"), ""); !strings.HasPrefix(got, "data:image/jpeg;base64,") {
		t.Fatalf("空 MIME 未回退 jpeg: %q", got)
	}
}
