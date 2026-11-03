package uptimekuma_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/apps/uptimekuma"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

func TestTheAdminLoginIsCreatedOverSocketIO(t *testing.T) {
	fake := porttest.NewSocketIO("", "")
	server := httptest.NewServer(fake.Handler())
	defer server.Close()
	rec := &porttest.Recorder{}
	h := porttest.New(t, porttest.Options{App: "uptime-kuma", Service: "uptime-kuma",
		Secrets: porttest.Secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "secret"}}, server, rec)

	if err := (uptimekuma.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if fake.User != "admin" || fake.Password != "secret" || rec.Status("admin") != stepstatus.StepOK {
		t.Fatalf("admin not created: user=%q step=%q", fake.User, rec.Status("admin"))
	}
	if err := (uptimekuma.App{}).Check(context.Background(), h); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestRerunSkipsEveryStep(t *testing.T) {
	server := httptest.NewServer(porttest.NewSocketIO("admin", "secret").Handler())
	defer server.Close()
	rec := &porttest.Recorder{}
	h := porttest.New(t, porttest.Options{App: "uptime-kuma", Service: "uptime-kuma",
		Secrets: porttest.Secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "secret"}}, server, rec)

	if err := (uptimekuma.App{}).Setup(context.Background(), h); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if rec.Status("ready") != stepstatus.StepSkip || rec.Status("admin") != stepstatus.StepSkip {
		t.Fatalf("rerun should skip: ready=%q admin=%q", rec.Status("ready"), rec.Status("admin"))
	}
}

func TestTheHintNamesTheAppPage(t *testing.T) {
	server := httptest.NewServer(porttest.NewSocketIO("admin", "changed").Handler())
	defer server.Close()
	rec := &porttest.Recorder{}
	h := porttest.New(t, porttest.Options{App: "uptime-kuma", Service: "uptime-kuma",
		Secrets: porttest.Secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "secret"}}, server, rec)

	if err := (uptimekuma.App{}).Setup(context.Background(), h); err == nil {
		t.Fatal("Setup should fail when the administrator is not the saved login")
	}
	if rec.Status("admin") != stepstatus.StepFail || !strings.Contains(rec.Hint(), server.URL) {
		t.Fatalf("admin step should fail with the App URL hint, got %q", rec.Hint())
	}
}

func TestHomarrPagePreservesSourceGroupsAndModernUnknowns(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		body   string
		modern bool
		legacy bool
	}{
		{"absent monitor heartbeat", `{"heartbeatList":{},"uptimeList":{}}`, true, false},
		{"explicit empty heartbeat", `{"heartbeatList":{"1":[]},"uptimeList":{}}`, true, true},
		{"expanded integer", `{"heartbeatList":{"1":[{"status":17,"time":"2026-11-02 11:59:00"}]},"uptimeList":{}}`, true, false},
		{"negative unknown integer", `{"heartbeatList":{"1":[{"status":-1,"time":"2026-11-02 11:59:00"}]},"uptimeList":{}}`, true, false},
		{"pending", `{"heartbeatList":{"1":[{"status":2,"time":"2026-11-02 11:59:00"}]},"uptimeList":{}}`, true, true},
		{"maintenance", `{"heartbeatList":{"1":[{"status":3,"time":"2026-11-02 11:59:00"}]},"uptimeList":{}}`, true, true},
		{"unparseable timestamp becomes unknown", `{"heartbeatList":{"1":[{"status":1,"time":""}]},"uptimeList":{}}`, true, false},
		{"null heartbeat array", `{"heartbeatList":{"1":null},"uptimeList":{}}`, false, false},
		{"missing status", `{"heartbeatList":{"1":[{"time":"2026-11-02 11:59:00"}]},"uptimeList":{}}`, false, false},
		{"fractional status", `{"heartbeatList":{"1":[{"status":1.5,"time":"2026-11-02 11:59:00"}]},"uptimeList":{}}`, false, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("page reader mutated the peer")
				}
				switch r.URL.Path {
				case "/api/status-page/chosen-page":
					w.Write([]byte(`{"config":{"slug":"chosen-page"},"publicGroupList":[{"id":7,"name":"Core","monitorList":[{"id":1,"name":"First"}]},{"id":8,"name":"Empty","monitorList":[]}]}`))
				case "/api/status-page/heartbeat/chosen-page":
					w.Write([]byte(scenario.body))
				default:
					t.Errorf("unexpected credential or page probe %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			h := helpers.New(helpers.Config{App: "uptime-kuma", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(), Inputs: map[string]string{"homarr_status_page": "chosen-page"}})
			page, err := uptimekuma.HomarrStatusPage(t.Context(), h)
			if (err == nil) != scenario.modern {
				t.Fatalf("modern validity=%v err=%v", scenario.modern, err)
			}
			if err == nil && (page.Slug != "chosen-page" || len(page.Groups) != 2 || page.Groups[0].ID != 7 || page.Groups[0].Name != "Core" || len(page.Groups[0].Monitors) != 1 || page.Groups[0].Monitors[0].ID != 1 || page.Groups[1].ID != 8 || page.Groups[1].Name != "Empty" || page.Groups[1].Monitors == nil || len(page.Groups[1].Monitors) != 0) {
				t.Fatalf("source group identities/membership were lost: %#v", page)
			}
			monitors, err := uptimekuma.HomarrMonitors(t.Context(), h)
			if (err == nil) != scenario.legacy || err == nil && (len(monitors) != 1 || monitors[0].ID != 1 || monitors[0].Name != "First") {
				t.Fatalf("legacy strictness changed: monitors=%v err=%v", monitors, err)
			}
		})
	}
}

func TestSetupAcceptsTheModernUnknownPagePrerequisite(t *testing.T) {
	fake := porttest.NewSocketIO("admin", "secret")
	next := fake.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status-page/chosen-page":
			w.Write([]byte(`{"config":{"slug":"chosen-page"},"publicGroupList":[{"id":7,"name":"Core","monitorList":[{"id":1,"name":"Never checked"}]}]}`))
		case "/api/status-page/heartbeat/chosen-page":
			w.Write([]byte(`{"heartbeatList":{},"uptimeList":{}}`))
		default:
			next.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	h := porttest.New(t, porttest.Options{App: "uptime-kuma", Service: "uptime-kuma", Secrets: porttest.Secrets{"ADMIN_USER": "admin", "ADMIN_PASSWORD": "secret"}}, server, &porttest.Recorder{})
	h.Inputs["homarr_status_page"] = "chosen-page"
	if err := (uptimekuma.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if err := (uptimekuma.App{}).Check(t.Context(), h); err != nil {
		t.Fatal(err)
	}
}
