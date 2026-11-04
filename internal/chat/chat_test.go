package chat_test

import (
	"testing"

	"rua.plus/saber/internal/chat"
)

// ptr 返回指向 value 的指针，便于构造带控制文本的消息。
func ptr(value string) *string { return &value }

// TestMessageCommandText 验证控制指令识别正文的取值规则：接入端给出的原文优先，
// 未给出时退回完整文本，指向空串表示用户只引用未发言。
func TestMessageCommandText(t *testing.T) {
	tests := []struct {
		name    string
		message chat.Message
		want    string
	}{
		{
			name:    "未提供控制文本时使用完整文本",
			message: chat.Message{Text: "帮我写一段代码"},
			want:    "帮我写一段代码",
		},
		{
			name:    "控制文本优先于引用回退",
			message: chat.Message{Text: "> <@bot:local> 已接收\n\n取消任务 #1", ControlText: ptr("取消任务 #1")},
			want:    "取消任务 #1",
		},
		{
			name:    "只引用未发言时控制文本为空",
			message: chat.Message{Text: "> <@bot:local> 取消任务 #1\n\n", ControlText: ptr("")},
			want:    "",
		},
		{
			name:    "空文本消息也返回空",
			message: chat.Message{},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.message.CommandText(); got != tt.want {
				t.Errorf("CommandText() = %q, 期望 %q", got, tt.want)
			}
		})
	}
}

// TestMessageContextIsNotCommandText 验证平台上下文只用于理解场景：
// 它不参与命令识别，也不影响正文必填校验。
func TestMessageContextIsNotCommandText(t *testing.T) {
	message := chat.Message{
		Session:  chat.Session{Platform: "violet", Account: "site", Conversation: "tweet:1"},
		ID:       "comment-1",
		SenderID: "user-1",
		Text:     "这个观点怎么解释？",
		Context:  "[Violet 推文互动]\n事件: tweet.commented\n[/Violet 推文互动]",
	}
	if got := message.CommandText(); got != "这个观点怎么解释？" {
		t.Fatalf("CommandText 不应包含平台上下文: %q", got)
	}
	if err := message.Validate(); err != nil {
		t.Fatalf("带上下文的消息应通过校验: %v", err)
	}
	empty := chat.Message{
		Session:  message.Session,
		SenderID: "user-1",
		Context:  "只有上下文没有正文",
	}
	if err := empty.Validate(); err == nil {
		t.Fatal("上下文不能替代正文让空消息通过校验")
	}
}
