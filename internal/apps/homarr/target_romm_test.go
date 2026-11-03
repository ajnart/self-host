package homarr_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

var rommMetrics = []string{"platforms", "roms", "saves", "states", "screenshots", "totalFileSize"}

type rommLinkFixture struct {
	mu        sync.Mutex
	refreshes int
	catalogs  int
	probes    int
	timestamp int
	invalidAt int
	metric    string
	value     any
	unit      string
	native    string
	status    int
}

func (f *rommLinkFixture) link(t *testing.T) (app.Link, *helpers.H, *helpers.H) {
	t.Helper()
	fake := newFake()
	fake.apiKey = "keyid.secret"
	fallback := fake.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/api/stats" {
			f.probes++
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			w.Write([]byte(f.native))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/trpc/") && !fake.keyOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/trpc/integration.getKinds":
			trpc(w, []any{map[string]any{"kind": "romm", "requiredSecrets": [][]string{{}}}})
		case "/api/trpc/widget.stats.catalog":
			f.catalogs++
			metrics := []any{}
			for _, key := range rommMetrics {
				unit := "count"
				if key == "totalFileSize" {
					unit = "bytes"
				}
				if key == f.metric && f.unit != "" {
					unit = f.unit
				}
				metrics = append(metrics, map[string]any{"key": key, "unit": unit})
			}
			trpc(w, map[string]any{"metrics": metrics})
		case "/api/trpc/widget.stats.snapshot":
			trpc(w, map[string]any{"updatedAt": f.timestamp})
		case "/api/trpc/widget.stats.refresh":
			f.refreshes++
			if r.Method != "POST" || inputOf(r)["force"] != true {
				t.Error("RomM verification must force a Stats refresh")
			}
			f.timestamp++
			values := map[string]any{}
			for _, key := range rommMetrics {
				values[key] = 0
			}
			if f.refreshes == f.invalidAt {
				values[f.metric] = f.value
			}
			trpc(w, map[string]any{"updatedAt": f.timestamp, "error": false, "stale": false, "values": values})
		default:
			fallback.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return linkFor(t, "romm"), homarrH(t, srv, fakeSecrets{"HOMARR_API_KEY": fake.apiKey}), targetH(t, srv, "romm", 8080, fakeSecrets{})
}

func (f *rommLinkFixture) requireInvocations(t *testing.T, want int) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refreshes != want || f.catalogs != want || f.probes != want {
		t.Fatalf("RomM verification: native=%d catalogs=%d refreshes=%d, want %d each", f.probes, f.catalogs, f.refreshes, want)
	}
}

const rommEmpty = `{"PLATFORMS":0,"ROMS":0,"SAVES":0,"STATES":0,"SCREENSHOTS":0,"TOTAL_FILESIZE_BYTES":0}`

func TestRommLinkRejectsInvalidFirstSampleWithoutRefreshingAgain(t *testing.T) {
	for _, metric := range rommMetrics {
		for _, value := range []any{-1, 0.5, "0"} {
			t.Run(metric+"/"+fmt.Sprint(value), func(t *testing.T) {
				fixture := &rommLinkFixture{timestamp: 1000, invalidAt: 1, metric: metric, value: value, native: rommEmpty}
				link, from, to := fixture.link(t)
				if err := link.Apply(t.Context(), from, to); err == nil || !strings.Contains(err.Error(), "incomplete RomM Stats") {
					t.Fatalf("first semantic failure must fail the public Link: %v", err)
				}
				fixture.requireInvocations(t, 1)
			})
		}
	}
}

func TestRommLinkRejectsWrongMetricUnits(t *testing.T) {
	for _, metric := range rommMetrics {
		t.Run(metric, func(t *testing.T) {
			unit := "bytes"
			if metric == "totalFileSize" {
				unit = "count"
			}
			fixture := &rommLinkFixture{timestamp: 1000, metric: metric, unit: unit, native: rommEmpty}
			link, from, to := fixture.link(t)
			if err := link.Apply(t.Context(), from, to); err == nil {
				t.Fatal("RomM metric with a wrong catalog unit completed the public Link")
			}
			fixture.requireInvocations(t, 1)
		})
	}
}

func TestRommLinkAcceptsZeroWithOneRefreshPerVerification(t *testing.T) {
	fixture := &rommLinkFixture{timestamp: 1000, native: rommEmpty}
	link, from, to := fixture.link(t)
	if err := link.Apply(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	fixture.requireInvocations(t, 2)
	if err := link.Apply(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	fixture.requireInvocations(t, 3)
}

func TestRommLinkValidatesPostCheckSample(t *testing.T) {
	fixture := &rommLinkFixture{timestamp: 1000, invalidAt: 2, metric: "roms", value: -1, native: rommEmpty}
	link, from, to := fixture.link(t)
	if err := link.Apply(t.Context(), from, to); err == nil || !strings.Contains(err.Error(), "incomplete RomM Stats") {
		t.Fatalf("invalid post-check sample must fail the public Link: %v", err)
	}
	fixture.requireInvocations(t, 2)
}

func TestRommLinkKeepsProtectedOrIncompleteNativeStatsManual(t *testing.T) {
	for name, fixture := range map[string]*rommLinkFixture{
		"protected":  {timestamp: 1000, status: http.StatusUnauthorized},
		"incomplete": {timestamp: 1000, native: `{"PLATFORMS":0}`},
		"malformed":  {timestamp: 1000, native: `[]`},
	} {
		t.Run(name, func(t *testing.T) {
			link, from, to := fixture.link(t)
			if err := link.Apply(t.Context(), from, to); err == nil {
				t.Fatal("unreadable native RomM Stats completed the public Link")
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.probes != 1 || fixture.catalogs != 0 || fixture.refreshes != 0 {
				t.Fatalf("native failure acquired Stats: native=%d catalogs=%d refreshes=%d", fixture.probes, fixture.catalogs, fixture.refreshes)
			}
		})
	}
}
