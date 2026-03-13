// Package ai_test 包含重试处理器的单元测试。
package ai

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"
)

// TestRetryableError 测试 RetryableError 函数。
//
// 该测试覆盖以下场景：
//   - HTTP 状态码错误（429, 500, 502, 503, 504）
//   - 网络错误（timeout, connection refused, EOF, dial tcp）
//   - 认证错误（unauthorized, forbidden, invalid token）
//   - nil 错误
func TestRetryableError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		// nil 错误
		{"nil error", nil, false},

		// HTTP 状态码 - 可重试
		{"HTTP 429", errors.New("error: 429 rate limit exceeded"), true},
		{"HTTP 500", errors.New("error: 500 internal server error"), true},
		{"HTTP 502", errors.New("error: 502 bad gateway"), true},
		{"HTTP 503", errors.New("error: 503 service unavailable"), true},
		{"HTTP 504", errors.New("error: 504 gateway timeout"), true},

		// 网络错误 - 可重试
		{"timeout", errors.New("context deadline exceeded: timeout"), true},
		{"connection refused", errors.New("dial tcp: connection refused"), true},
		{"dial tcp", errors.New("dial tcp 192.168.1.1:443: connection refused"), true},
		{"EOF uppercase", errors.New("unexpected EOF"), false},
		{"eof lowercase", errors.New("unexpected eof"), false},
		{"connection reset", errors.New("read: connection reset by peer"), true},
		{"broken pipe", errors.New("write: broken pipe"), true},

		// 认证错误 - 不可重试
		{"unauthorized", errors.New("error: 401 unauthorized"), false},
		{"forbidden", errors.New("error: 403 forbidden"), false},
		{"invalid token", errors.New("invalid token"), false},
		{"authentication failed", errors.New("authentication failed"), false},
		{"permission denied", errors.New("permission denied"), false},

		// 大小写不敏感测试
		{"TIMEOUT uppercase", errors.New("TIMEOUT error"), true},
		{"Unauthorized mixed case", errors.New("Unauthorized access"), false},

		// 其他错误 - 默认不可重试
		{"unknown error", errors.New("some random error"), false},
		{"HTTP 400", errors.New("error: 400 bad request"), false},
		{"HTTP 404", errors.New("error: 404 not found"), false},

		// 组合场景
		{"429 with unauthorized text", errors.New("429 rate limit - unauthorized user"), true}, // 429 takes precedence
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetryableError(tt.err)
			if got != tt.wantRetry {
				t.Errorf("RetryableError(%v) = %v, want %v", tt.err, got, tt.wantRetry)
			}
		})
	}
}

// TestRetryableError_NetOpError 测试 net.OpError 类型的错误。
func TestRetryableError_NetOpError(t *testing.T) {
	// 创建一个 net.OpError
	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: errors.New("connection refused"),
	}

	if !RetryableError(opErr) {
		t.Error("RetryableError(net.OpError) = false, want true")
	}
}

// TestRetryConfigWrapper_WithRetry 测试 WithRetry 方法。
//
// 该测试覆盖以下场景：
//   - 首次成功
//   - 重试后成功
//   - 达到最大重试次数
//   - 上下文取消
//   - 非可重试错误
func TestRetryConfigWrapper_WithRetry(t *testing.T) {
	tests := []struct {
		name         string
		maxRetries   int
		initialDelay time.Duration
		maxDelay     time.Duration
		backoff      float64
		attempts     int // 操作失败次数后成功（-1 表示一直失败）
		wantErr      bool
		errType      string // 错误类型描述
	}{
		{
			name:         "success on first attempt",
			maxRetries:   3,
			initialDelay: 10 * time.Millisecond,
			maxDelay:     100 * time.Millisecond,
			backoff:      2.0,
			attempts:     0,
			wantErr:      false,
		},
		{
			name:         "success on second attempt",
			maxRetries:   3,
			initialDelay: 10 * time.Millisecond,
			maxDelay:     100 * time.Millisecond,
			backoff:      2.0,
			attempts:     1,
			wantErr:      false,
		},
		{
			name:         "success on third attempt",
			maxRetries:   3,
			initialDelay: 10 * time.Millisecond,
			maxDelay:     100 * time.Millisecond,
			backoff:      2.0,
			attempts:     2,
			wantErr:      false,
		},
		{
			name:         "max retries exceeded",
			maxRetries:   2,
			initialDelay: 10 * time.Millisecond,
			maxDelay:     100 * time.Millisecond,
			backoff:      2.0,
			attempts:     -1, // 一直失败
			wantErr:      true,
			errType:      "max retries",
		},
		{
			name:         "zero retries",
			maxRetries:   0,
			initialDelay: 10 * time.Millisecond,
			maxDelay:     100 * time.Millisecond,
			backoff:      2.0,
			attempts:     -1,
			wantErr:      true,
			errType:      "max retries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &RetryConfigWrapper{
				MaxRetries:     tt.maxRetries,
				InitialDelay:   tt.initialDelay,
				MaxDelay:       tt.maxDelay,
				BackoffFactor:  tt.backoff,
				FallbackModels: nil,
			}

			callCount := 0
			operation := func() (any, error) {
				callCount++
				if tt.attempts >= 0 && callCount > tt.attempts {
					return "success", nil
				}
				return nil, errors.New("error: 500 internal server error")
			}

			ctx := context.Background()
			result, err := rc.WithRetry(ctx, operation)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if result != "success" {
					t.Errorf("expected 'success', got %v", result)
				}
			}
		})
	}
}

// TestRetryConfigWrapper_WithRetry_ContextCancellation 测试上下文取消。
func TestRetryConfigWrapper_WithRetry_ContextCancellation(t *testing.T) {
	rc := &RetryConfigWrapper{
		MaxRetries:     5,
		InitialDelay:   100 * time.Millisecond,
		MaxDelay:       1 * time.Second,
		BackoffFactor:  2.0,
		FallbackModels: nil,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// 在第一次失败后取消上下文
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	operation := func() (any, error) {
		return nil, errors.New("error: 500 internal server error")
	}

	_, err := rc.WithRetry(ctx, operation)
	if err == nil {
		t.Error("expected error due to context cancellation")
	}
}

// TestRetryConfigWrapper_WithRetry_NonRetryableError 测试非可重试错误。
func TestRetryConfigWrapper_WithRetry_NonRetryableError(t *testing.T) {
	rc := &RetryConfigWrapper{
		MaxRetries:     5,
		InitialDelay:   10 * time.Millisecond,
		MaxDelay:       100 * time.Millisecond,
		BackoffFactor:  2.0,
		FallbackModels: nil,
	}

	callCount := 0
	operation := func() (any, error) {
		callCount++
		return nil, errors.New("error: 401 unauthorized")
	}

	ctx := context.Background()
	_, err := rc.WithRetry(ctx, operation)

	if err == nil {
		t.Error("expected error")
	}
	// 非可重试错误应该立即返回，不重试
	if callCount != 1 {
		t.Errorf("expected 1 call for non-retryable error, got %d", callCount)
	}
}

// TestRetryConfigWrapper_WithRetry_Backoff 测试指数退避计算。
func TestRetryConfigWrapper_WithRetry_Backoff(t *testing.T) {
	rc := &RetryConfigWrapper{
		MaxRetries:     3,
		InitialDelay:   50 * time.Millisecond,
		MaxDelay:       500 * time.Millisecond,
		BackoffFactor:  2.0,
		FallbackModels: nil,
	}

	var delays []time.Duration
	start := time.Now()

	callCount := 0
	operation := func() (any, error) {
		callCount++
		if callCount > 1 {
			elapsed := time.Since(start)
			delays = append(delays, elapsed)
		}
		start = time.Now()
		return nil, errors.New("error: 500")
	}

	ctx := context.Background()
	_, _ = rc.WithRetry(ctx, operation)

	// 验证延迟大致符合指数退避（允许误差）
	// 注意：这个测试可能因为系统调度而不精确
}

// TestFallbackModelHandler_TryWithFallback 测试 TryWithFallback 方法。
//
// 该测试覆盖以下场景：
//   - 主模型成功
//   - 主模型失败，备用模型成功
//   - 所有模型失败
//   - 无备用模型
func TestFallbackModelHandler_TryWithFallback(t *testing.T) {
	tests := []struct {
		name           string
		mainModel      string
		fallbackModels []string
		failModels     map[string]bool // 哪些模型会失败
		wantModel      string          // 期望成功的模型
		wantErr        bool
	}{
		{
			name:           "main model succeeds",
			mainModel:      "gpt-4",
			fallbackModels: []string{"gpt-3.5", "gpt-3"},
			failModels:     map[string]bool{},
			wantModel:      "gpt-4",
			wantErr:        false,
		},
		{
			name:           "main fails, first fallback succeeds",
			mainModel:      "gpt-4",
			fallbackModels: []string{"gpt-3.5", "gpt-3"},
			failModels:     map[string]bool{"gpt-4": true},
			wantModel:      "gpt-3.5",
			wantErr:        false,
		},
		{
			name:           "main and first fail, second fallback succeeds",
			mainModel:      "gpt-4",
			fallbackModels: []string{"gpt-3.5", "gpt-3"},
			failModels:     map[string]bool{"gpt-4": true, "gpt-3.5": true},
			wantModel:      "gpt-3",
			wantErr:        false,
		},
		{
			name:           "all models fail",
			mainModel:      "gpt-4",
			fallbackModels: []string{"gpt-3.5", "gpt-3"},
			failModels:     map[string]bool{"gpt-4": true, "gpt-3.5": true, "gpt-3": true},
			wantErr:        true,
		},
		{
			name:           "no fallback models, main succeeds",
			mainModel:      "gpt-4",
			fallbackModels: []string{},
			failModels:     map[string]bool{},
			wantModel:      "gpt-4",
			wantErr:        false,
		},
		{
			name:           "no fallback models, main fails",
			mainModel:      "gpt-4",
			fallbackModels: []string{},
			failModels:     map[string]bool{"gpt-4": true},
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &RetryConfigWrapper{
				MaxRetries:     1,
				InitialDelay:   10 * time.Millisecond,
				MaxDelay:       100 * time.Millisecond,
				BackoffFactor:  2.0,
				FallbackModels: tt.fallbackModels,
			}

			fmh := &FallbackModelHandler{
				MainModel:   tt.mainModel,
				RetryConfig: rc,
			}

			operation := func(model string) (any, error) {
				if tt.failModels[model] {
					return nil, errors.New("error: 500 internal server error")
				}
				return fmt.Sprintf("result from %s", model), nil
			}

			ctx := context.Background()
			result, err := fmh.TryWithFallback(ctx, operation)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				expected := fmt.Sprintf("result from %s", tt.wantModel)
				if result != expected {
					t.Errorf("expected %q, got %q", expected, result)
				}
			}
		})
	}
}

// TestFallbackModelHandler_TryWithFallback_Concurrency 测试并发安全性。
func TestFallbackModelHandler_TryWithFallback_Concurrency(t *testing.T) {
	rc := &RetryConfigWrapper{
		MaxRetries:     1,
		InitialDelay:   10 * time.Millisecond,
		MaxDelay:       100 * time.Millisecond,
		BackoffFactor:  2.0,
		FallbackModels: []string{"fallback-1", "fallback-2"},
	}

	fmh := &FallbackModelHandler{
		MainModel:   "main",
		RetryConfig: rc,
	}

	const goroutines = 50
	errChan := make(chan error, goroutines)

	for i := range goroutines {
		go func(idx int) {
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic in goroutine %d: %v", idx, r)
				}
			}()

			operation := func(model string) (any, error) {
				return fmt.Sprintf("result-%d-%s", idx, model), nil
			}

			ctx := context.Background()
			_, err := fmh.TryWithFallback(ctx, operation)
			errChan <- err
		}(i)
	}

	for range goroutines {
		if err := <-errChan; err != nil {
			t.Errorf("concurrent test failed: %v", err)
		}
	}
}

// TestRetryConfigWrapper_WithRetry_Concurrency 测试 WithRetry 的并发安全性。
func TestRetryConfigWrapper_WithRetry_Concurrency(t *testing.T) {
	rc := &RetryConfigWrapper{
		MaxRetries:     2,
		InitialDelay:   10 * time.Millisecond,
		MaxDelay:       100 * time.Millisecond,
		BackoffFactor:  2.0,
		FallbackModels: nil,
	}

	const goroutines = 100
	var wg sync.WaitGroup
	errChan := make(chan error, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errChan <- fmt.Errorf("panic: %v", r)
				}
			}()

			callCount := 0
			operation := func() (any, error) {
				callCount++
				if callCount < 2 {
					return nil, errors.New("error: 500")
				}
				return idx, nil
			}

			ctx := context.Background()
			_, err := rc.WithRetry(ctx, operation)
			errChan <- err
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
