package computer

type protectedError struct {
	message string
	cause   error
}

func (e protectedError) Error() string { return e.message }
func (e protectedError) Unwrap() error { return e.cause }
