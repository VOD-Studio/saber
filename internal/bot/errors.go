// Package bot 封装所有机器人初始化和运行逻辑。
package bot

import (
	"errors"
	"fmt"
	"log/slog"
)

// 错误类型常量
const (
	ErrorTypeConfig   = "config"
	ErrorTypeNetwork  = "network"
	ErrorTypeAuth     = "auth"
	ErrorTypeAI       = "ai"
	ErrorTypeMCP      = "mcp"
	ErrorTypeMatrix   = "matrix"
	ErrorTypeInternal = "internal"
)

// ErrorCategory 定义错误类别
type ErrorCategory int

const (
	// CategoryTransient 暂时性错误，可重试
	CategoryTransient ErrorCategory = iota
	// CategoryPermanent 永久性错误，需要干预
	CategoryPermanent
	// CategoryFatal 致命错误，需要退出
	CategoryFatal
)

// BotError 是机器人的详细错误类型。
type BotError struct {
	Type     string        // 错误类型
	Category ErrorCategory // 错误类别
	Message  string        // 错误消息
	Cause    error         // 原始错误
	Recover  bool          // 是否可恢复
	Hint     string        // 恢复建议
}

// Error 实现 error 接口。
func (e *BotError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Type, e.Message)
}

// Unwrap 返回底层错误，支持 errors.Is 和 errors.As。
func (e *BotError) Unwrap() error {
	return e.Cause
}

// IsTransient 检查错误是否为暂时性的。
func (e *BotError) IsTransient() bool {
	return e.Category == CategoryTransient
}

// IsRecoverable 检查错误是否可恢复。
func (e *BotError) IsRecoverable() bool {
	return e.Recover
}

// NewConfigError 创建配置错误。
func NewConfigError(message string, cause error) *BotError {
	return &BotError{
		Type:     ErrorTypeConfig,
		Category: CategoryPermanent,
		Message:  message,
		Cause:    cause,
		Recover:  false,
		Hint:     "检查配置文件格式和内容",
	}
}

// NewNetworkError 创建网络错误。
func NewNetworkError(message string, cause error) *BotError {
	return &BotError{
		Type:     ErrorTypeNetwork,
		Category: CategoryTransient,
		Message:  message,
		Cause:    cause,
		Recover:  true,
		Hint:     "检查网络连接，稍后重试",
	}
}

// NewAuthError 创建认证错误。
func NewAuthError(message string, cause error) *BotError {
	return &BotError{
		Type:     ErrorTypeAuth,
		Category: CategoryPermanent,
		Message:  message,
		Cause:    cause,
		Recover:  false,
		Hint:     "检查认证凭据是否正确",
	}
}

// NewAIError 创建 AI 服务错误。
func NewAIError(message string, cause error, category ErrorCategory) *BotError {
	return &BotError{
		Type:     ErrorTypeAI,
		Category: category,
		Message:  message,
		Cause:    cause,
		Recover:  category == CategoryTransient,
		Hint:     "检查 AI 服务配置和可用性",
	}
}

// NewFatalError 创建致命错误。
func NewFatalError(message string, cause error) *BotError {
	return &BotError{
		Type:     ErrorTypeInternal,
		Category: CategoryFatal,
		Message:  message,
		Cause:    cause,
		Recover:  false,
		Hint:     "查看日志获取详细信息",
	}
}

// IsBotError 检查错误是否为 BotError 类型。
func IsBotError(err error) (*BotError, bool) {
	var botErr *BotError
	if errors.As(err, &botErr) {
		return botErr, true
	}
	return nil, false
}

// ShouldRetry 判断错误是否应该重试。
func ShouldRetry(err error) bool {
	if botErr, ok := IsBotError(err); ok {
		return botErr.IsTransient()
	}
	return false
}

// GetRecoveryHint 获取错误的恢复建议。
func GetRecoveryHint(err error) string {
	if botErr, ok := IsBotError(err); ok {
		return botErr.Hint
	}
	return ""
}

// LogError 记录错误并包含恢复建议。
func LogError(err error) {
	if botErr, ok := IsBotError(err); ok {
		slog.Error(botErr.Message,
			"type", botErr.Type,
			"category", botErr.Category,
			"recoverable", botErr.Recover,
			"hint", botErr.Hint,
			"cause", botErr.Cause)
		return
	}
	slog.Error("发生错误", "error", err)
}

// ExitCodeError 是一种特殊错误，指示程序应以特定退出码结束。
//
// 这允许调用者区分致命错误和正常退出（如 --version 标志），
// 同时保持优雅关闭能力。
type ExitCodeError struct {
	// Code 是程序应退出的状态码。
	Code int
	// Err 是底层错误（可选）。
	Err error
}

// Error 实现 error 接口。
func (e *ExitCodeError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Code == 0 {
		return "正常退出"
	}
	return fmt.Sprintf("退出码: %d", e.Code)
}

// Unwrap 返回底层错误，支持 errors.Is 和 errors.As。
func (e *ExitCodeError) Unwrap() error {
	return e.Err
}

// IsExitCode 检查错误是否为 ExitCodeError 并返回退出码。
//
// 示例:
//
//	if code, ok := bot.IsExitCode(err); ok {
//	    os.Exit(code)
//	}
func IsExitCode(err error) (int, bool) {
	var exitErr *ExitCodeError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	return 0, false
}

// ExitSuccess 创建一个表示成功退出的 ExitCodeError。
//
// 用于 --version、--help 等标志处理。
func ExitSuccess() *ExitCodeError {
	return &ExitCodeError{Code: 0}
}

// ExitError 创建一个表示错误退出的 ExitCodeError。
//
// 参数:
//   - code: 非 zero 的退出码
//   - err: 描述错误的底层错误
func ExitError(code int, err error) *ExitCodeError {
	return &ExitCodeError{Code: code, Err: err}
}