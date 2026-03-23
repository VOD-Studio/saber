//go:build goolm

package ai

import (
	"fmt"
	"sync"
	"testing"
)

// BenchmarkAddMessage_SingleRoom 测试单房间顺序添加消息的性能。
func BenchmarkAddMessage_SingleRoom(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100))
	roomID := TestRoomID(0)
	userID := TestUserID(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
	}
}

// BenchmarkAddMessage_SingleRoom_LongMessage 测试单房间添加长消息的性能。
func BenchmarkAddMessage_SingleRoom_LongMessage(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100))
	roomID := TestRoomID(0)
	userID := TestUserID(0)
	longMessage := string(make([]byte, 1000)) // 1000 字符消息

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.AddMessage(roomID, RoleUser, longMessage, userID)
	}
}

// BenchmarkAddMessage_MultiRoom 测试多房间并行添加消息的性能。
func BenchmarkAddMessage_MultiRoom(b *testing.B) {
	const roomCount = 10
	cm := NewTestContextManager(WithMaxMessages(100))

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			roomID := TestRoomID(i % roomCount)
			userID := TestUserID(i % roomCount)
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
			i++
		}
	})
}

// BenchmarkGetContext_SingleRoom 测试单房间读取上下文的性能。
func BenchmarkGetContext_SingleRoom(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100))
	roomID := TestRoomID(0)
	userID := TestUserID(0)

	// 预填充消息
	for i := 0; i < 50; i++ {
		cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cm.GetContext(roomID)
	}
}

// BenchmarkGetContext_MultiRoom 测试多房间并行读取上下文的性能。
func BenchmarkGetContext_MultiRoom(b *testing.B) {
	const roomCount = 10
	cm := NewTestContextManager(WithMaxMessages(100))

	// 预填充每个房间
	for r := 0; r < roomCount; r++ {
		roomID := TestRoomID(r)
		userID := TestUserID(r)
		for i := 0; i < 50; i++ {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			roomID := TestRoomID(i % roomCount)
			_ = cm.GetContext(roomID)
			i++
		}
	})
}

// BenchmarkAddGet_Concurrent 测试并发读写混合场景的性能。
func BenchmarkAddGet_Concurrent(b *testing.B) {
	const roomCount = 10
	cm := NewTestContextManager(WithMaxMessages(100))

	// 预填充每个房间
	for r := 0; r < roomCount; r++ {
		roomID := TestRoomID(r)
		userID := TestUserID(r)
		for i := 0; i < 50; i++ {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
		}
	}

	var wg sync.WaitGroup
	b.ResetTimer()

	// 70% 读，30% 写
	for i := 0; i < b.N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			roomID := TestRoomID(idx % roomCount)
			userID := TestUserID(idx % roomCount)

			if idx%10 < 7 {
				// 读操作
				_ = cm.GetContext(roomID)
			} else {
				// 写操作
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", idx), userID)
			}
		}(i)
	}
	wg.Wait()
}

// BenchmarkGetContextSize 测试获取上下文大小的性能。
func BenchmarkGetContextSize(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100))
	roomID := TestRoomID(0)
	userID := TestUserID(0)

	// 预填充消息
	for i := 0; i < 50; i++ {
		cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cm.GetContextSize(roomID)
	}
}

// BenchmarkListActiveRooms 测试获取活跃房间列表的性能。
func BenchmarkListActiveRooms(b *testing.B) {
	const roomCount = 100
	cm := NewTestContextManager(WithMaxMessages(100))

	// 预填充房间
	for r := 0; r < roomCount; r++ {
		roomID := TestRoomID(r)
		userID := TestUserID(r)
		for i := 0; i < 10; i++ {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cm.ListActiveRooms()
	}
}

// BenchmarkClearContext 测试清除上下文的性能。
func BenchmarkClearContext(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100))
	roomID := TestRoomID(0)
	userID := TestUserID(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 预填充一些消息
		for j := 0; j < 10; j++ {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", j), userID)
		}
		cm.ClearContext(roomID)
	}
}

// BenchmarkEstimateTokens 测试 token 估算的性能。
func BenchmarkEstimateTokens(b *testing.B) {
	text := string(make([]byte, 1000)) // 1000 字符文本

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = estimateTokens(text)
	}
}

// BenchmarkContextManager_LargeScale 测试大规模房间场景的性能。
func BenchmarkContextManager_LargeScale(b *testing.B) {
	const roomCount = 100
	const messagesPerRoom = 50

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm := NewTestContextManager(WithMaxMessages(100))
		for r := 0; r < roomCount; r++ {
			roomID := TestRoomID(r)
			userID := TestUserID(r)
			for m := 0; m < messagesPerRoom; m++ {
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", m), userID)
			}
		}
	}
}

// BenchmarkAddMessage_WithMaxTokens 测试启用 MaxTokens 限制时的添加性能。
func BenchmarkAddMessage_WithMaxTokens(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100), WithMaxTokens(1000))
	roomID := TestRoomID(0)
	userID := TestUserID(0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d with some content", i), userID)
	}
}

// BenchmarkConcurrentReadWrite 测试高并发读写场景。
func BenchmarkConcurrentReadWrite(b *testing.B) {
	cm := NewTestContextManager(WithMaxMessages(100))
	roomID := TestRoomID(0)
	userID := TestUserID(0)

	// 预填充
	for i := 0; i < 50; i++ {
		cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("New message %d", i), userID)
			} else {
				_ = cm.GetContext(roomID)
			}
			i++
		}
	})
}

// BenchmarkMultiRoomConcurrentReadWrite 测试多房间高并发读写场景。
func BenchmarkMultiRoomConcurrentReadWrite(b *testing.B) {
	const roomCount = 50
	cm := NewTestContextManager(WithMaxMessages(100))

	// 预填充
	for r := 0; r < roomCount; r++ {
		roomID := TestRoomID(r)
		userID := TestUserID(r)
		for i := 0; i < 20; i++ {
			cm.AddMessage(roomID, RoleUser, fmt.Sprintf("Message %d", i), userID)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			roomID := TestRoomID(i % roomCount)
			userID := TestUserID(i % roomCount)
			if i%3 == 0 {
				cm.AddMessage(roomID, RoleUser, fmt.Sprintf("New message %d", i), userID)
			} else {
				_ = cm.GetContext(roomID)
			}
			i++
		}
	})
}

// BenchmarkPrePopulatedManager 测试预填充上下文管理器的创建性能。
func BenchmarkPrePopulatedManager(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BenchmarkContextManager(10, 20, 100)
	}
}

// 停止后台清理 goroutine 的辅助函数，避免资源泄漏
func init() {
	// 确保 ContextManager 的后台 goroutine 不会在测试中泄漏
}