package fileflows_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/fileflows"
	"github.com/ajnart/self-host/internal/apps/porttest"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type fakeFileFlows struct {
	mu       sync.Mutex
	requests []string
}

func (f *fakeFileFlows) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.URL.Path != "/" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Write([]byte("FileFlows"))
}

func TestSetupAndCheckOnlyObserveTheWebConsole(t *testing.T) {
	fake := &fakeFileFlows{}
	server := httptest.NewServer(fake)
	defer server.Close()
	h := porttest.New(t, porttest.Options{App: "fileflows", Service: "fileflows"}, server, &porttest.Recorder{})

	if err := (fileflows.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := (fileflows.App{}).Check(context.Background(), h); err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, request := range fake.requests {
		if request != "GET /" {
			t.Fatalf("unexpected request %q; Setup must not reach /api, the licence or the EULA", request)
		}
	}
}

func TestRerunSkipsEveryStep(t *testing.T) {
	server := httptest.NewServer(&fakeFileFlows{})
	defer server.Close()
	rec := &porttest.Recorder{}
	h := porttest.New(t, porttest.Options{App: "fileflows", Service: "fileflows"}, server, rec)

	if err := (fileflows.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	second := &porttest.Recorder{}
	h = porttest.New(t, porttest.Options{App: "fileflows", Service: "fileflows"}, server, second)
	if err := (fileflows.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("rerun Setup: %v", err)
	}
	if status := second.Status("ready"); status != stepstatus.StepSkip {
		t.Fatalf("rerun ready = %q, want skip", status)
	}
}

func TestCheckFailsWhenTheConsoleIsDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	h := porttest.New(t, porttest.Options{App: "fileflows", Service: "fileflows"}, server, &porttest.Recorder{})

	if err := (fileflows.App{}).Check(context.Background(), h); err == nil {
		t.Fatal("Check should fail while the console is unavailable")
	}
}

func TestComposeKeepsOwnerControlledBoundaries(t *testing.T) {
	compose := (fileflows.App{}).Compose(app.RenderContext{Port: 19200})
	for _, want := range []string{"revenz/fileflows:stable", ":19200:5000", "./data:/app/Data", "./logs:/app/Logs", "./temp:/temp"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose missing %q", want)
		}
	}
	for _, banned := range []string{"FF_EULA_ACCEPTED", "docker.sock", "/media", "devices", "gpus", "privileged"} {
		if strings.Contains(compose, banned) {
			t.Errorf("compose must not contain %q", banned)
		}
	}
}
