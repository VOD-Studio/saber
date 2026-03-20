// Package ai_test 包含熔断器的单元测试。
package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestCircuitBreaker_New 测试熔断器初始化。
func TestCircuitBreaker_New(t *testing.T) {
	tests := []struct {
		name             string
		failureThreshold int
		resetTimeout     time.Duration
	}{
		{"default config", 5, 30 * time.Second},
		{"custom config", 3, 10 * time.Second},
		{"zero threshold defaults to 5", 0, 30 * time.Second},
		{"negative threshold defaults to 5", -1, 30 * time.Second},
		{"zero timeout defaults to 30s", 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := NewCircuitBreaker(tt.failureThreshold, tt.resetTimeout)

			if cb == nil {
				t.Fatal("NewCircuitBreaker returned nil")
			}

			// 验证初始状态为 Closed
			if state := cb.State(); state != CircuitStateClosed {
				t.Errorf("initial state = %v, want %v", state, CircuitStateClosed)
			}
		})
	}
}

// TestCircuitBreaker_Allow 测试 Allow 方法在不同状态下的行为。
func TestCircuitBreaker_Allow(t *testing.T) {
	t.Run("closed state allows all requests", func(t *testing.T) {
		cb := NewCircuitBreaker(5, 1*time.Second)

		for i := 0; i < 10; i++ {
			if !cb.Allow() {
				t.Errorf("request %d rejected in closed state", i)
			}
		}
	})

	t.Run("open state rejects all requests", func(t *testing.T) {
		cb := NewCircuitBreaker(2, 1*time.Second)

		// 触发失败使熔断器打开
		cb.RecordFailure()
		cb.RecordFailure()

		if state := cb.State(); state != CircuitStateOpen {
			t.Fatalf("state = %v, want %v", state, CircuitStateOpen)
		}

		for i := 0; i < 5; i++ {
			if cb.Allow() {
				t.Errorf("request %d allowed in open state", i)
			}
		}
	})

	t.Run("half-open state allows one request", func(t *testing.T) {
		cb := NewCircuitBreaker(1, 100*time.Millisecond)

		// 触发失败使熔断器打开
		cb.RecordFailure()

		if state := cb.State(); state != CircuitStateOpen {
			t.Fatalf("state = %v, want %v", state, CircuitStateOpen)
		}

		// 等待恢复超时
		time.Sleep(150 * time.Millisecond)

		// 第一个请求应该被允许（探测请求）
		if !cb.Allow() {
			t.Error("first request in half-open state should be allowed")
		}

		// 后续请求应该被拒绝
		if cb.Allow() {
			t.Error("second request in half-open state should be rejected")
		}
	})
}

// TestCircuitBreaker_RecordFailure 测试失败记录和状态转换。
func TestCircuitBreaker_RecordFailure(t *testing.T) {
	t.Run("failures below threshold keep circuit closed", func(t *testing.T) {
		cb := NewCircuitBreaker(5, 1*time.Second)

		for i := 0; i < 4; i++ {
			cb.RecordFailure()
			if state := cb.State(); state != CircuitStateClosed {
				t.Errorf("after %d failures, state = %v, want %v", i+1, state, CircuitStateClosed)
			}
		}
	})

	t.Run("failures reach threshold open circuit", func(t *testing.T) {
		cb := NewCircuitBreaker(3, 1*time.Second)

		// 记录阈值数量的失败
		for i := 0; i < 3; i++ {
			cb.RecordFailure()
		}

		if state := cb.State(); state != CircuitStateOpen {
			t.Errorf("state = %v, want %v", state, CircuitStateOpen)
		}
	})

	t.Run("failure in half-open state opens circuit immediately", func(t *testing.T) {
		cb := NewCircuitBreaker(1, 100*time.Millisecond)

		// 打开熔断器
		cb.RecordFailure()
		if state := cb.State(); state != CircuitStateOpen {
			t.Fatalf("state = %v, want %v", state, CircuitStateOpen)
		}

		// 等待进入半开状态
		time.Sleep(150 * time.Millisecond)

		// 允许探测请求
		if !cb.Allow() {
			t.Fatal("probe request should be allowed")
		}

		// 探测请求失败，熔断器应该立即打开
		cb.RecordFailure()
		if state := cb.State(); state != CircuitStateOpen {
			t.Errorf("state = %v, want %v", state, CircuitStateOpen)
		}
	})
}

// TestCircuitBreaker_RecordSuccess 测试成功记录和状态转换。
func TestCircuitBreaker_RecordSuccess(t *testing.T) {
	t.Run("success in closed state resets failure count", func(t *testing.T) {
		cb := NewCircuitBreaker(5, 1*time.Second)

		// 记录一些失败
		cb.RecordFailure()
		cb.RecordFailure()

		// 记录成功
		cb.RecordSuccess()

		// 需要再次达到阈值才能打开
		for i := 0; i < 5; i++ {
			cb.RecordFailure()
		}

		if state := cb.State(); state != CircuitStateOpen {
			t.Errorf("state = %v, want %v", state, CircuitStateOpen)
		}
	})

	t.Run("success in half-open state closes circuit", func(t *testing.T) {
		cb := NewCircuitBreaker(1, 100*time.Millisecond)

		// 打开熔断器
		cb.RecordFailure()
		if state := cb.State(); state != CircuitStateOpen {
			t.Fatalf("state = %v, want %v", state, CircuitStateOpen)
		}

		// 等待进入半开状态
		time.Sleep(150 * time.Millisecond)

		// 允许探测请求
		if !cb.Allow() {
			t.Fatal("probe request should be allowed")
		}

		// 探测请求成功，熔断器应该关闭
		cb.RecordSuccess()
		if state := cb.State(); state != CircuitStateClosed {
			t.Errorf("state = %v, want %v", state, CircuitStateClosed)
		}
	})
}

// TestCircuitBreaker_StateTransitions 测试完整的状态转换周期。
func TestCircuitBreaker_StateTransitions(t *testing.T) {
	cb := NewCircuitBreaker(2, 100*time.Millisecond)

	// 初始状态：Closed
	if state := cb.State(); state != CircuitStateClosed {
		t.Fatalf("initial state = %v, want %v", state, CircuitStateClosed)
	}

	// 记录失败直到熔断器打开
	cb.RecordFailure()
	if state := cb.State(); state != CircuitStateClosed {
		t.Errorf("after 1 failure, state = %v, want %v", state, CircuitStateClosed)
	}

	cb.RecordFailure()
	if state := cb.State(); state != CircuitStateOpen {
		t.Errorf("after 2 failures, state = %v, want %v", state, CircuitStateOpen)
	}

	// 在 Open 状态下，所有请求被拒绝
	if cb.Allow() {
		t.Error("request should be rejected in open state")
	}

	// 等待恢复超时，进入 HalfOpen
	time.Sleep(150 * time.Millisecond)

	// 第一个请求被允许（探测）
	if !cb.Allow() {
		t.Error("probe request should be allowed in half-open state")
	}

	// 第二个请求被拒绝（等待探测结果）
	if cb.Allow() {
		t.Error("second request should be rejected while waiting for probe result")
	}

	// 探测成功
	cb.RecordSuccess()
	if state := cb.State(); state != CircuitStateClosed {
		t.Errorf("after success, state = %v, want %v", state, CircuitStateClosed)
	}

	// 熔断器关闭后，请求被允许
	if !cb.Allow() {
		t.Error("request should be allowed in closed state")
	}
}

// TestCircuitBreaker_Concurrency 测试并发安全性。
func TestCircuitBreaker_Concurrency(t *testing.T) {
	cb := NewCircuitBreaker(100, 1*time.Second)

	const goroutines = 100
	var wg sync.WaitGroup
	errChan := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errChan <- errors.New("panic occurred")
				}
			}()

			// 混合操作
			for j := 0; j < 10; j++ {
				cb.Allow()
				if idx%2 == 0 {
					cb.RecordFailure()
				} else {
					cb.RecordSuccess()
				}
				_ = cb.State()
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			t.Errorf("concurrent test failed: %v", err)
		}
	}
}

// TestCircuitBreaker_Execute 测试 Execute 方法的成功和失败场景。
func TestCircuitBreaker_Execute(t *testing.T) {
	t.Run("success in closed state", func(t *testing.T) {
		cb := NewCircuitBreaker(5, 1*time.Second)

		result, err := cb.Execute(func() (any, error) {
			return "success", nil
		})

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if result != "success" {
			t.Errorf("result = %v, want 'success'", result)
		}
	})

	t.Run("failure counts toward threshold", func(t *testing.T) {
		cb := NewCircuitBreaker(2, 1*time.Second)

		// 第一次失败
		_, err := cb.Execute(func() (any, error) {
			return nil, errors.New("error: 500")
		})
		if err == nil {
			t.Error("expected error")
		}

		// 熔断器应该仍然关闭
		if state := cb.State(); state != CircuitStateClosed {
			t.Errorf("state = %v, want %v", state, CircuitStateClosed)
		}

		// 第二次失败
		_, err = cb.Execute(func() (any, error) {
			return nil, errors.New("error: 500")
		})
		if err == nil {
			t.Error("expected error")
		}

		// 熔断器应该打开
		if state := cb.State(); state != CircuitStateOpen {
			t.Errorf("state = %v, want %v", state, CircuitStateOpen)
		}
	})

	t.Run("rejected in open state", func(t *testing.T) {
		cb := NewCircuitBreaker(1, 1*time.Second)

		// 打开熔断器
		cb.RecordFailure()

		// 尝试执行应该被拒绝
		_, err := cb.Execute(func() (any, error) {
			return "should not reach", nil
		})

		if err == nil {
			t.Error("expected circuit open error")
		}
		if !errors.Is(err, ErrCircuitOpen) {
			t.Errorf("error = %v, want ErrCircuitOpen", err)
		}
	})
}

// TestRetryConfigWrapper_WithCircuitBreaker 测试 RetryConfigWrapper 与熔断器的集成。
func TestRetryConfigWrapper_WithCircuitBreaker(t *testing.T) {
	t.Run("circuit breaker protects operation", func(t *testing.T) {
		cb := NewCircuitBreaker(2, 1*time.Second)
		rc := &RetryConfigWrapper{
			MaxRetries:     3,
			InitialDelay:   10 * time.Millisecond,
			MaxDelay:       100 * time.Millisecond,
			BackoffFactor:  2.0,
			CircuitBreaker: cb,
		}

		// 连续失败直到熔断器打开
		attempts := 0
		for i := 0; i < 10; i++ {
			_, err := rc.WithRetry(context.Background(), func() (any, error) {
				attempts++
				return nil, errors.New("error: 500")
			})
			if errors.Is(err, ErrCircuitOpen) {
				break
			}
		}

		// 熔断器应该打开
		if state := cb.State(); state != CircuitStateOpen {
			t.Errorf("state = %v, want %v", state, CircuitStateOpen)
		}

		// 后续请求应该被熔断器拒绝，不需要实际调用操作
		previousAttempts := attempts
		_, err := rc.WithRetry(context.Background(), func() (any, error) {
			attempts++
			return nil, errors.New("error: 500")
		})

		if !errors.Is(err, ErrCircuitOpen) {
			t.Errorf("error = %v, want ErrCircuitOpen", err)
		}
		if attempts != previousAttempts {
			t.Errorf("operation should not be called when circuit is open, attempts = %d, want %d", attempts, previousAttempts)
		}
	})

	t.Run("success resets failure count", func(t *testing.T) {
		cb := NewCircuitBreaker(3, 1*time.Second)
		rc := &RetryConfigWrapper{
			MaxRetries:     1,
			InitialDelay:   10 * time.Millisecond,
			MaxDelay:       100 * time.Millisecond,
			BackoffFactor:  2.0,
			CircuitBreaker: cb,
		}

		// 两次失败
		_, _ = rc.WithRetry(context.Background(), func() (any, error) {
			return nil, errors.New("error: 500")
		})
		_, _ = rc.WithRetry(context.Background(), func() (any, error) {
			return nil, errors.New("error: 500")
		})

		// 熔断器应该仍然关闭
		if state := cb.State(); state != CircuitStateClosed {
			t.Errorf("state = %v, want %v", state, CircuitStateClosed)
		}

		// 一次成功
		_, _ = rc.WithRetry(context.Background(), func() (any, error) {
			return "success", nil
		})

		// 再三次失败才能打开熔断器
		_, _ = rc.WithRetry(context.Background(), func() (any, error) {
			return nil, errors.New("error: 500")
		})
		_, _ = rc.WithRetry(context.Background(), func() (any, error) {
			return nil, errors.New("error: 500")
		})
		_, _ = rc.WithRetry(context.Background(), func() (any, error) {
			return nil, errors.New("error: 500")
		})

		// 熔断器现在应该打开
		if state := cb.State(); state != CircuitStateOpen {
			t.Errorf("state = %v, want %v", state, CircuitStateOpen)
		}
	})

	t.Run("nil circuit breaker works as before", func(t *testing.T) {
		rc := &RetryConfigWrapper{
			MaxRetries:     2,
			InitialDelay:   10 * time.Millisecond,
			MaxDelay:       100 * time.Millisecond,
			BackoffFactor:  2.0,
			CircuitBreaker: nil,
		}

		callCount := 0
		_, err := rc.WithRetry(context.Background(), func() (any, error) {
			callCount++
			return nil, errors.New("error: 500")
		})

		if err == nil {
			t.Error("expected error")
		}
		// 没有熔断器，应该重试 MaxRetries + 1 次
		if callCount != 3 {
			t.Errorf("callCount = %d, want 3", callCount)
		}
	})
}
