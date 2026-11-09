package testkit

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestStartupDiagnosticsAllowlist(t *testing.T) {
	for _, step := range append(StartupPhases(), "directories", "private-key-value") {
		t.Run(step, func(t *testing.T) {
			d := &startupDiagnostics{}
			d.Emit(events.Event{Event: events.StepStart, Step: step, Error: "private-key-value", Command: "private-key-value", Msg: "private-key-value", URL: "private-key-value"})
			failure := d.failure().(*StartupError)
			if !slices.Contains(StartupPhases(), failure.Phase()) || strings.Contains(failure.Error(), "private-key-value") || failure.Reason() != string(StartupReasonSetup) {
				t.Fatal("diagnostic leaked data or changed the existing reason")
			}
			want := step
			if step == "directories" {
				want = "prepare"
			}
			if step == "private-key-value" {
				want = "setup"
			}
			if failure.Phase() != want {
				t.Fatalf("phase = %s, want %s", failure.Phase(), want)
			}
		})
	}
	d := &startupDiagnostics{}
	d.Emit(events.Event{Event: events.Info, Step: "private-key-value"})
	if d.failure().(*StartupError).Phase() != "prepare" {
		t.Fatal("pre-install failure lost its phase")
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() { d.Emit(events.Event{Event: events.StepStart, Step: "pull"}); d.failure() })
	}
	wg.Wait()
}

func TestSingleAppStartupPhaseSurvivesScopedCleanup(t *testing.T) {
	f := newFakeDocker(t)
	_, err := startFake(t, f, fixtureApp{setup: func(ctx context.Context, h *helpers.H) error {
		return h.Step(ctx, "auth", "synthetic authentication", helpers.StepSpec{Do: func(context.Context) error { return errors.New("private-key-value") }})
	}})
	var failure *StartupError
	if !errors.As(err, &failure) || failure.Phase() != "auth" || failure.Reason() != string(StartupReasonSetup) {
		t.Fatalf("missing safe startup diagnostic: %v", err)
	}
	if strings.Contains(err.Error(), "private-key-value") || errors.Is(err, ErrCleanupIncomplete) || len(f.containers) != 0 {
		t.Fatal("failed startup leaked data or resources")
	}
}

func TestContainerStartPhaseSurvivesScopedCleanup(t *testing.T) {
	f := newFakeDocker(t)
	f.partialFailure = true
	_, err := startFake(t, f, fixtureApp{})
	var failure *StartupError
	if !errors.As(err, &failure) || failure.Phase() != "start" || failure.Reason() != string(StartupReasonSetup) {
		t.Fatalf("missing container-start phase: %v", err)
	}
	if strings.Contains(err.Error(), syntheticKey) || errors.Is(err, ErrCleanupIncomplete) || len(f.containers) != 0 {
		t.Fatal("failed start leaked data or resources")
	}
}
