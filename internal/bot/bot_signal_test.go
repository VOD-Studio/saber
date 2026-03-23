// Package bot_test 包含 setupSignalHandler 函数的单元测试。
package bot

import (
	"context"
	"testing"
	"time"

	"rua.plus/saber/internal/config"
)

// TestSetupSignalHandler_ContextCancel 测试 context 取消时的信号处理器。
func TestSetupSignalHandler_ContextCancel(t *testing.T) {
	cfg := &config.Config{}
	state := &appState{
		cfg: cfg,
	}

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	// 验证 context 初始状态
	select {
	case <-ctx.Done():
		t.Error("context should not be done initially")
	default:
		// 正确：context 未完成
	}

	// 取消 context
	cancel()

	// 等待 context 完成
	select {
	case <-ctx.Done():
		// 正确：context 已取消
	case <-time.After(time.Second):
		t.Error("context should be done after cancel")
	}
}

// TestSetupSignalHandler_MultipleCancel 测试多次调用 cancel 不会 panic。
func TestSetupSignalHandler_MultipleCancel(t *testing.T) {
	cfg := &config.Config{}
	state := &appState{
		cfg: cfg,
	}

	ctx, cancel := state.setupSignalHandler()

	// 多次调用 cancel 不应该 panic
	cancel()
	cancel()
	cancel()

	select {
	case <-ctx.Done():
		// 正确
	case <-time.After(time.Second):
		t.Error("context should be done")
	}
}

// TestSetupSignalHandler_DerivedContext 测试从返回的 context 派生新 context。
func TestSetupSignalHandler_DerivedContext(t *testing.T) {
	cfg := &config.Config{}
	state := &appState{
		cfg: cfg,
	}

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	// 派生新 context
	derivedCtx, derivedCancel := context.WithCancel(ctx)
	defer derivedCancel()

	// 取消父 context
	cancel()

	// 派生的 context 也应该被取消
	select {
	case <-derivedCtx.Done():
		// 正确：派生 context 被取消
	case <-time.After(time.Second):
		t.Error("derived context should be done when parent is cancelled")
	}
}

// TestSetupSignalHandler_TimeoutContext 测试在信号处理器 context 上添加超时。
func TestSetupSignalHandler_TimeoutContext(t *testing.T) {
	cfg := &config.Config{}
	state := &appState{
		cfg: cfg,
	}

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	// 添加超时
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer timeoutCancel()

	// 等待超时
	select {
	case <-timeoutCtx.Done():
		// 正确：超时触发
	case <-time.After(time.Second):
		t.Error("timeout context should be done after timeout")
	}

	// 检查错误是 DeadlineExceeded
	if timeoutCtx.Err() != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got %v", timeoutCtx.Err())
	}
}

// TestSetupSignalHandler_ContextValues 测试 context 值传递。
func TestSetupSignalHandler_ContextValues(t *testing.T) {
	cfg := &config.Config{}
	state := &appState{
		cfg: cfg,
	}

	ctx, cancel := state.setupSignalHandler()
	defer cancel()

	// 添加值到 context
	type key string
	valueCtx := context.WithValue(ctx, key("test"), "value")

	// 检索值
	if v := valueCtx.Value(key("test")); v != "value" {
		t.Errorf("expected 'value', got %v", v)
	}
}