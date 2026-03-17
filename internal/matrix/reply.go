// Package matrix 提供基于 mautrix-go 的 Matrix 客户端封装。
package matrix

import (
	"fmt"
	"strings"

	"maunium.net/go/mautrix/id"
)

// CreateReplyFallback 生成 Matrix 回复消息的回退文本。
//
// 该函数按照 Matrix 规范生成回复消息的纯文本回退格式，
// 用于不支持富文本回复的客户端。
//
// 回退文本格式:
//
//	> <@sender:server> original message
//	> continues on next line if multiline
//
//	reply content
//
// 参数:
//   - senderID: 原始消息发送者的用户 ID
//   - originalMsg: 原始消息内容（可能包含多行）
//   - replyMsg: 回复的消息内容
//
// 返回:
//   - 格式化后的回退文本字符串
//
// 示例:
//
//	text := CreateReplyFallback("@alice:matrix.org", "Hello!", "Hi there!")
//	// 返回："> <@alice:matrix.org> Hello!\n\nHi there!"
func CreateReplyFallback(senderID id.UserID, originalMsg, replyMsg string) string {
	// 处理空内容的情况
	if originalMsg == "" {
		return replyMsg
	}

	// 处理多行原始消息：每行前面添加 "> "
	quotedLines := make([]string, 0)
	for _, line := range strings.Split(originalMsg, "\n") {
		quotedLines = append(quotedLines, fmt.Sprintf("> <%s> %s", senderID.String(), line))
	}
	quotedText := strings.Join(quotedLines, "\n")

	// 组合引用和回复内容，中间用空行分隔
	if replyMsg == "" {
		return quotedText
	}

	return fmt.Sprintf("%s\n\n%s", quotedText, replyMsg)
}

// CreateReplyFallbackWithDisplayName 生成包含显示名称的回复回退文本。
//
// 该函数与 CreateReplyFallback 类似，但使用显示名称而非用户 ID，
// 提供更友好的阅读体验。
//
// 参数:
//   - senderID: 原始消息发送者的用户 ID
//   - displayName: 发送者的显示名称
//   - originalMsg: 原始消息内容
//   - replyMsg: 回复的消息内容
//
// 返回:
//   - 格式化后的回退文本字符串
func CreateReplyFallbackWithDisplayName(senderID id.UserID, displayName, originalMsg, replyMsg string) string {
	// 如果没有显示名称，退回到使用用户 ID
	if displayName == "" {
		return CreateReplyFallback(senderID, originalMsg, replyMsg)
	}

	// 处理空内容的情况
	if originalMsg == "" {
		return replyMsg
	}

	// 处理多行原始消息
	quotedLines := make([]string, 0)
	for _, line := range strings.Split(originalMsg, "\n") {
		quotedLines = append(quotedLines, fmt.Sprintf("> <%s> %s", displayName, line))
	}
	quotedText := strings.Join(quotedLines, "\n")

	// 组合引用和回复内容
	if replyMsg == "" {
		return quotedText
	}

	return fmt.Sprintf("%s\n\n%s", quotedText, replyMsg)
}
