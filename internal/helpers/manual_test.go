package helpers_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func TestStepUsesActionableManualHintAndPreservesGenericFallback(t *testing.T) {
	for _, scenario := range []struct {
		name string
		err  error
		want string
	}{
		{"generic", helpers.ErrManual, "generic [redacted] recovery"},
		{"wrapped-generic", fmt.Errorf("synthetic-password: %w", helpers.ErrManual), "generic [redacted] recovery"},
		{"actionable", &helpers.ManualError{Hint: "specific synthetic-password recovery"}, "specific [redacted] recovery"},
		{"wrapped-actionable", fmt.Errorf("outer: %w", &helpers.ManualError{Hint: "specific synthetic-password recovery"}), "specific [redacted] recovery"},
		{"empty-actionable", &helpers.ManualError{}, "generic [redacted] recovery"},
	} {
		for _, source := range []string{"apply", "check"} {
			t.Run(scenario.name+"/"+source, func(t *testing.T) {
				var records []helpers.StepResult
				h := newH(t, nil, nil, &records)
				h.Log.Secret("synthetic-password")
				var logged []events.Event
				h.Log.AddSink(events.SinkFunc(func(event events.Event) { logged = append(logged, event) }))
				spec := helpers.StepSpec{Hint: "generic synthetic-password recovery"}
				if source == "check" {
					spec.Check = func(context.Context) (bool, error) { return false, scenario.err }
				} else {
					spec.Do = func(context.Context) error { return scenario.err }
				}
				err := h.Step(t.Context(), "ownership", "review ownership", spec)
				if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) || !errors.Is(err, scenario.err) {
					t.Fatal("Manual error identity was lost or became a failure")
				}
				if len(records) != 1 || records[0].Status != stepstatus.StepManual || records[0].Hint != scenario.want || records[0].Error != "" {
					t.Fatal("recorder did not receive the expected safe Manual hint")
				}
				manual := false
				for _, event := range logged {
					if event.Event == events.StepManual {
						manual = true
						if event.Hint != scenario.want || event.Error != "" {
							t.Fatal("Manual event did not use the expected safe hint")
						}
					}
				}
				if !manual || strings.Contains(fmt.Sprint(records, logged, err), "synthetic-password") {
					t.Fatal("Manual diagnostics were missing or exposed a secret")
				}
			})
		}
	}
}
