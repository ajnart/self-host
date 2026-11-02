package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type applicableTestApp struct {
	*testApp
	links []app.Link
}

func (a *applicableTestApp) Links() []app.Link { return a.links }

func TestLinkApplicabilityPrecedesReadinessAndReplacesStaleResults(t *testing.T) {
	consumer := &applicableTestApp{testApp: &testApp{id: "consumer", done: map[string]bool{}}}
	target := &testApp{id: "target", done: map[string]bool{}, fail: errors.New("unready")}
	h := newHarness(t, consumer, target)
	_ = h.install("consumer", "target")
	ss, err := h.svc.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	selected, applied, predicates := false, 0, 0
	link := app.Link{ID: "link-target", Target: "target", Title: "selected server",
		Applicable: func(context.Context, *helpers.H) (bool, error) {
			predicates++
			return selected, nil
		},
		Apply: func(context.Context, *helpers.H, *helpers.H) error { applied++; return nil },
	}
	for _, prior := range []stepstatus.Step{stepstatus.StepSkip, stepstatus.StepFail, stepstatus.StepManual} {
		ss.state.Apps["consumer"].Steps[link.ID] = project.StepRecord{Status: prior, Msg: linkWaitingMessage("target"), Error: "old failure", Hint: "old hint"}
		ss.applyLink(context.Background(), consumer, link, []string{"consumer", "target"})
		got := ss.state.Apps["consumer"].Steps[link.ID]
		if got.Status != stepstatus.StepSkip || got.Msg != "not selected: selected server" || got.Error != "" || got.Hint != "" {
			t.Fatalf("unselected Link retained stale state: %+v", got)
		}
	}
	selected = true
	ss.applyLink(context.Background(), consumer, link, []string{"consumer", "target"})
	if got := ss.state.Apps["consumer"].Steps[link.ID]; got.Msg != linkWaitingMessage("target") || applied != 0 {
		t.Fatalf("selected unready target did not wait: %+v, calls %d", got, applied)
	}
	ss.state.Apps["target"].Steps = nil
	ss.state.Apps["target"].Setup = stepstatus.SetupOK
	ss.applyLink(context.Background(), consumer, link, []string{"consumer", "target"})
	if applied != 1 || predicates != 5 {
		t.Fatalf("applicability/apply counts: %d/%d", predicates, applied)
	}
	link.Applicable = nil
	ss.applyLink(context.Background(), consumer, link, []string{"consumer", "target"})
	if applied != 2 {
		t.Fatal("default Link behavior changed")
	}
}

func TestLinkApplicabilityErrorsAreConsumerStepsAndAbsentTargetsDoNotProbe(t *testing.T) {
	consumer := &testApp{id: "consumer", done: map[string]bool{}}
	target := &testApp{id: "target", done: map[string]bool{}}
	h := newHarness(t, consumer, target)
	if err := h.install("consumer", "target"); err != nil {
		t.Fatal(err)
	}
	ss, err := h.svc.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	link := app.Link{ID: "link-target", Target: "target", Title: "selected server",
		Applicable: func(context.Context, *helpers.H) (bool, error) {
			calls++
			return false, &helpers.ManualError{Hint: "Choose the intended server before retrying."}
		},
		Apply: func(context.Context, *helpers.H, *helpers.H) error { t.Fatal("unsafe apply"); return nil },
	}
	ss.applyLink(context.Background(), consumer, link, []string{"consumer"})
	if calls != 0 {
		t.Fatal("absent target invoked applicability")
	}
	ss.applyLink(context.Background(), consumer, link, []string{"consumer", "target"})
	got := ss.state.Apps["consumer"].Steps[link.ID]
	if got.Status != stepstatus.StepManual || got.Hint != "Choose the intended server before retrying." {
		t.Fatalf("missing consumer Manual recovery: %+v", got)
	}
	link.Applicable = func(context.Context, *helpers.H) (bool, error) { return false, errors.New("unavailable") }
	ss.applyLink(context.Background(), consumer, link, []string{"consumer", "target"})
	if got := ss.state.Apps["consumer"].Steps[link.ID]; got.Status != stepstatus.StepFail || got.Error != "unavailable" {
		t.Fatalf("missing consumer failure: %+v", got)
	}
}

func TestHistoricalLinkRemovalDoesNotConsultApplicability(t *testing.T) {
	removed := 0
	consumer := &applicableTestApp{testApp: &testApp{id: "consumer", done: map[string]bool{}}, links: []app.Link{{
		ID: "link-target", Target: "target", Title: "selected server",
		Applicable: func(context.Context, *helpers.H) (bool, error) {
			t.Fatal("removal consulted current selection")
			return false, nil
		},
		Remove: func(context.Context, *helpers.H, *helpers.H) error { removed++; return nil },
	}}}
	target := &testApp{id: "target", done: map[string]bool{}}
	h := newHarness(t, consumer, target)
	consumer.links[0].Applicable = nil
	consumer.links[0].Apply = func(context.Context, *helpers.H, *helpers.H) error { return nil }
	if err := h.install("consumer", "target"); err != nil {
		t.Fatal(err)
	}
	consumer.links[0].Applicable = func(context.Context, *helpers.H) (bool, error) {
		t.Fatal("removal consulted current selection")
		return false, nil
	}
	ss, err := h.svc.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if blocked := ss.unlink(context.Background(), "target"); len(blocked) != 0 || removed != 1 {
		t.Fatalf("historic removal skipped: %v, calls %d", blocked, removed)
	}
}

func TestTargetRecoveryOnlyRechecksOutstandingOptInApplicability(t *testing.T) {
	consumer := &applicableTestApp{testApp: &testApp{id: "consumer", done: map[string]bool{}}}
	trigger := &testApp{id: "trigger", done: map[string]bool{}}
	other := &testApp{id: "other", done: map[string]bool{}}
	h := newHarness(t, consumer, trigger, other)
	if err := h.install("consumer", "trigger", "other"); err != nil {
		t.Fatal(err)
	}
	ss, err := h.svc.open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	triggered, predicates, unexpectedApply, legacyRetries := 0, 0, 0, 0
	consumer.links = []app.Link{
		app.NewLink("link-trigger", "trigger", "trigger", app.LinkStep{
			Do: func(context.Context, *helpers.H, *helpers.H) error { triggered++; return nil },
		}, nil),
		{ID: "link-choice", Target: "other", Title: "choice", Applicable: func(context.Context, *helpers.H) (bool, error) { predicates++; return false, nil },
			Apply: func(context.Context, *helpers.H, *helpers.H) error { unexpectedApply++; return nil }},
		{ID: "link-wait", Target: "other", Title: "wait", Applicable: func(context.Context, *helpers.H) (bool, error) { predicates++; return false, nil },
			Apply: func(context.Context, *helpers.H, *helpers.H) error { unexpectedApply++; return nil }},
		{ID: "link-selected-manual", Target: "other", Title: "selected Manual", Applicable: func(context.Context, *helpers.H) (bool, error) { predicates++; return true, nil },
			Apply: func(context.Context, *helpers.H, *helpers.H) error { unexpectedApply++; return nil }},
		{ID: "link-healthy", Target: "other", Title: "healthy", Applicable: func(context.Context, *helpers.H) (bool, error) {
			t.Error("healthy applicability was unnecessarily reevaluated")
			return true, nil
		}},
		{ID: "link-legacy-manual", Target: "other", Title: "legacy Manual", Apply: func(context.Context, *helpers.H, *helpers.H) error { unexpectedApply++; return nil }},
		app.NewLink("link-legacy-failed", "other", "legacy failed", app.LinkStep{
			Do: func(context.Context, *helpers.H, *helpers.H) error { legacyRetries++; return nil },
		}, nil),
		{ID: "link-absent", Target: "absent", Title: "absent", Applicable: func(context.Context, *helpers.H) (bool, error) {
			t.Error("absent target was probed")
			return false, nil
		}},
	}
	st := ss.state.Apps["consumer"]
	for _, id := range []string{"link-choice", "link-selected-manual", "link-legacy-manual", "link-absent"} {
		st.Steps[id] = project.StepRecord{Status: stepstatus.StepManual, Hint: "keep this prerequisite unless unselected"}
	}
	st.Steps["link-wait"] = project.StepRecord{Status: stepstatus.StepSkip, Msg: linkWaitingMessage("other")}
	st.Steps["link-healthy"] = project.StepRecord{Status: stepstatus.StepOK}
	st.Steps["link-legacy-failed"] = project.StepRecord{Status: stepstatus.StepFail}
	if _, err := ss.runSetup(context.Background(), trigger); err != nil {
		t.Fatal(err)
	}
	if triggered != 1 || predicates != 3 || unexpectedApply != 0 || legacyRetries != 1 {
		t.Fatalf("unexpected recovery scope: trigger=%d predicates=%d other providers=%d legacy retries=%d", triggered, predicates, unexpectedApply, legacyRetries)
	}
	for _, id := range []string{"link-choice", "link-wait"} {
		if got := st.Steps[id]; got.Status != stepstatus.StepSkip || got.Hint != "" || got.Msg == linkWaitingMessage("other") {
			t.Fatalf("outstanding selection was not refreshed: %+v", got)
		}
	}
	for _, id := range []string{"link-selected-manual", "link-legacy-manual", "link-absent"} {
		if got := st.Steps[id]; got.Status != stepstatus.StepManual || got.Hint != "keep this prerequisite unless unselected" {
			t.Fatalf("unrelated prerequisite was changed: %+v", got)
		}
	}
}
