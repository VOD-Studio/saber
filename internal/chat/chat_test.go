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
