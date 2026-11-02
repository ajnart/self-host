package testkit

type StartupError struct {
	reason  string
	message string
}

func (e *StartupError) Error() string { return e.message }

func (e *StartupError) Reason() string { return e.reason }

func startupError(reason, message string) error {
	return &StartupError{reason: reason, message: message}
}
