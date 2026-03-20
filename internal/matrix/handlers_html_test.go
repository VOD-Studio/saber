// Package matrix 提供 Matrix 客户端功能。
package matrix

import (
	"testing"
)

// TestSanitizeHTML_RemovesDangerousTags 测试移除危险标签。
func TestSanitizeHTML_RemovesDangerousTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "移除 script 标签",
			input:    `<script>alert('xss')</script><b>safe</b>`,
			expected: `<b>safe</b>`,
		},
		{
			name:     "移除 iframe 标签",
			input:    `<iframe src="evil.com"></iframe><p>text</p>`,
			expected: `<p>text</p>`,
		},
		{
			name:     "移除 onclick 属性",
			input:    `<a onclick="evil()">link</a>`,
			expected: `link`,
		},
		{
			name:     "保留安全标签",
			input:    `<b>bold</b> <i>italic</i> <code>code</code>`,
			expected: `<b>bold</b> <i>italic</i> <code>code</code>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := SanitizeHTML(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeHTML() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestSanitizeHTML_AllowSafeTags 测试保留安全标签。
func TestSanitizeHTML_AllowSafeTags(t *testing.T) {
	t.Parallel()

	input := `<b>bold</b> <i>italic</i> <code>code</code> <a href="https://example.com">link</a> <pre>preformatted</pre>`
	result := SanitizeHTML(input)

	// 验证所有安全标签被保留
	safeTags := []string{"<b>", "</b>", "<i>", "</i>", "<code>", "</code>", "<a", "</a>", "<pre>", "</pre>"}
	for _, tag := range safeTags {
		if !contains(result, tag) {
			t.Errorf("SanitizeHTML() should preserve safe tag %q, got: %q", tag, result)
		}
	}
}

// contains 检查字符串是否包含子串。
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr || len(s) > len(substr) && contains(s[1:], substr)
}
