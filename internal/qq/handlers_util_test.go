// Package qq 提供 QQ 机器人的适配器实现。
package qq

import (
	"testing"
)

// TestExtractMessageContent 测试消息内容提取函数。
func TestExtractMessageContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "普通文本",
			content: "hello world",
			want:    "hello world",
		},
		{
			name:    "带前导空格",
			content: "  hello",
			want:    "hello",
		},
		{
			name:    "带尾部空格",
			content: "hello  ",
			want:    "hello",
		},
		{
			name:    "带前后空格",
			content: "  hello  ",
			want:    "hello",
		},
		{
			name:    "空字符串",
			content: "",
			want:    "",
		},
		{
			name:    "仅空格",
			content: "   ",
			want:    "",
		},
		{
			name:    "中文内容",
			content: "你好世界",
			want:    "你好世界",
		},
		{
			name:    "带换行符",
			content: "hello\nworld",
			want:    "hello\nworld",
		},
		{
			name:    "带制表符",
			content: "hello\tworld",
			want:    "hello\tworld",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMessageContent(tt.content)
			if got != tt.want {
				t.Errorf("extractMessageContent(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestRemoveMention 测试移除提及函数。
func TestRemoveMention(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "无提及",
			content: "hello world",
			want:    "hello world",
		},
		{
			name:    "单个提及",
			content: "<@!123456789> hello",
			want:    "hello",
		},
		{
			name:    "多个提及",
			content: "<@!123> <@!456> hello",
			want:    "hello",
		},
		{
			name:    "提及在中间",
			content: "hello <@!123> world",
			want:    "hello  world",
		},
		{
			name:    "提及在末尾",
			content: "hello <@!123>",
			want:    "hello",
		},
		{
			name:    "仅提及",
			content: "<@!123456>",
			want:    "",
		},
		{
			name:    "空字符串",
			content: "",
			want:    "",
		},
		{
			name:    "不完整的提及标签",
			content: "<@!123",
			want:    "<@!123",
		},
		{
			name:    "无结束标签",
			content: "<@!123 hello",
			want:    "<@!123 hello",
		},
		{
			name:    "提及前后空格",
			content: "  <@!123>  hello  ",
			want:    "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeMention(tt.content)
			if got != tt.want {
				t.Errorf("removeMention(%q) = %q, want %q", tt.content, got, tt.want)
			}
		})
	}
}

// TestRemoveMention_EdgeCases 测试边界情况。
func TestRemoveMention_EdgeCases(t *testing.T) {
	t.Run("连续多个提及", func(t *testing.T) {
		content := "<@!1><@!2><@!3>message"
		got := removeMention(content)
		want := "message"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("提及之间有文本", func(t *testing.T) {
		content := "start<@!1>middle<@!2>end"
		got := removeMention(content)
		want := "startmiddleend"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("特殊字符的数字", func(t *testing.T) {
		content := "<@!123456789012345> hello"
		got := removeMention(content)
		want := "hello"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// TestExtractMessageContent_AndRemoveMention_Combined 测试组合使用。
func TestExtractMessageContent_AndRemoveMention_Combined(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "带空格和提及",
			content: "  <@!123> hello world  ",
			want:    "hello world",
		},
		{
			name:    "纯文本",
			content: "  hello world  ",
			want:    "hello world",
		},
		{
			name:    "多个提及和空格",
			content: " <@!1> <@!2> message ",
			want:    "message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 模拟实际使用流程
			content := extractMessageContent(tt.content)
			content = removeMention(content)
			if content != tt.want {
				t.Errorf("combined result = %q, want %q", content, tt.want)
			}
		})
	}
}