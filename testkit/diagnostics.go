package testkit

import (
	"sync"

	"github.com/ajnart/self-host/internal/events"
)

// StartupPhases is the fixed, non-secret vocabulary exposed by startup errors.
// Raw events, error text, commands, Addresses and Project paths are never retained.
func StartupPhases() []string {
	return []string{"prepare", "render", "pull", "start", "ready", "api-key", "auth", "root-folder", "setup"}
}

type startupDiagnostics struct {
	mu    sync.Mutex
	phase string
}

func (d *startupDiagnostics) Emit(event events.Event) {
	if event.Event != events.StepStart {
		return
	}
	phase := "setup"
	switch event.Step {
	case "prepare", "render", "pull", "start", "ready", "api-key", "auth", "root-folder":
		phase = event.Step
	case "directories":
		phase = "prepare"
	}
	d.mu.Lock()
	d.phase = phase
	d.mu.Unlock()
}

func (d *startupDiagnostics) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	phase := d.phase
	if phase == "" {
		phase = "prepare"
	}
	return &StartupError{reason: StartupReasonSetup, message: "testkit installation or App Setup failed", phase: phase}
}
