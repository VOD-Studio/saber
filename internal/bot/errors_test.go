package bot

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *ExitCodeError
		expected string
	}{
		{
			name:     "成功退出无底层错误",
			err:      &ExitCodeError{Code: 0},
			expected: "正常退出",
		},
		{
			name:     "错误退出无底层错误",
			err:      &ExitCodeError{Code: 1},
			expected: "退出码: 1",
		},
		{
			name:     "带底层错误",
			err:      &ExitCodeError{Code: 1, Err: fmt.Errorf("配置文件不存在")},
			expected: "配置文件不存在",
		},
		{
			name:     "成功退出带底层错误（罕见情况）",
			err:      &ExitCodeError{Code: 0, Err: fmt.Errorf("some message")},
			expected: "some message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.expected {
				t.Errorf("Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExitCodeError_Unwrap(t *testing.T) {
	underlyingErr := fmt.Errorf("底层错误")
	err := &ExitCodeError{Code: 1, Err: underlyingErr}

	unwrapped := err.Unwrap()
	if unwrapped != underlyingErr {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, underlyingErr)
	}

	// 测试无底层错误的情况
	errNoUnderlying := &ExitCodeError{Code: 0}
	if errNoUnderlying.Unwrap() != nil {
		t.Errorf("Unwrap() for nil Err should return nil")
	}
}

func TestIsExitCode(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantCode     int
		wantOK       bool
	}{
		{
			name:     "ExitCodeError 成功退出",
			err:      &ExitCodeError{Code: 0},
			wantCode: 0,
			wantOK:   true,
		},
		{
			name:     "ExitCodeError 错误退出",
			err:      &ExitCodeError{Code: 1},
			wantCode: 1,
			wantOK:   true,
		},
		{
			name:     "ExitCodeError 带底层错误",
			err:      &ExitCodeError{Code: 2, Err: fmt.Errorf("test")},
			wantCode: 2,
			wantOK:   true,
		},
		{
			name:     "普通错误",
			err:      fmt.Errorf("普通错误"),
			wantCode: 0,
			wantOK:   false,
		},
		{
			name:     "nil 错误",
			err:      nil,
			wantCode: 0,
			wantOK:   false,
		},
		{
			name:     "包装的 ExitCodeError",
			err:      fmt.Errorf("wrapped: %w", &ExitCodeError{Code: 3}),
			wantCode: 3,
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := IsExitCode(tt.err)
			if ok != tt.wantOK {
				t.Errorf("IsExitCode() ok = %v, want %v", ok, tt.wantOK)
			}
			if code != tt.wantCode {
				t.Errorf("IsExitCode() code = %d, want %d", code, tt.wantCode)
			}
		})
	}
}

func TestExitSuccess(t *testing.T) {
	err := ExitSuccess()

	if err.Code != 0 {
		t.Errorf("ExitSuccess().Code = %d, want 0", err.Code)
	}
	if err.Err != nil {
		t.Errorf("ExitSuccess().Err = %v, want nil", err.Err)
	}

	// 验证它是 ExitCodeError
	code, ok := IsExitCode(err)
	if !ok {
		t.Error("ExitSuccess() should be recognized as ExitCodeError")
	}
	if code != 0 {
		t.Errorf("ExitSuccess() code = %d, want 0", code)
	}
}

func TestExitError(t *testing.T) {
	underlyingErr := fmt.Errorf("配置加载失败")
	err := ExitError(1, underlyingErr)

	if err.Code != 1 {
		t.Errorf("ExitError().Code = %d, want 1", err.Code)
	}
	if err.Err != underlyingErr {
		t.Errorf("ExitError().Err = %v, want %v", err.Err, underlyingErr)
	}

	// 验证它是 ExitCodeError
	code, ok := IsExitCode(err)
	if !ok {
		t.Error("ExitError() should be recognized as ExitCodeError")
	}
	if code != 1 {
		t.Errorf("ExitError() code = %d, want 1", code)
	}
}

func TestExitCodeError_WithErrorsIs(t *testing.T) {
	// 定义一个哨兵错误
	ErrNotFound := fmt.Errorf("not found")

	// 测试 errors.Is 是否能正确工作
	err := &ExitCodeError{Code: 1, Err: ErrNotFound}

	if !errors.Is(err, ErrNotFound) {
		t.Error("errors.Is should find the underlying error")
	}

	// 测试不匹配的情况
	ErrOther := fmt.Errorf("other error")
	if errors.Is(err, ErrOther) {
		t.Error("errors.Is should not find unrelated error")
	}
}

func TestExitCodeError_WithErrorsAs(t *testing.T) {
	// 使用一个已实现 error 接口的错误类型
	customErr := fmt.Errorf("custom error")
	err := &ExitCodeError{Code: 1, Err: customErr}

	// 验证底层错误存在
	if !errors.Is(err, customErr) {
		t.Error("errors.Is should find the underlying error")
	}

	// 使用 fmt.Errorf 包装测试 errors.As
	wrappedErr := fmt.Errorf("wrapped: %w", err)
	var target *ExitCodeError
	if !errors.As(wrappedErr, &target) {
		t.Error("errors.As should find the underlying ExitCodeError")
	}
	if target.Code != 1 {
		t.Errorf("ExitCodeError.Code = %d, want 1", target.Code)
	}
}