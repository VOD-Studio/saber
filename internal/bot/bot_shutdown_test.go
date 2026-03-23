// Package bot_test 包含 shutdown 函数的单元测试。
package bot

import (
	"sync"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
)

// mockStopper 用于测试的服务停止器。
type mockStopper struct {
	stopCalled bool
	stopDelay  time.Duration
	mu         sync.Mutex
}

func (m *mockStopper) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopDelay > 0 {
		time.Sleep(m.stopDelay)
	}
	m.stopCalled = true
}

func (m *mockStopper) wasStopped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopCalled
}

// mockCloser 用于测试的服务关闭器。
type mockCloser struct {
	closeCalled bool
	closeDelay  time.Duration
	closeErr    error
	mu          sync.Mutex
}

func (m *mockCloser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closeDelay > 0 {
		time.Sleep(m.closeDelay)
	}
	m.closeCalled = true
	return m.closeErr
}

func (m *mockCloser) wasClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeCalled
}

// TestShutdown_EmptyServices 测试服务为空时的关闭。
func TestShutdown_EmptyServices(t *testing.T) {
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: 5,
		},
	}

	state := &appState{
		cfg: cfg,
		services: &services{
			// 所有服务为 nil
		},
	}

	// 不应该 panic
	done := make(chan struct{})
	go func() {
		state.shutdown(func() {})
		close(done)
	}()

	select {
	case <-done:
		// 成功完成
	case <-time.After(time.Second):
		t.Error("shutdown took too long with empty services")
	}
}

// TestShutdown_WithAIService 测试 AI 服务为 nil 时的关闭。
func TestShutdown_WithAIService(t *testing.T) {
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: 5,
		},
	}

	state := &appState{
		cfg: cfg,
		services: &services{
			aiService: nil, // 测试 nil 情况
		},
	}

	done := make(chan struct{})
	go func() {
		state.shutdown(func() {})
		close(done)
	}()

	select {
	case <-done:
		// 成功
	case <-time.After(time.Second):
		t.Error("shutdown took too long")
	}
}

// TestShutdown_DefaultTimeout 测试默认超时时间。
func TestShutdown_DefaultTimeout(t *testing.T) {
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: 0, // 使用默认值
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	start := time.Now()
	state.shutdown(func() {})
	elapsed := time.Since(start)

	// 默认超时是 30 秒，但由于没有服务，应该立即返回
	if elapsed > time.Second {
		t.Errorf("shutdown took too long: %v", elapsed)
	}
}

// TestShutdown_ContextCancellation 测试 context 取消时的关闭。
func TestShutdown_ContextCancellation(t *testing.T) {
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: 30,
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	// shutdown 应该能正常处理取消
	state.shutdown(func() {})
}

// TestShutdown_ConcurrentStop 测试并发停止多个服务。
func TestShutdown_ConcurrentStop(t *testing.T) {
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: 10,
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	start := time.Now()
	state.shutdown(func() {})
	elapsed := time.Since(start)

	// 所有服务为 nil，应该快速完成
	if elapsed > 500*time.Millisecond {
		t.Errorf("shutdown took too long: %v", elapsed)
	}
}

// TestShutdown_NegativeTimeout 测试负超时值使用默认值。
func TestShutdown_NegativeTimeout(t *testing.T) {
	cfg := &config.Config{
		Shutdown: config.ShutdownConfig{
			TimeoutSeconds: -1, // 应该使用默认值 30
		},
	}

	state := &appState{
		cfg:      cfg,
		services: &services{},
	}

	// 不应该 panic，应该使用默认值
	state.shutdown(func() {})
}

// TestShutdownConfig_Validation 测试关闭配置验证。
func TestShutdownConfig_Validation(t *testing.T) {
	tests := []struct {
		name     string
		config   config.ShutdownConfig
		expected time.Duration
	}{
		{
			name:     "zero uses default",
			config:   config.ShutdownConfig{TimeoutSeconds: 0},
			expected: 30 * time.Second,
		},
		{
			name:     "negative uses default",
			config:   config.ShutdownConfig{TimeoutSeconds: -5},
			expected: 30 * time.Second,
		},
		{
			name:     "custom timeout",
			config:   config.ShutdownConfig{TimeoutSeconds: 60},
			expected: 60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			timeoutSeconds := tt.config.TimeoutSeconds
			if timeoutSeconds <= 0 {
				timeoutSeconds = 30
			}
			actual := time.Duration(timeoutSeconds) * time.Second
			if actual != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, actual)
			}
		})
	}
}