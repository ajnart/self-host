package unmanic_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/unmanic"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/systemtest"
)

type fakeSecrets map[string]string

func (s fakeSecrets) Get(key string) string       { return s[key] }
func (s fakeSecrets) Set(key, value string) error { s[key] = value; return nil }

type fixture struct {
	mu       sync.Mutex
	uuid     string
	version  string
	status   int
	workers  string
	pending  string
	requests []string
	body     map[string]any
}

func newFixture() *fixture {
	return &fixture{uuid: "uuid-1", version: "0.4.1", status: http.StatusOK, workers: `[{"id":"0","idle":true},{"id":"1","idle":false}]`, pending: `{"recordsTotal":4}`}
}

func (f *fixture) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			return
		}
		switch r.URL.Path {
		case "/unmanic/api/v2/version/read":
			w.Write([]byte(`{"version":"` + f.version + `"}`))
		case "/unmanic/api/v2/session/state":
			w.Write([]byte(`{"uuid":"` + f.uuid + `","email":"private@example.test"}`))
		case "/unmanic/api/v2/workers/status":
			w.Write([]byte(`{"workers_status":` + f.workers + `}`))
		case "/unmanic/api/v2/pending/tasks":
			json.NewDecoder(r.Body).Decode(&f.body)
			w.Write([]byte(f.pending))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newH(t *testing.T, srv *httptest.Server, secrets fakeSecrets) *helpers.H {
	deps := systemtest.NewDeps(t)
	return helpers.New(helpers.Config{
		App: "unmanic", Service: "unmanic", BaseURL: srv.URL, URL: srv.URL, HTTP: srv.Client(),
		Secrets: secrets, Log: events.Discard(), Record: func(helpers.StepResult) {}, Now: deps.Now, Sleep: deps.Sleep,
	})
}

func TestReadsIdentityAndCountsWithoutMutation(t *testing.T) {
	f := newFixture()
	h := newH(t, f.serve(t), fakeSecrets{})
	stats, err := unmanic.ReadHomarrStats(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Version != "0.4.1" || stats.InstallationUUID != "uuid-1" || stats.ActiveWorkers != 1 || stats.TotalWorkers != 2 || stats.PendingTasks != 4 {
		t.Fatalf("stats = %+v", stats)
	}
	if f.body["length"] != float64(0) || f.body["start"] != float64(0) || f.body["status"] != "all" {
		t.Fatalf("pending body = %v", f.body)
	}
	if h.Secret(h.Key("installation_uuid")) != "" {
		t.Fatal("reading must not persist the identity")
	}
}

func TestZeroWorkersAndZeroPendingAreValid(t *testing.T) {
	f := newFixture()
	f.workers, f.pending = `[]`, `{"recordsTotal":0}`
	stats, err := unmanic.ReadHomarrStats(context.Background(), newH(t, f.serve(t), fakeSecrets{}))
	if err != nil || stats.TotalWorkers != 0 || stats.PendingTasks != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestSetupPersistsIdentityAndRerunSkips(t *testing.T) {
	f := newFixture()
	secrets := fakeSecrets{}
	h := newH(t, f.serve(t), secrets)
	if err := (unmanic.App{}).Setup(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if secrets["UNMANIC_INSTALLATION_UUID"] != "uuid-1" {
		t.Fatalf("secrets = %v", secrets)
	}
	if err := (unmanic.App{}).Check(context.Background(), h); err != nil {
		t.Fatal(err)
	}
}

func TestChangedIdentityIsManualAndNeverOverwritten(t *testing.T) {
	f := newFixture()
	f.uuid = "uuid-2"
	secrets := fakeSecrets{"UNMANIC_INSTALLATION_UUID": "uuid-1"}
	h := newH(t, f.serve(t), secrets)
	err := (unmanic.App{}).Setup(context.Background(), h)
	if !errors.Is(err, helpers.ErrManual) || secrets["UNMANIC_INSTALLATION_UUID"] != "uuid-1" {
		t.Fatalf("err=%v secrets=%v", err, secrets)
	}
}

func TestAuthenticationStatusesAreManualWithoutBodies(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusFound} {
		f := newFixture()
		f.status = status
		_, err := unmanic.ReadHomarrStats(context.Background(), newH(t, f.serve(t), fakeSecrets{}))
		if !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
}

func TestMalformedAnswersAreRejected(t *testing.T) {
	for name, change := range map[string]func(*fixture){
		"no version":         func(f *fixture) { f.version = "" },
		"no uuid":            func(f *fixture) { f.uuid = "" },
		"missing idle":       func(f *fixture) { f.workers = `[{"id":"0"}]` },
		"no total":           func(f *fixture) { f.pending = `{}` },
		"negative total":     func(f *fixture) { f.pending = `{"recordsTotal":-1}` },
		"worker list absent": func(f *fixture) { f.workers = `null` },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			change(f)
			if _, err := unmanic.ReadHomarrStats(context.Background(), newH(t, f.serve(t), fakeSecrets{})); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestOnlyReadOnlyEndpointsAreCalled(t *testing.T) {
	f := newFixture()
	if _, err := unmanic.ReadHomarrStats(context.Background(), newH(t, f.serve(t), fakeSecrets{})); err != nil {
		t.Fatal(err)
	}
	want := "GET /unmanic/api/v2/version/read,GET /unmanic/api/v2/session/state,GET /unmanic/api/v2/workers/status,POST /unmanic/api/v2/pending/tasks"
	if got := strings.Join(f.requests, ","); got != want {
		t.Fatalf("requests = %s", got)
	}
}

func TestComposeBindsNoHostMediaOrPrivilege(t *testing.T) {
	compose := (unmanic.App{}).Compose(app.RenderContext{Port: 8888})
	for _, forbidden := range []string{"/var/run/docker.sock", "devices:", "privileged", "network_mode", "MEDIA_ROOT", "DOWNLOADS_ROOT"} {
		if strings.Contains(compose, forbidden) {
			t.Fatalf("compose contains %s", forbidden)
		}
	}
	for _, required := range []string{"${BIND_IP}:8888:8888", "./config:/config", "./cache:/tmp/unmanic", "./library:/library:ro"} {
		if !strings.Contains(compose, required) {
			t.Fatalf("compose lacks %s", required)
		}
	}
	if (unmanic.App{}).Info().UsesMedia {
		t.Fatal("UsesMedia must stay false")
	}
}

func TestSettingsAreDeclaredFreshOnly(t *testing.T) {
	files := (unmanic.App{}).Files(app.RenderContext{}, nil)
	if len(files) != 1 || !files[0].Keep || files[0].Path != "config/.unmanic/config/settings.json" || !strings.Contains(files[0].Content, `"clear_pending_tasks_on_restart":false`) {
		t.Fatalf("files = %+v", files)
	}
}
