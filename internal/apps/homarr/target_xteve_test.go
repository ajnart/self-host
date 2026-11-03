package homarr_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

type xteveLinkFixture struct {
	*fakeHomarr
	values    map[string]any
	metrics   []any
	timestamp float64
	advance   bool
	failed    bool
	stale     bool
	refreshes int
	catalogs  int
	snapshots int
	native    int
	password  string
}

func newXteveLinkFixture() *xteveLinkFixture {
	fake := newFake()
	fake.apiKey = "keyid.secret"
	f := &xteveLinkFixture{fakeHomarr: fake, advance: true, timestamp: 1000, password: "xteve-pw", values: map[string]any{"streamsAll": 1, "streamsActive": 1, "streamsXepg": 0}}
	for _, key := range []string{"streamsAll", "streamsActive", "streamsXepg"} {
		f.metrics = append(f.metrics, map[string]any{"key": key, "unit": "count"})
	}
	return f
}

func (f *xteveLinkFixture) serveNative(w http.ResponseWriter, r *http.Request) {
	f.native++
	if r.URL.Path == "/discover.json" {
		json.NewEncoder(w).Encode(map[string]any{"DeviceAuth": "xteve", "DeviceID": "DEVICE1"})
		return
	}
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)
	if body["cmd"] == "login" {
		if body["username"] != "xteve-api" || body["password"] != f.password {
			json.NewEncoder(w).Encode(map[string]any{"status": false, "err": "login incorrect"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": true, "token": "token"})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"status": true, "token": "next",
		"version": map[string]any{"api": "1.1.0", "xteve": "2.2.0.0200"},
		"epg":     map[string]any{"source": "XEPG"},
		"streams": map[string]any{"all": 1, "active": 1},
	})
}

func (f *xteveLinkFixture) serve(t *testing.T) (*helpers.H, *helpers.H, fakeSecrets) {
	t.Helper()
	fallback := f.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/", "/discover.json", "/api/trpc/integration.getKinds", "/api/trpc/widget.stats.catalog", "/api/trpc/widget.stats.snapshot", "/api/trpc/widget.stats.refresh":
		default:
			fallback.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/api/" || r.URL.Path == "/discover.json" {
			f.serveNative(w, r)
			return
		}
		if !f.keyOK(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/trpc/integration.getKinds":
			trpc(w, []any{map[string]any{"kind": "xteve", "requiredSecrets": [][]string{{}, {"username", "password"}}}})
		case "/api/trpc/widget.stats.catalog":
			f.catalogs++
			trpc(w, map[string]any{"metrics": f.metrics})
		case "/api/trpc/widget.stats.snapshot":
			f.snapshots++
			trpc(w, map[string]any{"updatedAt": f.timestamp})
		case "/api/trpc/widget.stats.refresh":
			f.refreshes++
			if inputOf(r)["force"] != true {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if f.advance {
				f.timestamp++
			}
			trpc(w, map[string]any{"updatedAt": f.timestamp, "values": f.values, "error": f.failed, "stale": f.stale})
		}
	}))
	t.Cleanup(srv.Close)
	secrets := allSecrets()
	secrets["XTEVE_USERNAME"], secrets["XTEVE_PASSWORD"] = "xteve-api", "xteve-pw"
	return homarrH(t, srv, secrets), targetH(t, srv, "xteve", 34400, secrets), secrets
}

func TestXteveLinkUsesOneStatsSamplePerVerificationAndReleasesOnlyOwnedBindings(t *testing.T) {
	f := newXteveLinkFixture()
	from, to, secrets := f.serve(t)
	link := linkFor(t, "xteve")
	if err := link.Apply(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.refreshes != 2 || f.catalogs != 2 || f.snapshots != 2 {
		t.Fatalf("initial apply and post-check: refreshes=%d catalogs=%d snapshots=%d", f.refreshes, f.catalogs, f.snapshots)
	}
	if len(f.integrations) != 1 || f.integrations[0]["kind"] != "xteve" || f.integrations[0]["url"] != "http://xteve:34400" {
		t.Fatalf("integration: %v", f.integrations)
	}
	pair := f.integrations[0]["secrets"].([]any)
	if len(pair) != 2 || pair[0].(map[string]any)["kind"] != "username" || pair[1].(map[string]any)["kind"] != "password" {
		t.Fatal("xTeVe must advertise its own username and password, never a token")
	}
	id := secrets["HOMARR_LINKED_XTEVE"]
	if id == "" || widgetCount(f.items, "stats", id) != 1 {
		t.Fatal("owned xTeVe Stats binding missing")
	}
	for _, item := range f.items {
		if item["kind"] == "stats" && len(item["options"].(map[string]any)["entries"].([]any)) != 3 {
			t.Fatal("xTeVe must expose exactly all, active and XEPG streams")
		}
	}
	f.values = map[string]any{"streamsAll": 0, "streamsActive": 0, "streamsXepg": 0}
	f.mu.Unlock()
	var status stepstatus.Step
	from.Record = func(result helpers.StepResult) { status = result.Status }
	if err := link.Apply(t.Context(), from, to); err != nil || status != stepstatus.StepSkip {
		t.Fatalf("zero-count rerun: err=%v status=%s", err, status)
	}
	f.mu.Lock()
	if f.refreshes != 3 || f.catalogs != 3 || f.snapshots != 3 || len(f.integrations) != 1 || secrets["HOMARR_LINKED_XTEVE"] != id {
		t.Fatal("rerun did not reuse ownership and one semantic sample")
	}
	native := f.native
	f.mu.Unlock()
	if err := link.Remove(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.native != native || f.refreshes != 3 || len(f.integrations) != 0 || widgetCount(f.items, "stats", id) != 0 {
		t.Fatal("removal read the peer or Stats, or left the owned binding")
	}
}

func TestXteveLinkRejectsInvalidStats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*xteveLinkFixture)
	}{
		{"negative", func(f *xteveLinkFixture) { f.values["streamsAll"] = -1 }},
		{"fraction", func(f *xteveLinkFixture) { f.values["streamsXepg"] = 0.5 }},
		{"text", func(f *xteveLinkFixture) { f.values["streamsAll"] = "1" }},
		{"null", func(f *xteveLinkFixture) { f.values["streamsActive"] = nil }},
		{"missing", func(f *xteveLinkFixture) { delete(f.values, "streamsXepg") }},
		{"active above all", func(f *xteveLinkFixture) { f.values["streamsActive"] = 2 }},
		{"unit", func(f *xteveLinkFixture) { f.metrics[0].(map[string]any)["unit"] = "bytes" }},
		{"no-op", func(f *xteveLinkFixture) { f.advance = false }},
		{"failed", func(f *xteveLinkFixture) { f.failed = true }},
		{"stale", func(f *xteveLinkFixture) { f.stale = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newXteveLinkFixture()
			tc.change(f)
			from, to, _ := f.serve(t)
			if err := linkFor(t, "xteve").Apply(t.Context(), from, to); err == nil {
				t.Fatal("invalid first Stats sample completed public Link")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.refreshes != 1 || f.catalogs != 1 || f.snapshots != 1 {
				t.Fatalf("invalid first sample must stop without a second refresh: %d/%d/%d", f.refreshes, f.catalogs, f.snapshots)
			}
		})
	}
}

func TestXteveLinkPreflightStopsBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*xteveLinkFixture, fakeSecrets)
	}{
		{"rejected credentials", func(f *xteveLinkFixture, _ fakeSecrets) { f.password = "changed" }},
		{"missing credentials", func(_ *xteveLinkFixture, s fakeSecrets) { delete(s, "XTEVE_USERNAME"); delete(s, "XTEVE_PASSWORD") }},
		{"partial pair", func(_ *xteveLinkFixture, s fakeSecrets) { delete(s, "XTEVE_PASSWORD") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newXteveLinkFixture()
			from, to, secrets := f.serve(t)
			tc.change(f, secrets)
			var status stepstatus.Step
			from.Record = func(result helpers.StepResult) { status = result.Status }
			err := linkFor(t, "xteve").Apply(t.Context(), from, to)
			if err == nil && status != stepstatus.StepManual {
				t.Fatal("unrecoverable credentials completed public Link")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.apps) != 0 || len(f.integrations) != 0 || len(f.items) != 0 || f.catalogs != 0 || f.refreshes != 0 {
				t.Fatal("failed preflight mutated Homarr or read Stats")
			}
		})
	}
}
