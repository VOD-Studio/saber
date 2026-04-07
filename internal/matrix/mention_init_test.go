//go:build goolm

package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// TestMentionService_Init 测试 MentionService.Init 方法。
func TestMentionService_Init(t *testing.T) {
	t.Run("成功初始化", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// 返回 Profile 响应
			_, _ = w.Write([]byte(`{"displayname":"TestBot","avatar_url":"mxc://example.com/avatar"}`))
		}))
		defer server.Close()

		client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
		svc := NewMentionService(client, id.UserID("@bot:example.com"))

		err := svc.Init(context.Background())
		if err != nil {
			t.Errorf("Init() error = %v", err)
		}

		name := svc.GetDisplayName()
		if name != "TestBot" {
			t.Errorf("GetDisplayName() = %v, want TestBot", name)
		}
	})

	t.Run("获取 Profile 失败", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		client, _ := mautrix.NewClient(server.URL, id.UserID("@bot:example.com"), "token")
		svc := NewMentionService(client, id.UserID("@bot:example.com"))

		err := svc.Init(context.Background())
		if err == nil {
			t.Error("Init() expected error, got nil")
		}
	})
}

// TestMentionService_IsMentioned_DisplayName 测试通过显示名称提及。
func TestMentionService_IsMentioned_DisplayName(t *testing.T) {
	client, _ := mautrix.NewClient("https://example.com", id.UserID("@bot:example.com"), "token")
	svc := NewMentionService(client, id.UserID("@bot:example.com"))
	svc.displayName = "TestBot"

	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{"精确匹配", "Hello TestBot!", true},
		{"不区分大小写", "hello testbot!", true},
		{"全大写", "HELLO TESTBOT!", true},
		{"部分匹配", "TestBotting around", true},
		{"无提及", "Hello world", false},
		{"空内容", "", false},
		{"Matrix ID 提及", "@bot:example.com hello", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := svc.IsMentioned(tt.content)
			if result != tt.expected {
				t.Errorf("IsMentioned(%q) = %v, want %v", tt.content, result, tt.expected)
			}
		})
	}
}

// TestMentionService_IsMentioned_NoDisplayName 测试无显示名称时的行为。
func TestMentionService_IsMentioned_NoDisplayName(t *testing.T) {
	client, _ := mautrix.NewClient("https://example.com", id.UserID("@bot:example.com"), "token")
	svc := NewMentionService(client, id.UserID("@bot:example.com"))
	// 不设置 displayName

	// 只能通过 Matrix ID 检测
	if !svc.IsMentioned("@bot:example.com hello") {
		t.Error("IsMentioned should detect Matrix ID")
	}

	if svc.IsMentioned("hello world") {
		t.Error("IsMentioned should not detect when no display name set")
	}
}
