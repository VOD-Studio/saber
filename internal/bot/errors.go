// Package bot 封装所有机器人初始化和运行逻辑。
package bot

import (
	"errors"
	"fmt"
)

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