package service

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestDesktopAppObservationUsesSharedStatus(t *testing.T) {
	running := AppStatus{ID: "one", Name: "One", Managed: true, State: "running",
		Setup: stepstatus.SetupPending, URL: "http://127.0.0.1:9000/"}
	unknown := AppStatus{ID: "two", Name: "Two", Managed: true, State: "unknown", URL: "https://user@app.test/"}
	unmanaged := AppStatus{ID: "three", State: "running", URL: "http://app.test/"}
	status := Status{Home: "/home/me/.self-host", Apps: []AppStatus{running, unknown, unmanaged}}
	got := status.ForDesktop()
	if got.Home != status.Home || len(got.Apps) != 3 {
		t.Fatalf("desktop observation = %+v", got)
	}
	first := got.Apps[0]
	if first.Level != "attention" || first.Address != running.URL || !slices.Equal(first.Actions, []string{"stop", "restart"}) {
		t.Fatalf("running App = %+v", first)
	}
	second := got.Apps[1]
	if second.State != "unknown" || second.Address != "" || !slices.Equal(second.Actions, []string{"start", "stop", "restart"}) {
		t.Fatalf("unknown App = %+v", second)
	}
	if len(got.Apps[2].Actions) != 0 {
		t.Fatalf("unmanaged App = %+v", got.Apps[2])
	}
	status.LANIPStale = true
	if address := status.ForDesktop().Apps[0].Address; address != "" {
		t.Fatalf("launchable Address despite stale LAN: %q", address)
	}
}

func TestDesktopActionChecksLiveStateBeforeChangingApp(t *testing.T) {
	for _, tc := range []struct {
		name, id, action, observed, live, after, want string
		psFails, verifyFails                          bool
	}{
		{name: "known restart", id: "one", action: "restart", observed: "running", live: "running", after: "running", want: "restart --no-deps -- one"},
		{name: "Status becomes available", id: "one", action: "stop", observed: "unknown", live: "running", after: "exited", want: "stop -- one"},
		{name: "start stopped", id: "one", action: "start", observed: "unknown", live: "exited", after: "running", want: "up -d --no-deps --remove-orphans=false -- one"},
		{name: "still stopped after Start", id: "one", action: "start", observed: "unknown", live: "exited", after: "exited", want: "up -d --no-deps --remove-orphans=false -- one", verifyFails: true},
		{name: "stale review", id: "one", action: "restart", observed: "running", live: "exited"},
		{name: "wrong action after authorization", id: "one", action: "start", observed: "unknown", live: "running"},
		{name: "Docker still unreachable", id: "one", action: "restart", observed: "unknown", psFails: true},
		{name: "removed App", id: "absent", action: "restart", observed: "unknown", live: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			before := len(h.composeCalls())
			p, err := h.svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			statePath := p.Path("state.json")
			beforeState, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				beforeState = append(beforeState, []byte("damaged tail")...)
				if err := os.WriteFile(statePath, beforeState, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			state := tc.live
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				line := systemtest.Line(cmd)
				if !strings.HasSuffix(line, "ps --all --format json") {
					if tc.want != "" && strings.HasSuffix(line, tc.want) {
						state = tc.after
					}
					return system.Result{}
				}
				lockCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
				defer cancel()
				if unlock, err := p.LockContext(lockCtx, h.deps.PID, h.deps.ProcessAlive); err == nil {
					unlock()
					t.Fatal("live Status was inspected outside the Project lock")
				}
				if tc.psFails {
					return system.Result{ExitCode: 1, Stderr: []byte("Docker unavailable")}
				}
				return system.Result{Stdout: []byte(`{"Service":"one","State":"` + state + `"}`)}
			})
			err = h.svc.ApplyDesktopAction(context.Background(), DesktopAction{Verb: tc.action, AppID: tc.id, ObservedState: tc.observed})
			calls := h.composeCalls()[before:]
			if tc.want == "" {
				if err == nil || len(calls) > 0 {
					t.Fatalf("rejected Action: error = %v, Compose = %v", err, calls)
				}
				afterState, err := os.ReadFile(statePath)
				if err != nil || !slices.Equal(beforeState, afterState) {
					t.Fatalf("rejected Action changed state: %v", err)
				}
			} else if (err != nil) != tc.verifyFails || !slices.Equal(calls, []string{tc.want}) {
				t.Fatalf("approved Action: error = %v, Compose = %v", err, calls)
			}
		})
	}
}
