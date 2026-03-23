//go:build goolm

package matrix

import (
	"testing"
)

// testBuildInfo 用于基准测试的构建信息。
var testBuildInfo = TestBuildInfo()

// BenchmarkCommandParsing_Simple 测试简单命令解析的性能。
func BenchmarkCommandParsing_Simple(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!ping")
	}
}

// BenchmarkCommandParsing_WithArgs 测试带参数命令解析的性能。
func BenchmarkCommandParsing_WithArgs(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!ai 请帮我写一段 Go 代码，实现一个简单的 HTTP 服务器")
	}
}

// BenchmarkCommandParsing_LongArgs 测试长参数命令解析的性能。
func BenchmarkCommandParsing_LongArgs(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	longArg := "这是一个很长的参数，" +
		"包含了多个中文字符和一些英文单词，" +
		"用于测试命令解析在处理长参数时的性能表现。" +
		"这个消息应该足够长，可以有效地测试解析效率。"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!ai " + longArg)
	}
}

// BenchmarkCommandParsing_MentionFormat 测试 mention 格式命令解析的性能。
func BenchmarkCommandParsing_MentionFormat(b *testing.B) {
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

// BenchmarkCommandParsing_MentionWithColon 测试带冒号的 mention 格式命令解析的性能。
func BenchmarkCommandParsing_MentionWithColon(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("@bot:example.com: 你好")
	}
}

// BenchmarkCommandParsing_Invalid 测试无效命令解析的性能。
func BenchmarkCommandParsing_Invalid(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("这不是一个命令")
	}
}

// BenchmarkCommandParsing_Empty 测试空消息解析的性能。
func BenchmarkCommandParsing_Empty(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("")
	}
}

// BenchmarkCommandParsing_Whitespace 测试带空白字符的消息解析性能。
func BenchmarkCommandParsing_Whitespace(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("   !ping   ")
	}
}

// BenchmarkCommandParsing_MultipleArgs 测试多参数命令解析的性能。
func BenchmarkCommandParsing_MultipleArgs(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!command arg1 arg2 arg3 arg4 arg5 arg6 arg7 arg8 arg9 arg10")
	}
}

// BenchmarkCommandParsing_SpecialChars 测试特殊字符命令解析的性能。
func BenchmarkCommandParsing_SpecialChars(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!cmd special:chars@test#here$now%done")
	}
}

// BenchmarkCommandParsing_Emoji 测试包含 emoji 的命令解析性能。
func BenchmarkCommandParsing_Emoji(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand("!ping 🎉 ✅ ❌ 🚀")
	}
}

// BenchmarkCommandParsing_Mixed 测试混合格式命令解析的性能。
func BenchmarkCommandParsing_Mixed(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	messages := []string{
		"!ping",
		"@bot:example.com 你好",
		"!ai 测试消息",
		"普通消息",
		"!help",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cs.ParseCommand(messages[i%len(messages)])
	}
}

// BenchmarkCommandRegistration 测试命令注册的性能。
func BenchmarkCommandRegistration(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mockClient := NewMockMatrixClient(
			"@bot:example.com",
			"DEVICE",
			"token",
			"https://matrix.org",
		)
		cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)
		RegisterBuiltinCommands(cs)
	}
}

// BenchmarkCommandRegistration_Custom 测试自定义命令注册的性能。
func BenchmarkCommandRegistration_Custom(b *testing.B) {
	mockClient := NewMockMatrixClient(
		"@bot:example.com",
		"DEVICE",
		"token",
		"https://matrix.org",
	)
	cs := NewCommandService(mockClient.GetClient(), mockClient.UserID, &testBuildInfo)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cs.RegisterCommandWithDesc("test", "Test command", &PingCommand{service: cs})
	}
}