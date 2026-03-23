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
		name     string
		err      error
		wantCode int
		wantOK   bool
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

// BotError 测试

func TestBotError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *BotError
		expected string
	}{
		{
			name:     "带原因的错误",
			err:      &BotError{Type: ErrorTypeConfig, Category: CategoryPermanent, Message: "配置错误", Cause: fmt.Errorf("文件不存在")},
			expected: "[config] 配置错误: 文件不存在",
		},
		{
			name:     "无原因的错误",
			err:      &BotError{Type: ErrorTypeNetwork, Category: CategoryTransient, Message: "网络错误"},
			expected: "[network] 网络错误",
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

func TestBotError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("底层错误")
	err := &BotError{Type: ErrorTypeAI, Message: "AI 错误", Cause: cause}

	unwrapped := err.Unwrap()
	if unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}

	// 测试无原因的情况
	errNoCause := &BotError{Type: ErrorTypeConfig, Message: "无原因错误"}
	if errNoCause.Unwrap() != nil {
		t.Error("Unwrap() for nil Cause should return nil")
	}
}

func TestBotError_IsTransient(t *testing.T) {
	tests := []struct {
		name     string
		err      *BotError
		expected bool
	}{
		{"暂时性错误", &BotError{Category: CategoryTransient}, true},
		{"永久性错误", &BotError{Category: CategoryPermanent}, false},
		{"致命错误", &BotError{Category: CategoryFatal}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsTransient(); got != tt.expected {
				t.Errorf("IsTransient() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBotError_IsRecoverable(t *testing.T) {
	tests := []struct {
		name     string
		err      *BotError
		expected bool
	}{
		{"可恢复", &BotError{Recover: true}, true},
		{"不可恢复", &BotError{Recover: false}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsRecoverable(); got != tt.expected {
				t.Errorf("IsRecoverable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestNewConfigError(t *testing.T) {
	cause := fmt.Errorf("yaml 解析失败")
	err := NewConfigError("无法加载配置", cause)

	if err.Type != ErrorTypeConfig {
		t.Errorf("Type = %s, want %s", err.Type, ErrorTypeConfig)
	}
	if err.Category != CategoryPermanent {
		t.Errorf("Category = %v, want %v", err.Category, CategoryPermanent)
	}
	if err.Message != "无法加载配置" {
		t.Errorf("Message = %s, want 无法加载配置", err.Message)
	}
	if err.Cause != cause {
		t.Errorf("Cause = %v, want %v", err.Cause, cause)
	}
	if err.Recover {
		t.Error("Recover should be false for config errors")
	}
	if err.Hint != "检查配置文件格式和内容" {
		t.Errorf("Hint = %s, want 检查配置文件格式和内容", err.Hint)
	}
}

func TestNewNetworkError(t *testing.T) {
	cause := fmt.Errorf("connection refused")
	err := NewNetworkError("网络连接失败", cause)

	if err.Type != ErrorTypeNetwork {
		t.Errorf("Type = %s, want %s", err.Type, ErrorTypeNetwork)
	}
	if err.Category != CategoryTransient {
		t.Errorf("Category = %v, want %v", err.Category, CategoryTransient)
	}
	if !err.Recover {
		t.Error("Recover should be true for network errors")
	}
}

func TestNewAuthError(t *testing.T) {
	cause := fmt.Errorf("invalid token")
	err := NewAuthError("认证失败", cause)

	if err.Type != ErrorTypeAuth {
		t.Errorf("Type = %s, want %s", err.Type, ErrorTypeAuth)
	}
	if err.Category != CategoryPermanent {
		t.Errorf("Category = %v, want %v", err.Category, CategoryPermanent)
	}
	if err.Recover {
		t.Error("Recover should be false for auth errors")
	}
}

func TestNewAIError(t *testing.T) {
	cause := fmt.Errorf("rate limit exceeded")

	t.Run("暂时性 AI 错误", func(t *testing.T) {
		err := NewAIError("AI 请求被限流", cause, CategoryTransient)

		if err.Type != ErrorTypeAI {
			t.Errorf("Type = %s, want %s", err.Type, ErrorTypeAI)
		}
		if err.Category != CategoryTransient {
			t.Errorf("Category = %v, want %v", err.Category, CategoryTransient)
		}
		if !err.Recover {
			t.Error("Recover should be true for transient AI errors")
		}
	})

	t.Run("永久性 AI 错误", func(t *testing.T) {
		err := NewAIError("API 密钥无效", cause, CategoryPermanent)

		if err.Category != CategoryPermanent {
			t.Errorf("Category = %v, want %v", err.Category, CategoryPermanent)
		}
		if err.Recover {
			t.Error("Recover should be false for permanent AI errors")
		}
	})
}

func TestNewFatalError(t *testing.T) {
	cause := fmt.Errorf("out of memory")
	err := NewFatalError("致命错误", cause)

	if err.Type != ErrorTypeInternal {
		t.Errorf("Type = %s, want %s", err.Type, ErrorTypeInternal)
	}
	if err.Category != CategoryFatal {
		t.Errorf("Category = %v, want %v", err.Category, CategoryFatal)
	}
	if err.Recover {
		t.Error("Recover should be false for fatal errors")
	}
}

func TestIsBotError(t *testing.T) {
	t.Run("是 BotError", func(t *testing.T) {
		err := NewConfigError("配置错误", nil)
		botErr, ok := IsBotError(err)
		if !ok {
			t.Error("IsBotError should return true for BotError")
		}
		if botErr.Type != ErrorTypeConfig {
			t.Errorf("Type = %s, want %s", botErr.Type, ErrorTypeConfig)
		}
	})

	t.Run("不是 BotError", func(t *testing.T) {
		err := fmt.Errorf("普通错误")
		_, ok := IsBotError(err)
		if ok {
			t.Error("IsBotError should return false for non-BotError")
		}
	})

	t.Run("包装的 BotError", func(t *testing.T) {
		originalErr := NewNetworkError("网络错误", nil)
		wrappedErr := fmt.Errorf("wrapped: %w", originalErr)

		botErr, ok := IsBotError(wrappedErr)
		if !ok {
			t.Error("IsBotError should find wrapped BotError")
		}
		if botErr.Type != ErrorTypeNetwork {
			t.Errorf("Type = %s, want %s", botErr.Type, ErrorTypeNetwork)
		}
	})
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"暂时性错误应重试", NewNetworkError("网络错误", nil), true},
		{"永久性错误不应重试", NewConfigError("配置错误", nil), false},
		{"普通错误不应重试", fmt.Errorf("普通错误"), false},
		{"nil 错误不应重试", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldRetry(tt.err); got != tt.expected {
				t.Errorf("ShouldRetry() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetRecoveryHint(t *testing.T) {
	t.Run("BotError 有提示", func(t *testing.T) {
		err := NewConfigError("配置错误", nil)
		hint := GetRecoveryHint(err)
		if hint != "检查配置文件格式和内容" {
			t.Errorf("Hint = %s, want 检查配置文件格式和内容", hint)
		}
	})

	t.Run("普通错误无提示", func(t *testing.T) {
		err := fmt.Errorf("普通错误")
		hint := GetRecoveryHint(err)
		if hint != "" {
			t.Errorf("Hint = %s, want empty string", hint)
		}
	})

	t.Run("nil 错误无提示", func(t *testing.T) {
		hint := GetRecoveryHint(nil)
		if hint != "" {
			t.Errorf("Hint = %s, want empty string", hint)
		}
	})
}

func TestBotError_WithErrorsIs(t *testing.T) {
	cause := fmt.Errorf("底层错误")
	err := NewConfigError("配置错误", cause)

	if !errors.Is(err, cause) {
		t.Error("errors.Is should find the underlying error")
	}
}

func TestBotError_WithErrorsAs(t *testing.T) {
	originalErr := NewNetworkError("网络错误", nil)
	wrappedErr := fmt.Errorf("wrapped: %w", originalErr)

	var botErr *BotError
	if !errors.As(wrappedErr, &botErr) {
		t.Error("errors.As should find the underlying BotError")
	}
	if botErr.Type != ErrorTypeNetwork {
		t.Errorf("Type = %s, want %s", botErr.Type, ErrorTypeNetwork)
	}
}
