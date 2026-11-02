package testkit

type StartupReason string

const (
	StartupReasonApp          StartupReason = "APP"
	StartupReasonOptions      StartupReason = "OPTIONS"
	StartupReasonDocker       StartupReason = "DOCKER"
	StartupReasonCapabilities StartupReason = "CAPABILITIES"
	StartupReasonPort         StartupReason = "PORT"
	StartupReasonOwnership    StartupReason = "OWNERSHIP"
	StartupReasonLimits       StartupReason = "LIMITS"
	StartupReasonLock         StartupReason = "LOCK"
	StartupReasonUnsupported  StartupReason = "UNSUPPORTED"
	StartupReasonStorage      StartupReason = "STORAGE"
	StartupReasonContainer    StartupReason = "CONTAINER"
	StartupReasonSetup        StartupReason = "SETUP"
)

func StartupReasons() map[StartupReason]string {
	return map[StartupReason]string{
		StartupReasonApp:          "Choose one supported Catalog App.",
		StartupReasonOptions:      "Check the selected image and lifetime options.",
		StartupReasonDocker:       "Check the local Unix Docker socket, daemon and Compose plugin.",
		StartupReasonCapabilities: "Docker must support memory, CPU quota and PID limits.",
		StartupReasonPort:         "A required local port could not be reserved.",
		StartupReasonOwnership:    "Fixture ownership or isolation could not be verified.",
		StartupReasonLimits:       "Container resource caps or selected image could not be verified.",
		StartupReasonLock:         "The private startup lock is unavailable.",
		StartupReasonUnsupported:  "This App's Compose configuration or operations are unsupported.",
		StartupReasonStorage:      "Private fixture storage or the recovery manifest is unavailable.",
		StartupReasonContainer:    "The selected App container is absent.",
		StartupReasonSetup:        "App Setup failed or requires a Manual step.",
	}
}

type StartupError struct {
	reason  StartupReason
	message string
}

func (e *StartupError) Error() string { return e.message }

func (e *StartupError) Reason() string { return string(e.reason) }

func startupError(reason StartupReason, message string) error {
	return &StartupError{reason: reason, message: message}
}
