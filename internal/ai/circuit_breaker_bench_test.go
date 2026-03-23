//go:build goolm

package ai

import (
	"sync"
	"testing"
	"time"
)

// BenchmarkAllow_Closed 测试 Closed 状态下 Allow 调用的性能。
func BenchmarkAllow_Closed(b *testing.B) {
	cb := NewCircuitBreaker(100, time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.Allow()
	}
}

// BenchmarkAllow_Open 测试 Open 状态下 Allow 调用的性能。
func BenchmarkAllow_Open(b *testing.B) {
	cb := NewCircuitBreaker(1, time.Hour) // 1 次失败即熔断

	// 触发熔断
	cb.RecordFailure()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.Allow()
	}
}

// BenchmarkAllow_HalfOpen 测试 HalfOpen 状态下 Allow 调用的性能。
func BenchmarkAllow_HalfOpen(b *testing.B) {
	cb := NewCircuitBreaker(1, time.Nanosecond) // 极短超时

	// 触发熔断
	cb.RecordFailure()
	// 等待超时进入 HalfOpen
	time.Sleep(time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.Allow()
	}
}

// BenchmarkRecordSuccess 测试记录成功操作的性能。
func BenchmarkRecordSuccess(b *testing.B) {
	cb := NewCircuitBreaker(100, time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordSuccess()
	}
}

// BenchmarkRecordFailure 测试记录失败操作的性能。
func BenchmarkRecordFailure(b *testing.B) {
	cb := NewCircuitBreaker(1000, time.Minute)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordFailure()
	}
}

// BenchmarkState 测试获取状态的性能。
func BenchmarkState(b *testing.B) {
	cb := NewCircuitBreaker(5, time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.State()
	}
}

// BenchmarkState_Open 测试 Open 状态下获取状态的性能。
func BenchmarkState_Open(b *testing.B) {
	cb := NewCircuitBreaker(1, time.Hour)

	// 触发熔断
	cb.RecordFailure()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cb.State()
	}
}

// BenchmarkExecute_Success 测试 Execute 成功路径的性能。
func BenchmarkExecute_Success(b *testing.B) {
	cb := NewCircuitBreaker(100, time.Minute)
	fn := func() (any, error) {
		return nil, nil
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cb.Execute(fn)
	}
}

// BenchmarkExecute_Failure 测试 Execute 失败路径的性能。
func BenchmarkExecute_Failure(b *testing.B) {
	cb := NewCircuitBreaker(1000, time.Minute)
	fn := func() (any, error) {
		return nil, ErrCircuitOpen // 模拟错误
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = cb.Execute(fn)
	}
}

// BenchmarkConcurrent_Allow 测试并发 Allow 调用的性能。
func BenchmarkConcurrent_Allow(b *testing.B) {
	cb := NewCircuitBreaker(1000, time.Minute)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cb.Allow()
		}
	})
}

// BenchmarkConcurrent_Record 测试并发记录操作的性能。
func BenchmarkConcurrent_Record(b *testing.B) {
	cb := NewCircuitBreaker(10000, time.Minute)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				cb.RecordSuccess()
			} else {
				cb.RecordFailure()
			}
			i++
		}
	})
}

// BenchmarkConcurrent_State 测试并发获取状态的性能。
func BenchmarkConcurrent_State(b *testing.B) {
	cb := NewCircuitBreaker(100, time.Minute)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cb.State()
		}
	})
}

// BenchmarkConcurrent_Mixed 测试并发混合操作的性能。
func BenchmarkConcurrent_Mixed(b *testing.B) {
	cb := NewCircuitBreaker(10000, time.Minute)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			switch i % 4 {
			case 0:
				_ = cb.Allow()
			case 1:
				cb.RecordSuccess()
			case 2:
				cb.RecordFailure()
			case 3:
				_ = cb.State()
			}
			i++
		}
	})
}

// BenchmarkState_Transition 测试状态转换的性能。
func BenchmarkState_Transition(b *testing.B) {
	cb := NewCircuitBreaker(1, time.Nanosecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// 触发熔断
		cb.RecordFailure()
		// 等待超时进入 HalfOpen
		time.Sleep(time.Microsecond)
		// 获取状态（可能触发转换）
		_ = cb.State()
		// 重置
		cb.RecordSuccess()
	}
}

// BenchmarkConcurrent_Execute 测试并发 Execute 调用的性能。
func BenchmarkConcurrent_Execute(b *testing.B) {
	cb := NewCircuitBreaker(10000, time.Minute)
	fn := func() (any, error) {
		return nil, nil
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = cb.Execute(fn)
		}
	})
}

// BenchmarkHighContention 测试高竞争场景下的性能。
func BenchmarkHighContention(b *testing.B) {
	cb := NewCircuitBreaker(1000, time.Minute)
	var wg sync.WaitGroup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(10)
		for j := 0; j < 10; j++ {
			go func(idx int) {
				defer wg.Done()
				if idx%2 == 0 {
					_ = cb.Allow()
				} else {
					_ = cb.State()
				}
			}(j)
		}
		wg.Wait()
	}
}

// BenchmarkNewCircuitBreaker 测试创建 CircuitBreaker 的性能。
func BenchmarkNewCircuitBreaker(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewCircuitBreaker(5, 30*time.Second)
	}
}

// BenchmarkCircuitBreaker_FullCycle 测试完整熔断周期的性能。
func BenchmarkCircuitBreaker_FullCycle(b *testing.B) {
	cb := NewCircuitBreaker(3, time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Closed 状态操作
		for j := 0; j < 5; j++ {
			_ = cb.Allow()
			cb.RecordSuccess()
		}

		// 触发熔断
		for j := 0; j < 3; j++ {
			cb.RecordFailure()
		}

		// Open 状态
		_ = cb.Allow() // 应返回 false

		// 等待恢复
		time.Sleep(2 * time.Millisecond)

		// HalfOpen 状态
		_ = cb.State()

		// 恢复
		cb.RecordSuccess()
	}
}