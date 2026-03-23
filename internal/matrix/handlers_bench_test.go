//go:build goolm

package matrix

import (
	"context"
	"testing"

	"golang.org/x/sync/semaphore"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// BenchmarkParseCommand_Prefixed 测试前缀命令解析的性能。
func BenchmarkParseCommand_Prefixed(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!ai 你好，请帮我分析一下这个问题")
	}
}

// BenchmarkParseCommand_Mention 测试 mention 命令解析的性能。
func BenchmarkParseCommand_Mention(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("@bot:example.com 你好")
	}
}

// BenchmarkParseCommand_NoCommand 测试非命令消息解析的性能。
func BenchmarkParseCommand_NoCommand(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("这是一条普通消息，不是命令")
	}
}

// BenchmarkSemaphore_AcquireRelease 测试信号量获取释放的性能。
func BenchmarkSemaphore_AcquireRelease(b *testing.B) {
	sem := semaphore.NewWeighted(10)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := sem.Acquire(ctx, 1); err != nil {
			b.Fatal(err)
		}
		sem.Release(1)
	}
}

// BenchmarkSemaphore_Parallel 测试并行信号量操作的性能。
func BenchmarkSemaphore_Parallel(b *testing.B) {
	sem := semaphore.NewWeighted(10)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if err := sem.Acquire(ctx, 1); err != nil {
				continue
			}
			sem.Release(1)
		}
	})
}

// BenchmarkSemaphore_HighContention 测试高竞争信号量场景的性能。
func BenchmarkSemaphore_HighContention(b *testing.B) {
	sem := semaphore.NewWeighted(3) // 低容量高竞争

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			if err := sem.Acquire(ctx, 1); err != nil {
				continue
			}
			sem.Release(1)
		}
	})
}

// BenchmarkCreateReplyFallback_LongMessage 测试长消息回复回退文本的性能。
func BenchmarkCreateReplyFallback_LongMessage(b *testing.B) {
	senderID := id.UserID("@user:example.com")
	originalMsg := string(make([]byte, 1000)) // 1000 字符消息
	replyMsg := "Reply to long message"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CreateReplyFallback(senderID, originalMsg, replyMsg)
	}
}

// BenchmarkSanitizeHTML 测试 HTML 净化的性能。
func BenchmarkSanitizeHTML(b *testing.B) {
	html := `<div><p>Hello <strong>World</strong>!</p><script>alert('xss')</script></div>`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeHTML(html)
	}
}

// BenchmarkSanitizeHTML_Complex 测试复杂 HTML 净化的性能。
func BenchmarkSanitizeHTML_Complex(b *testing.B) {
	html := `<table><thead><tr><th>Header</th></tr></thead><tbody><tr><td>Data</td></tr></tbody></table>
<script>malicious code</script><style>body{display:none}</style>
<a href="javascript:evil()">click</a><img src=x onerror=alert(1)>`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeHTML(html)
	}
}

// BenchmarkExtractMediaInfo 测试提取媒体信息的性能。
func BenchmarkExtractMediaInfo(b *testing.B) {
	content := &event.MessageEventContent{
		MsgType: event.MsgImage,
		Body:    "image.png",
		URL:     id.ContentURIString("mxc://example.com/abc123"),
		Info: &event.FileInfo{
			MimeType: "image/png",
			Size:     1024,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ExtractMediaInfo(content)
	}
}

// BenchmarkGenerateTestEvents 测试生成测试事件的性能。
func BenchmarkGenerateTestEvents(b *testing.B) {
	roomID := TestRoomID(0)
	senderID := TestUserID(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GenerateTestEvents(10, roomID, senderID)
	}
}

// BenchmarkGenerateTestEvents_Large 测试生成大量测试事件的性能。
func BenchmarkGenerateTestEvents_Large(b *testing.B) {
	roomID := TestRoomID(0)
	senderID := TestUserID(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GenerateTestEvents(100, roomID, senderID)
	}
}

// BenchmarkListCommands 测试列出命令的性能。
func BenchmarkListCommands(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	// 注册一些命令
	RegisterBuiltinCommands(cs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ListCommands()
	}
}

// BenchmarkGetCommand 测试获取命令的性能。
func BenchmarkGetCommand(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)
	RegisterBuiltinCommands(cs)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cs.GetCommand("ping")
	}
}

// BenchmarkBotID 测试获取 Bot ID 的性能。
func BenchmarkBotID(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.BotID()
	}
}

// BenchmarkConcurrentParseCommand 测试并发命令解析的性能。
func BenchmarkConcurrentParseCommand(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	messages := []string{
		"!ai 你好",
		"!ping",
		"!help",
		"@bot:example.com 测试消息",
		"普通消息",
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = cs.ParseCommand(messages[i%len(messages)])
			i++
		}
	})
}