package browser

// redactedError 隐去模型可见秘密，同时保留取消与权限错误的原始链。
type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.cause }
