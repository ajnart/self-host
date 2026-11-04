package catalog_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
	"go.yaml.in/yaml/v3"
)

func specApp(t *testing.T, id string) app.App {
	t.Helper()
	a, ok := catalog.Default().App(id)
	if !ok {
		t.Fatalf("catalog has no App %q", id)
	}
	return a
}

type readyTransport struct{}

func (readyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func TestLokiInstallUsesConfiguredStorageUser(t *testing.T) {
	for _, tc := range []struct {
		name, options, want string
		explicit            bool
	}{
		{name: "rootless", options: `["name=rootless"]`, want: "0:0"},
		{name: "rootful", options: `["name=seccomp"]`, want: "1000:1000"},
		{name: "explicit", options: `["name=rootless"]`, want: "1234:5678", explicit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			deps.Runner.(*systemtest.Runner).On("docker info", system.Result{Stdout: []byte(tc.options)})
			deps.HTTP = &http.Client{Transport: readyTransport{}}
			cat := catalog.New([]app.App{specApp(t, "loki")}, nil)
			args := []string{"install", "loki", "--yes"}
			if r := clitest.Run(t, deps, cat, args...); r.Code != 0 {
				t.Fatal(r.Out)
			}
			if tc.explicit {
				if r := clitest.Run(t, deps, cat, "config", "set", "PUID=1234", "PGID=5678"); r.Code != 0 {
					t.Fatal(r.Out)
				}
				if r := clitest.Run(t, deps, cat, args...); r.Code != 0 {
					t.Fatal(r.Out)
				}
			}
			root := clitest.Root(t, deps)
			var doc struct {
				Services map[string]struct {
					User string `yaml:"user"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal([]byte(clitest.Read(t, filepath.Join(root, "apps", "loki", "compose.yml"))), &doc); err != nil {
				t.Fatal(err)
			}
			settings, err := (&project.Project{Root: root}).LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			if got := os.Expand(doc.Services["loki"].User, func(key string) string { return settings[key] }); got != tc.want {
				t.Fatalf("storage user = %q, want configured %s", got, tc.want)
			}
		})
	}
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// Stirling PDF v3.0.0 serves root as text/html, while its documented container
// healthcheck reads the JSON status map from /api/v1/info/status. Exercise both
// public readiness operations without relaxing the shared JSON Accept header.
func TestStirlingPDFReadinessUsesJSONStatusWhenFrontendRefusesJSON(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: transport(func(request *http.Request) (*http.Response, error) {
		requests++
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want shared JSON negotiation", got)
		}
		status, body := http.StatusNotFound, ""
		switch request.URL.Path {
		case "/":
			status = http.StatusNotAcceptable
		case "/api/v1/info/status":
			status, body = http.StatusOK, `{"status":"UP","version":"3.0.0"}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	h := helpers.New(helpers.Config{App: "stirling-pdf", BaseURL: "http://stirling-fixture.invalid", URL: "http://stirling-fixture.invalid", HTTP: client, Log: events.Discard()})
	application := specApp(t, "stirling-pdf")
	if err := application.Check(context.Background(), h); err != nil {
		t.Fatalf("Check must accept the pinned JSON health endpoint: %v", err)
	}
	if err := application.Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup must accept the pinned JSON health endpoint: %v", err)
	}
	if requests != 2 {
		t.Errorf("healthy Check and Setup made %d requests, want one each", requests)
	}
}

func TestFileFlowsSetupAndCheckOnlyObserveTheWebConsole(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte("FileFlows"))
	}))
	defer server.Close()
	h := porttest.New(t, porttest.Options{App: "fileflows", Service: "fileflows"}, server, &porttest.Recorder{})
	fileflows := specApp(t, "fileflows")

	if err := fileflows.Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if err := fileflows.Check(context.Background(), h); err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, request := range requests {
		if request != "GET /" {
			t.Fatalf("unexpected request %q; Setup must not reach /api, the licence or the EULA", request)
		}
	}
}

func TestFileFlowsComposeKeepsOwnerControlledBoundaries(t *testing.T) {
	compose := specApp(t, "fileflows").Compose(app.RenderContext{Port: 19200})
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
