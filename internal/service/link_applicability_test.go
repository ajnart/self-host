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
