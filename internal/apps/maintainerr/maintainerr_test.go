package maintainerr_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/maintainerr"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type maintainerrFixture struct {
	mu       sync.Mutex
	bodies   map[string]string
	statuses map[string]int
	requests []string
	results  []helpers.StepResult
	h        *helpers.H
}

func newMaintainerrFixture(t *testing.T, backend string) *maintainerrFixture {
	t.Helper()
	f := &maintainerrFixture{
		bodies: map[string]string{
			"/api/health/ready":           `{"status":"ok","database":"ok"}`,
			"/api/settings/test/setup":    `true`,
			"/api/media-server/type":      fmt.Sprintf(`{"type":%q}`, backend),
			"/api/media-server":           `{"machineId":"synthetic-server","version":"1.2.3"}`,
			"/api/media-server/users":     `[{"id":"synthetic-owner"}]`,
			"/api/media-server/libraries": `[]`,
			"/api/settings/test/plex":     `{"status":"OK","code":1,"message":"1.2.3"}`,
		},
		statuses: map[string]int{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("verification must use only credential-free read requests to Maintainerr")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, ok := f.bodies[r.URL.Path]
		if !ok {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status := f.statuses[r.URL.Path]; status != 0 {
			w.WriteHeader(status)
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	f.h = helpers.New(helpers.Config{
		App: "maintainerr", Service: "maintainerr", Dir: dir,
		BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(), Log: events.Discard(),
		Record: func(r helpers.StepResult) { f.results = append(f.results, r) },
	})
	return f
}

func TestMaintainerrDeploymentContract(t *testing.T) {
	a := maintainerr.App{}
	info := a.Info()
	if info.ID != "maintainerr" || info.Service != "maintainerr" || info.Category != "Media" || info.Port != 6246 || info.ContainerPort != 6246 || info.UsesMedia {
		t.Fatalf("unexpected App metadata: %+v", info)
	}
	if info.Image != "ghcr.io/maintainerr/maintainerr:latest" || len(info.Description) > 100 || len(a.Inputs()) != 0 || len(a.Links()) != 0 {
		t.Fatal("unexpected image, description, inputs or automatic peer Links")
	}
	definition := a.Compose(app.RenderContext{Port: 16246})
	for _, want := range []string{
		"image: " + info.Image, "restart: unless-stopped", "user: ${PUID}:${PGID}",
		"TZ: ${TZ}", "TELEMETRY: \"off\"", "source: ./data", "target: /opt/data",
		"create_host_path: false", "${BIND_IP}:16246:6246",
	} {
		if !strings.Contains(definition, want) {
			t.Errorf("Compose is missing %q", want)
		}
	}
	for _, unwanted := range []string{"/var/run/docker.sock", "privileged", "MEDIA_ROOT", "DOWNLOADS_ROOT", "depends_on", "API_KEY", "entrypoint:", "command:"} {
		if strings.Contains(definition, unwanted) {
			t.Errorf("Compose grants or replaces unexpected behavior: %s", unwanted)
		}
	}
	for _, want := range []string{"private", "data", "media server", "no login", "rules", "retention"} {
		if !strings.Contains(a.StandaloneGuide(), want) {
			t.Errorf("standalone guidance is missing %q", want)
		}
	}
}

func TestMaintainerrDeclaresOnlyItsPrivateDataWithSelectedRuntimeIdentity(t *testing.T) {
	for _, ids := range [][2]string{{"1000", "1001"}, {"0", "0"}, {"", ""}} {
		dirs := (maintainerr.App{}).Directories(app.RenderContext{Settings: map[string]string{"PUID": ids[0], "PGID": ids[1]}})
		if len(dirs) != 1 || dirs[0] != (app.Directory{Path: "data", Mode: 0o700, UID: ids[0], GID: ids[1]}) {
			t.Fatalf("directory does not follow the declared Compose identity: %+v", dirs)
		}
	}
	if _, declaresFiles := any(maintainerr.App{}).(app.FileDeclarer); declaresFiles {
		t.Fatal("directory preparation must not use a sentinel file declaration")
	}
	if _, preflights := any(maintainerr.App{}).(app.PreflightChecker); preflights {
		t.Fatal("Maintainerr storage admission must belong to the shared Service validator")
	}
}

func TestMaintainerrConfiguredServerAllowsEmptyLibrariesAndPreservesSettings(t *testing.T) {
	for _, backend := range []string{"plex", "jellyfin", "emby"} {
		t.Run(backend, func(t *testing.T) {
			f := newMaintainerrFixture(t, backend)
			path := filepath.Join(f.h.Dir, "data", "operator-setting")
			if err := os.WriteFile(path, []byte("preserved setting"), 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				f.results = nil
				if err := (maintainerr.App{}).Setup(t.Context(), f.h); err != nil {
					t.Fatal(err)
				}
				if len(f.results) != 2 || f.results[0].ID != "ready" || f.results[1].ID != "media-server" {
					t.Fatal("App Setup duplicated Service-owned directory admission")
				}
				for _, result := range f.results {
					if result.Status != stepstatus.StepSkip {
						t.Fatalf("read-only rerun should observe existing configuration: %+v", result)
					}
				}
				if err := (maintainerr.App{}).Check(t.Context(), f.h); err != nil {
					t.Fatal(err)
				}
			}
			contents, err := os.ReadFile(path)
			if err != nil || string(contents) != "preserved setting" {
				t.Fatal("Setup modified operator settings")
			}
		})
	}
}

func TestMaintainerrRejectsUnconfiguredOrUnverifiedServer(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		status           int
		manual           bool
	}{
		{"unconfigured", "/api/settings/test/setup", `false`, 200, true},
		{"unknown setup", "/api/settings/test/setup", `null`, 200, false},
		{"missing database", "/api/health/ready", `{"status":"ok"}`, 200, false},
		{"database unavailable", "/api/health/ready", `{"status":"degraded","database":"unreachable"}`, 503, false},
		{"unknown backend", "/api/media-server/type", `{"type":"other"}`, 200, true},
		{"empty identity", "/api/media-server", `{}`, 200, true},
		{"null identity", "/api/media-server", `null`, 200, true},
		{"unknown server version", "/api/media-server", `{"machineId":"server","version":""}`, 200, true},
		{"unauthenticated users", "/api/media-server/users", `[]`, 200, true},
		{"null users", "/api/media-server/users", `null`, 200, true},
		{"missing user id", "/api/media-server/users", `[{}]`, 200, true},
		{"protected API", "/api/media-server/users", `private-response-marker`, 401, true},
		{"protected health", "/api/health/ready", `private-response-marker`, 401, true},
		{"redirected health", "/api/health/ready", `private-response-marker`, 302, true},
		{"guard rejection", "/api/media-server/users", `private-response-marker`, 403, true},
		{"malformed response", "/api/media-server/users", `private-response-marker`, 200, false},
		{"unknown libraries", "/api/media-server/libraries", `null`, 200, false},
		{"missing library id", "/api/media-server/libraries", `[{"type":"movie"}]`, 200, false},
		{"backend unavailable", "/api/media-server/users", `private-response-marker`, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintainerrFixture(t, "jellyfin")
			f.mu.Lock()
			f.bodies[tc.path], f.statuses[tc.path] = tc.body, tc.status
			f.mu.Unlock()
			err := (maintainerr.App{}).Check(t.Context(), f.h)
			if err == nil || errors.Is(err, helpers.ErrManual) != tc.manual || strings.Contains(err.Error(), "private-response-marker") {
				t.Fatalf("unexpected or unsafe verification result: %v", err)
			}
			if tc.manual {
				err = (maintainerr.App{}).Setup(t.Context(), f.h)
				if !errors.Is(err, helpers.ErrManual) || errors.Is(err, helpers.ErrStepFailed) {
					t.Fatalf("owner prerequisites must remain Manual: %v", err)
				}
				last := f.results[len(f.results)-1]
				if last.Status != stepstatus.StepManual || !strings.Contains(last.Hint, "self-host setup maintainerr") {
					t.Fatal("Setup did not record actionable Manual recovery")
				}
			}
		})
	}
}

func TestMaintainerrPlexPublicIdentityIsNotAuthentication(t *testing.T) {
	for _, body := range []string{
		`{"status":"NOK","code":0,"message":"private-response-marker"}`,
		`{"status":"OK","message":"1.2.3"}`,
		`{"status":"OK","code":1,"message":""}`,
	} {
		f := newMaintainerrFixture(t, "plex")
		f.mu.Lock()
		f.bodies["/api/settings/test/plex"] = body
		f.mu.Unlock()
		err := (maintainerr.App{}).Check(t.Context(), f.h)
		if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private-response-marker") {
			t.Fatalf("public Plex identity did not prove library access: %v", err)
		}
	}
}

func TestMaintainerrCancellationDoesNotBecomeManual(t *testing.T) {
	f := newMaintainerrFixture(t, "plex")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := (maintainerr.App{}).Check(ctx, f.h); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}

func TestMaintainerrSelectionChangesRemainManual(t *testing.T) {
	for _, change := range []string{"backend", "identity"} {
		t.Run(change, func(t *testing.T) {
			f := newMaintainerrFixture(t, "jellyfin")
			reads := map[string]int{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads[r.URL.Path]++
				body := f.bodies[r.URL.Path]
				if reads[r.URL.Path] == 2 {
					if change == "backend" && r.URL.Path == "/api/media-server/type" {
						body = `{"type":"emby"}`
					}
					if change == "identity" && r.URL.Path == "/api/media-server" {
						body = `{"machineId":"another-server","version":"1.2.3"}`
					}
				}
				fmt.Fprint(w, body)
			}))
			t.Cleanup(srv.Close)
			f.h.BaseURL, f.h.Config.HTTP = srv.URL, srv.Client()
			err := (maintainerr.App{}).Check(t.Context(), f.h)
			if !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("selection changed without Manual recovery: %v", err)
			}
		})
	}
}
