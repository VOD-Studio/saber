package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
)

// RetryableError 判断错误是否可重试。
//
// 可重试的错误包括：
//   - HTTP 429（速率限制）、500、502、503、504 状态码
//   - 网络错误（超时、连接被拒绝、dial tcp、EOF）
//
// 身份验证错误不可重试。
func RetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 检查 HTTP 状态码错误
	errMsg := err.Error()
	httpStatusCodes := []string{"429", "500", "502", "503", "504"}
	for _, code := range httpStatusCodes {
		if strings.Contains(errMsg, code) {
			return true
		}
	}

	// 检查网络错误
	if errors.As(err, new(*net.OpError)) {
		return true
	}

	// 检查特定的网络相关错误消息
	networkErrors := []string{
		"timeout",
		"connection refused",
		"dial tcp",
		"EOF",
		"connection reset",
		"broken pipe",
	}
	for _, netErr := range networkErrors {
		if strings.Contains(strings.ToLower(errMsg), netErr) {
			return true
		}
	}

	// 身份验证错误通常不可重试
	authErrors := []string{
		"unauthorized",
		"forbidden",
		"invalid token",
		"authentication failed",
		"permission denied",
	}
	for _, authErr := range authErrors {
		if strings.Contains(strings.ToLower(errMsg), authErr) {
			return false
		}
	}

	return false
}

// RetryConfigWrapper 封装重试配置和逻辑。
type RetryConfigWrapper struct {
	// MaxRetries 最大重试次数
	MaxRetries int

	// InitialDelay 初始延迟时间
	InitialDelay time.Duration

	// MaxDelay 最大延迟时间
	MaxDelay time.Duration

	// BackoffFactor 退避因子（指数退避）
	BackoffFactor float64

	// FallbackModels 备用模型列表
	FallbackModels []string
}

// WithRetry 使用指数退避执行操作。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - operation: 要执行的操作函数，返回结果和错误
//
// 返回:
//   - interface{}: 操作结果
//   - error: 操作错误
func (rc *RetryConfigWrapper) WithRetry(ctx context.Context, operation func() (any, error)) (any, error) {
	var lastErr error
	delay := rc.InitialDelay

	for i := 0; i <= rc.MaxRetries; i++ {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("operation cancelled: %w", ctx.Err())
		default:
		}

		// 执行操作
		result, err := operation()
		if err == nil {
			return result, nil
		}

		lastErr = err

		// 如果不是最后一次重试且错误可重试，则等待后重试
		if i < rc.MaxRetries && RetryableError(err) {
			slog.Debug("Operation failed, will retry", "attempt", i+1, "error", err, "delay", delay)

			// 等待延迟时间
			select {
			case <-time.After(delay):
				// 继续下一次重试
			case <-ctx.Done():
				return nil, fmt.Errorf("operation cancelled during retry delay: %w", ctx.Err())
			}

			// 计算下一次延迟（指数退避）
			delay = min(time.Duration(float64(delay)*rc.BackoffFactor), rc.MaxDelay)
		} else {
			// 错误不可重试或已达到最大重试次数
			break
		}
	}

	return nil, fmt.Errorf("operation failed after %d attempts: %w", rc.MaxRetries+1, lastErr)
}

// FallbackModelHandler 处理模型故障转移逻辑。
type FallbackModelHandler struct {
	// Config AI 配置
	Config any

	// MainModel 主要模型标识符
	MainModel string

	// RetryConfig 重试配置
	RetryConfig *RetryConfigWrapper
}

// TryWithFallback 尝试使用主模型，失败时使用备用模型。
//
// 参数:
//   - ctx: 上下文，用于取消操作
//   - operation: 接受模型名称的操作函数
//
// 返回:
//   - any: 操作结果
//   - error: 操作错误
func (fmh *FallbackModelHandler) TryWithFallback(ctx context.Context, operation func(model string) (any, error)) (any, error) {
	// 首先尝试主模型
	result, err := fmh.RetryConfig.WithRetry(ctx, func() (any, error) {
		return operation(fmh.MainModel)
	})
	if err == nil {
		return result, nil
	}

	// 如果主模型失败且有备用模型，尝试备用模型
	if len(fmh.RetryConfig.FallbackModels) > 0 {
		slog.Info("Main model failed, trying fallback models", "main_model", fmh.MainModel, "fallback_count", len(fmh.RetryConfig.FallbackModels))

		for _, fallbackModel := range fmh.RetryConfig.FallbackModels {
			slog.Debug("Trying fallback model", "model", fallbackModel)

			fallbackResult, fallbackErr := fmh.RetryConfig.WithRetry(ctx, func() (any, error) {
				return operation(fallbackModel)
			})
			if fallbackErr == nil {
				slog.Info("Fallback model succeeded", "model", fallbackModel)
				return fallbackResult, nil
			}

			slog.Debug("Fallback model failed", "model", fallbackModel, "error", fallbackErr)
		}
	}

	return nil, fmt.Errorf("all models failed (main: %s, fallbacks: %v): %w",
		fmh.MainModel, fmh.RetryConfig.FallbackModels, err)
}
