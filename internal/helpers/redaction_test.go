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

func TestStepRedactsRecordsAndReturnedFailure(t *testing.T) {
	for _, outcome := range []stepstatus.Step{stepstatus.StepOK, stepstatus.StepSkip, stepstatus.StepFail} {
		t.Run(string(outcome), func(t *testing.T) {
			var records []helpers.StepResult
			h := newH(t, nil, nil, &records)
			var failedEvent events.Event
			h.Log.AddSink(events.SinkFunc(func(event events.Event) {
				if event.Event == events.StepFail {
					failedEvent = event
				}
			}))
			h.Log.Secret("synthetic-password")
			cause := errors.New("refused synthetic-password")
			spec := helpers.StepSpec{Hint: "retry synthetic-password"}
			switch outcome {
			case stepstatus.StepSkip:
				spec.Check = func(context.Context) (bool, error) { return true, nil }
			case stepstatus.StepFail:
				spec.Do = func(context.Context) error { return cause }
			}
			err := h.Step(context.Background(), "login", "login synthetic-password", spec)
			if len(records) != 1 || strings.Contains(fmt.Sprint(records), "synthetic-password") {
				t.Fatalf("credential reached recorder: %+v", records)
			}
			if outcome == stepstatus.StepFail {
				if err == nil || strings.Contains(err.Error(), "synthetic-password") || !errors.Is(err, cause) || !errors.Is(err, helpers.ErrStepFailed) {
					t.Fatalf("failure not redacted or cause lost: %v", err)
				}
				if failedEvent.Cause != cause {
					t.Fatalf("StepFail event cause = %v, want original cause", failedEvent.Cause)
				}
			}
		})
	}
}
