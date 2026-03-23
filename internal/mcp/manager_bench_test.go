//go:build goolm

package mcp

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/id"

	appcontext "rua.plus/saber/internal/context"
)

// BenchmarkListTools_Cached 测试缓存命中时工具列表获取的性能。
func BenchmarkListTools_Cached(b *testing.B) {
	mgr := BenchmarkManager(50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.ListTools()
	}
}

// BenchmarkListTools_Small 测试少量工具列表获取的性能。
func BenchmarkListTools_Small(b *testing.B) {
	mgr := BenchmarkManager(5)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.ListTools()
	}
}

// BenchmarkListTools_Large 测试大量工具列表获取的性能。
func BenchmarkListTools_Large(b *testing.B) {
	mgr := BenchmarkManager(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.ListTools()
	}
}

// BenchmarkMockServer_RegisterTool 测试工具注册的性能。
func BenchmarkMockServer_RegisterTool(b *testing.B) {
	server := NewMockMCPServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		server.RegisterTool(&MockTool{
			Name:        "tool_" + string(rune(i)),
			Description: "Benchmark tool",
			InputSchema: map[string]any{"type": "object"},
		})
	}
}

// BenchmarkMockServer_ListTools 测试工具列表获取的性能。
func BenchmarkMockServer_ListTools(b *testing.B) {
	server := NewTestMCPServerWithFixtures()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = server.ListTools()
	}
}

// BenchmarkMockServer_ListToolsAsMCP 测试 MCP 格式工具列表获取的性能。
func BenchmarkMockServer_ListToolsAsMCP(b *testing.B) {
	server := NewTestMCPServerWithFixtures()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = server.ListToolsAsMCP()
	}
}

// BenchmarkMockServer_GetTool 测试获取单个工具的性能。
func BenchmarkMockServer_GetTool(b *testing.B) {
	server := NewTestMCPServerWithFixtures()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = server.GetTool("echo")
	}
}

// BenchmarkMockServer_CallTool 测试工具调用的性能。
func BenchmarkMockServer_CallTool(b *testing.B) {
	server := NewTestMCPServerWithFixtures()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = server.CallTool(ctx, "echo", map[string]any{"message": "test"})
	}
}

// BenchmarkMockManager_ListTools 测试 MockManager 工具列表获取的性能。
func BenchmarkMockManager_ListTools(b *testing.B) {
	mgr := NewMockManager(true)
	mgr.RegisterTool(TestFixtures.EchoTool)
	mgr.RegisterTool(TestFixtures.ContextTool)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mgr.ListTools()
	}
}

// BenchmarkMockManager_CallTool 测试 MockManager 工具调用的性能。
func BenchmarkMockManager_CallTool(b *testing.B) {
	mgr := NewMockManager(true)
	mgr.RegisterTool(TestFixtures.EchoTool)
	mgr.RegisterTool(TestFixtures.ContextTool)

	ctx := appcontext.WithUserContext(
		context.Background(),
		id.UserID("@user:example.com"),
		id.RoomID("!room:example.com"),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = mgr.CallTool(ctx, "mock", "echo", map[string]any{"message": "test"})
	}
}

// BenchmarkNewTestUserContext 测试创建测试用户上下文的性能。
func BenchmarkNewTestUserContext(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewTestUserContext(i%100, i%100)
	}
}

// BenchmarkNewTestMCPServerWithFixtures 测试创建带 fixtures 的测试服务器的性能。
func BenchmarkNewTestMCPServerWithFixtures(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewTestMCPServerWithFixtures()
	}
}

// BenchmarkBenchmarkManager 测试创建预填充管理器的性能。
func BenchmarkBenchmarkManager(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BenchmarkManager(50)
	}
}

// BenchmarkConcurrent_ListTools 测试并发工具列表获取的性能。
func BenchmarkConcurrent_ListTools(b *testing.B) {
	mgr := BenchmarkManager(50)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = mgr.ListTools()
		}
	})
}

// BenchmarkConcurrent_CallTool 测试并发工具调用的性能。
func BenchmarkConcurrent_CallTool(b *testing.B) {
	mgr := NewMockManager(true)
	mgr.RegisterTool(TestFixtures.EchoTool)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			ctx := appcontext.WithUserContext(
				context.Background(),
				id.UserID("@user:example.com"),
				id.RoomID("!room:example.com"),
			)
			_, _ = mgr.CallTool(ctx, "mock", "echo", map[string]any{"message": "test"})
			i++
		}
	})
}

// BenchmarkConcurrent_RegisterUnregister 测试并发注册/注销工具的性能。
func BenchmarkConcurrent_RegisterUnregister(b *testing.B) {
	server := NewMockMCPServer()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tool := &MockTool{
				Name:        "temp_tool",
				Description: "Temporary tool",
				InputSchema: map[string]any{"type": "object"},
			}
			server.RegisterTool(tool)
			server.UnregisterTool("temp_tool")
			i++
		}
	})
}

// BenchmarkToolRegistration_Many 测试批量工具注册的性能。
func BenchmarkToolRegistration_Many(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		server := NewMockMCPServer()
		for j := 0; j < 100; j++ {
			server.RegisterTool(&MockTool{
				Name:        "tool_" + string(rune(j)),
				Description: "Benchmark tool",
				InputSchema: map[string]any{"type": "object"},
			})
		}
	}
}

// BenchmarkEchoTool 测试 echo 工具的性能。
func BenchmarkEchoTool(b *testing.B) {
	ctx := context.Background()
	args := map[string]any{"message": "Hello, World!"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = TestFixtures.EchoTool.Handler(ctx, args)
	}
}

// BenchmarkContextTool 测试 context 工具的性能。
func BenchmarkContextTool(b *testing.B) {
	ctx := appcontext.WithUserContext(
		context.Background(),
		id.UserID("@user:example.com"),
		id.RoomID("!room:example.com"),
	)
	args := map[string]any{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = TestFixtures.ContextTool.Handler(ctx, args)
	}
}

// BenchmarkMockServer_ToolCount 测试获取工具数量的性能。
func BenchmarkMockServer_ToolCount(b *testing.B) {
	server := BenchmarkManager(50).GetServer()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = server.ToolCount()
	}
}