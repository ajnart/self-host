package setupspec_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/setupspec"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func noAccountSpec() setupspec.Spec {
	return setupspec.Spec{Kind: setupspec.KindNoAccount, Probe: setupspec.Probe{Path: "/ready", Contains: "ok"}}
}

func TestNoAccountSetupThenCheckThenRerun(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	h, recorded := newH(t, srv)
	ctx := context.Background()
	spec := noAccountSpec()

	if err := spec.Setup(ctx, h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := spec.Check(ctx, h); err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, request := range seen {
		if request != "GET /ready" {
			t.Fatalf("unexpected request %q; a no-account Setup only observes the probe path", request)
		}
	}
	*recorded = nil
	if err := spec.Setup(ctx, h); err != nil {
		t.Fatalf("second Setup: %v", err)
	}
	for _, r := range *recorded {
		if r.Status != stepstatus.StepSkip {
			t.Errorf("step %s = %s on rerun, want skip", r.ID, r.Status)
		}
	}
}

func TestNoAccountCheckFailsWhenTheAppIsDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	h, _ := newH(t, srv)

	err := noAccountSpec().Check(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("Check = %v, want the probe's status refusal", err)
	}
}
