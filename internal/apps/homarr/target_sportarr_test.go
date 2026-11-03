package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
)

type sportarrConnectorFixture struct {
	appName       string
	method        string
	media         any
	mediaStatus   int
	mediaRequests atomic.Int32
	calendar      any
	calendarFails bool
	calendarReqs  atomic.Int32
	otherRequests atomic.Int32
}

func sportarrConnectorSample() map[string]any {
	return map[string]any{"integrationId": "sportarr-owned", "missing": []any{}, "queued": []any{}, "missingCount": 0, "queuedCount": 0}
}

func sportarrConnectorMissing() map[string]any {
	return map[string]any{
		"id": 7, "title": "Synthetic sports event", "type": "episode", "seasonNumber": 2026, "episodeNumber": 0,
		"year": nil, "seriesTitle": nil, "imageUrl": nil, "link": "https://sports.example/sportarr/activity",
	}
}

func sportarrConnectorQueued() map[string]any {
	return map[string]any{
		"id": 2_000_017, "title": "Synthetic pending import", "type": "episode", "status": "completed", "timeLeft": nil,
		"percentComplete": 100, "seasonNumber": nil, "episodeNumber": nil, "seriesTitle": nil, "imageUrl": nil,
		"link": "https://sports.example/sportarr/activity",
	}
}

func sportarrConnectorPeer(t *testing.T, f *sportarrConnectorFixture) *helpers.H {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/system/status":
			if r.Header.Get("X-Api-Key") != "sportarr-fixture-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			name := f.appName
			if name == "" {
				name = "Sportarr"
			}
			json.NewEncoder(w).Encode(map[string]any{"appName": name, "instanceName": "Sportarr", "version": "4.1.11.1121", "sportarrVersion": "4.1.11", "authentication": "apikey"})
		case "POST /api/login":
			var body struct{ Username, Password string }
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Username != "admin" || body.Password != "sportarr-fixture-password" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SportarrAuth", Value: "sportarr-fixture-session", HttpOnly: true, Path: "/"})
			w.Write([]byte(`{"success":true}`))
		case "GET /api/auth/check", "GET /api/settings", "POST /api/logout":
			cookie, err := r.Cookie("SportarrAuth")
			if err != nil || cookie.Value != "sportarr-fixture-session" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/api/auth/check" {
				w.Write([]byte(`{"authenticated":true,"username":"admin"}`))
				return
			}
			if r.URL.Path == "/api/logout" {
				w.Write([]byte(`{"message":"Logged out successfully"}`))
				return
			}
			method := f.method
			if method == "" {
				method = "forms"
			}
			security, _ := json.Marshal(map[string]any{"authenticationMethod": method, "authenticationRequired": "enabled", "username": "admin", "apiKey": "sportarr-fixture-key"})
			json.NewEncoder(w).Encode(map[string]any{"securitySettings": string(security)})
		default:
			t.Errorf("unexpected Sportarr request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return helpers.New(helpers.Config{
		App: "sportarr", Service: "sportarr", ContainerPort: 1867, Port: 21867,
		BaseURL: srv.URL, URL: "https://sports.example/sportarr", HTTP: srv.Client(), Log: events.Discard(),
		Secrets: statsSecrets{"SPORTARR_API_KEY": "sportarr-fixture-key", "ADMIN_USER": "admin", "ADMIN_PASSWORD": "sportarr-fixture-password"},
	})
}

func sportarrConnectorHomarr(t *testing.T, f *sportarrConnectorFixture) *helpers.H {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/trpc/widget.calendar.findAllEvents" {
			f.calendarReqs.Add(1)
			if f.calendarFails {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			value := f.calendar
			if value == nil {
				value = []any{map[string]any{"integration": map[string]any{"id": "sportarr-owned", "kind": "sportarr"}, "events": []any{map[string]any{"title": "Synthetic event", "startDate": "2026-11-03T20:00:00Z"}}}}
			}
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
			return
		}
		if r.URL.Path != "/api/trpc/widget.mediaOrganizer.getData" {
			f.otherRequests.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mediaRequests.Add(1)
		var input struct {
			JSON struct {
				IDs      []string `json:"integrationIds"`
				PageSize int      `json:"pageSize"`
			} `json:"json"`
		}
		if r.Method != http.MethodGet || r.Header.Get("ApiKey") != "homarr-fixture-key" || json.Unmarshal([]byte(r.URL.Query().Get("input")), &input) != nil || !reflect.DeepEqual(input.JSON.IDs, []string{"sportarr-owned"}) || input.JSON.PageSize != 50 {
			t.Error("incorrect shared authenticated bounded Media Organizer request")
		}
		if f.mediaStatus != 0 {
			w.WriteHeader(f.mediaStatus)
			w.Write([]byte("private-provider-marker"))
			return
		}
		value := f.media
		if value == nil {
			value = []any{sportarrConnectorSample()}
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
	}))
	t.Cleanup(srv.Close)
	return helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "homarr-fixture-key"}})
}

func TestSportarrConnectorDescriptorUsesOnlyItsNativeAdmission(t *testing.T) {
	f := &sportarrConnectorFixture{}
	to := sportarrConnectorPeer(t, f)
	descriptor := sportarrTarget()
	if descriptor.id != "sportarr" || descriptor.name != "Sportarr" || descriptor.kind != "sportarr" || descriptor.internal(to) != "http://sportarr:1867" {
		t.Fatal("Sportarr descriptor lost its own identity or internal port")
	}
	if !reflect.DeepEqual(descriptor.credentials(nil, to), []secret{{Kind: "apiKey", Value: "sportarr-fixture-key"}}) {
		t.Fatal("descriptor did not consume the App-owned key")
	}
	kinds := []string{}
	for _, widget := range descriptor.widgets {
		kinds = append(kinds, widget.kind)
	}
	if !reflect.DeepEqual(kinds, []string{"calendar", "mediaMissing"}) || descriptor.stats != nil || len(descriptor.statsMetrics()) != 0 {
		t.Fatal("Sportarr must retain Calendar and Media Missing without Stats admission")
	}
	link := descriptor.link()
	if link.Target != "sportarr" || link.ID != "link-sportarr" {
		t.Fatal("descriptor did not use the common consuming Link")
	}
}

func TestSportarrConnectorReadsEmptyStandaloneAndLeagueData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"authenticated empty", func(map[string]any) {}},
		{"standalone event without league identity", func(sample map[string]any) {
			sample["missing"], sample["missingCount"] = []any{sportarrConnectorMissing()}, 1
			sample["queued"], sample["queuedCount"] = []any{sportarrConnectorQueued()}, 1
		}},
		{"league link", func(sample map[string]any) {
			item := sportarrConnectorMissing()
			item["link"], item["seriesTitle"], item["year"] = "https://sports.example/sportarr/leagues/17", "Synthetic league", 2026
			sample["missing"], sample["missingCount"] = []any{item}, 1
		}},
		{"league with optional identifier omitted", func(sample map[string]any) {
			item := sportarrConnectorMissing()
			item["link"] = "https://sports.example/sportarr/leagues"
			sample["missing"], sample["missingCount"] = []any{item}, 1
		}},
		{"optional queue metadata omitted", func(sample map[string]any) {
			item := sportarrConnectorQueued()
			delete(item, "seasonNumber")
			delete(item, "episodeNumber")
			delete(item, "seriesTitle")
			delete(item, "imageUrl")
			item["status"], item["percentComplete"], item["timeLeft"] = "downloading", 0, "00:00:03"
			sample["queued"], sample["queuedCount"] = []any{item}, 1
		}},
		{"bounded first page with larger total", func(sample map[string]any) {
			items := []any{}
			for id := range 50 {
				item := sportarrConnectorMissing()
				item["id"] = id + 1
				items = append(items, item)
			}
			sample["missing"], sample["missingCount"] = items, 83
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample := sportarrConnectorSample()
			tc.change(sample)
			f := &sportarrConnectorFixture{media: []any{sample}}
			if err := sportarrTarget().verifyData(t.Context(), sportarrConnectorHomarr(t, f), sportarrConnectorPeer(t, f), "sportarr-owned"); err != nil {
				t.Fatal(err)
			}
			if f.mediaRequests.Load() != 1 || f.otherRequests.Load() != 0 {
				t.Fatal("verification must use one shared media read, without Stats")
			}
		})
	}
}

func TestSportarrConnectorRejectsIncompleteAndWrongNativeMedia(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any, map[string]any)
	}{
		{"wrong integration", func(s, _, _ map[string]any) { s["integrationId"] = "another-owned" }},
		{"error fallback", func(s, _, _ map[string]any) { s["error"] = "private-provider-marker" }},
		{"null array", func(s, _, _ map[string]any) { s["missing"] = nil }},
		{"null count", func(s, _, _ map[string]any) { s["queuedCount"] = nil }},
		{"fractional count", func(s, _, _ map[string]any) { s["missingCount"] = 1.5 }},
		{"empty positive missing", func(s, _, _ map[string]any) { s["missing"] = []any{} }},
		{"empty positive queued", func(s, _, _ map[string]any) { s["queued"] = []any{} }},
		{"null required event identifier", func(_, m, _ map[string]any) { m["id"] = nil }},
		{"fractional event identifier", func(_, m, _ map[string]any) { m["id"] = 7.5 }},
		{"zero event identifier", func(_, m, _ map[string]any) { m["id"] = 0 }},
		{"out of range event identifier", func(_, m, _ map[string]any) { m["id"] = 2147483648 }},
		{"missing event title", func(_, m, _ map[string]any) { delete(m, "title") }},
		{"wrong media type", func(_, m, _ map[string]any) { m["type"] = "movie" }},
		{"null missing season", func(_, m, _ map[string]any) { m["seasonNumber"] = nil }},
		{"fractional episode", func(_, m, _ map[string]any) { m["episodeNumber"] = 1.5 }},
		{"invalid optional queue number", func(_, _, q map[string]any) { q["seasonNumber"] = "2026" }},
		{"invalid nullable title", func(_, m, _ map[string]any) { m["seriesTitle"] = 1 }},
		{"null link", func(_, m, _ map[string]any) { m["link"] = nil }},
		{"foreign link", func(_, m, _ map[string]any) { m["link"] = "https://elsewhere.example/sportarr/activity" }},
		{"Sonarr route", func(_, m, _ map[string]any) { m["link"] = "https://sports.example/sportarr/series/a-league" }},
		{"wrong base path", func(_, m, _ map[string]any) { m["link"] = "https://sports.example/activity" }},
		{"nonnumeric league route", func(_, m, _ map[string]any) { m["link"] = "https://sports.example/sportarr/leagues/a-league" }},
		{"credential-bearing link", func(_, m, _ map[string]any) {
			m["link"] = "https://user:private-provider-marker@sports.example/sportarr/activity"
		}},
		{"query-bearing link", func(_, m, _ map[string]any) {
			m["link"] = "https://sports.example/sportarr/activity?apikey=private-provider-marker"
		}},
		{"invalid queue status", func(_, _, q map[string]any) { q["status"] = "invented" }},
		{"missing progress", func(_, _, q map[string]any) { delete(q, "percentComplete") }},
		{"excess progress", func(_, _, q map[string]any) { q["percentComplete"] = 101 }},
		{"fractional progress", func(_, _, q map[string]any) { q["percentComplete"] = 0.5 }},
		{"missing nullable time left", func(_, _, q map[string]any) { delete(q, "timeLeft") }},
		{"invalid time left", func(_, _, q map[string]any) { q["timeLeft"] = 0 }},
		{"duplicate event identifiers", func(s, m, _ map[string]any) { s["missing"], s["missingCount"] = []any{m, m}, 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sample, missing, queued := sportarrConnectorSample(), sportarrConnectorMissing(), sportarrConnectorQueued()
			sample["missing"], sample["missingCount"], sample["queued"], sample["queuedCount"] = []any{missing}, 1, []any{queued}, 1
			tc.change(sample, missing, queued)
			f := &sportarrConnectorFixture{media: []any{sample}}
			err := sportarrTarget().verifyData(t.Context(), sportarrConnectorHomarr(t, f), sportarrConnectorPeer(t, f), "sportarr-owned")
			if err == nil || strings.Contains(err.Error(), "private-provider-marker") || f.mediaRequests.Load() != 1 || f.otherRequests.Load() != 0 {
				t.Fatalf("invalid native data accepted/leaked or wrong shared request count: %v", err)
			}
		})
	}
}

func TestSportarrConnectorAppPrerequisitesStopBeforeHomarr(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*sportarrConnectorFixture, *helpers.H)
		manual bool
	}{
		{"Sonarr compatibility identity", func(f *sportarrConnectorFixture, _ *helpers.H) { f.appName = "Sonarr" }, false},
		{"missing App key", func(_ *sportarrConnectorFixture, to *helpers.H) { to.Secrets.Set("SPORTARR_API_KEY", "") }, true},
		{"rejected App key", func(_ *sportarrConnectorFixture, to *helpers.H) { to.Secrets.Set("SPORTARR_API_KEY", "rejected-key") }, true},
		{"person-owned external auth", func(f *sportarrConnectorFixture, _ *helpers.H) { f.method = "external" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &sportarrConnectorFixture{}
			from, to := sportarrConnectorHomarr(t, f), sportarrConnectorPeer(t, f)
			tc.change(f, to)
			descriptor := sportarrTarget()
			if linked, err := descriptor.linked(t.Context(), from, to); linked || err == nil || errors.Is(err, helpers.ErrManual) != tc.manual {
				t.Fatalf("unexpected readiness classification: linked=%v err=%v", linked, err)
			}
			var steps []helpers.StepResult
			from.Record = func(result helpers.StepResult) { steps = append(steps, result) }
			err := descriptor.link().Apply(t.Context(), from, to)
			if err == nil || errors.Is(err, helpers.ErrManual) != tc.manual || len(steps) != 1 {
				t.Fatalf("Link lost the App's readiness boundary: %v", err)
			}
			want := status.StepFail
			if tc.manual {
				want = status.StepManual
			}
			if steps[0].Status != want || f.mediaRequests.Load() != 0 || f.otherRequests.Load() != 0 {
				t.Fatal("App prerequisite reached Homarr or lost its saved status")
			}
		})
	}
}

func TestSportarrConnectorMediaFailureIsNotAnEmptyLibrary(t *testing.T) {
	f := &sportarrConnectorFixture{mediaStatus: http.StatusUnauthorized}
	err := sportarrTarget().verifyData(t.Context(), sportarrConnectorHomarr(t, f), sportarrConnectorPeer(t, f), "sportarr-owned")
	if err == nil || strings.Contains(err.Error(), "private-provider-marker") || f.mediaRequests.Load() != 1 || f.otherRequests.Load() != 0 {
		t.Fatalf("Media Organizer authentication failure became empty success: %v", err)
	}
}

func TestSportarrConnectorUsesTheSelectedBrowserAddressForMediaLinks(t *testing.T) {
	for _, stale := range []bool{false, true} {
		sample, item := sportarrConnectorSample(), sportarrConnectorMissing()
		if !stale {
			item["link"] = "https://access.example:9443/sports/activity"
		}
		sample["missing"], sample["missingCount"] = []any{item}, 1
		f := &sportarrConnectorFixture{media: []any{sample}}
		from, to := sportarrConnectorHomarr(t, f), sportarrConnectorPeer(t, f)
		from.InstalledApps = func(context.Context) ([]helpers.InstalledApp, error) {
			return []helpers.InstalledApp{{ID: "sportarr", URL: "https://access.example:9443/sports"}}, nil
		}
		err := sportarrTarget().verifyData(t.Context(), from, to, "sportarr-owned")
		if (err != nil) != stale {
			t.Fatalf("stale=%v Address validation error=%v", stale, err)
		}
	}
}

func TestSportarrConnectorRequiresItsExactAdvertisedCredentialAlternative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		secrets []any
		valid   bool
	}{
		{"apiKey", "sportarr", []any{[]any{"apiKey"}}, true},
		{"Sonarr is not Sportarr", "sonarr", []any{[]any{"apiKey"}}, false},
		{"case sensitive kind", "Sportarr", []any{[]any{"apiKey"}}, false},
		{"empty secret alternative", "sportarr", []any{[]any{}}, false},
		{"different credentials", "sportarr", []any{[]any{"username", "password"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("ApiKey") != "homarr-fixture-key" {
					t.Error("compatibility check was not authenticated/read-only")
				}
				var value any
				switch r.URL.Path {
				case "/api/trpc/info.getInfo":
					value = map[string]any{"version": "2.99.0"}
				case "/api/trpc/integration.getKinds":
					value = []any{map[string]any{"kind": tc.kind, "requiredSecrets": tc.secrets}}
				default:
					t.Error("unexpected compatibility request")
				}
				json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
			}))
			t.Cleanup(srv.Close)
			from := helpers.New(helpers.Config{App: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "homarr-fixture-key"}})
			to := sportarrConnectorPeer(t, &sportarrConnectorFixture{})
			if err := sportarrTarget().compatible(t.Context(), from, to); (err == nil) != tc.valid {
				t.Fatalf("valid=%v compatibility error=%v", tc.valid, err)
			}
		})
	}
}

func TestSportarrConnectorBrowserLinkNormalization(t *testing.T) {
	base, err := url.Parse("https://sports.example:443/sportarr/")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		link  string
		valid bool
	}{
		{"https://SPORTS.example/sportarr/activity", true},
		{"https://sports.example:443/sportarr/leagues/17", true},
		{"http://sports.example/sportarr/activity", false},
		{"https://sports.example:9443/sportarr/activity", false},
		{"https://sports.example/sportarr/activity?", false},
		{"https://sports.example/sportarr/leagues/0", false},
		{"https://sports.example/sportarr/leagues/2147483648", false},
		{"https://sports.example/sportarr/leagues/17/other", false},
	} {
		if valid := sportarrMediaLink(tc.link, base); valid != tc.valid {
			t.Errorf("link %q valid=%v, want %v", tc.link, valid, tc.valid)
		}
	}
}

func TestSportarrConnectorCalendarMustBeNativeAndComplete(t *testing.T) {
	ok := func(events any) any {
		return []any{map[string]any{"integration": map[string]any{"id": "sportarr-owned", "kind": "sportarr"}, "events": events}}
	}
	for _, tc := range []struct {
		name     string
		calendar any
		fails    bool
		valid    bool
	}{
		{"empty events", ok([]any{}), false, true},
		{"valid event", ok([]any{map[string]any{"title": "Event", "startDate": "2026-11-03T20:00:00Z"}}), false, true},
		{"HTTP failure", nil, true, false},
		{"error fallback", []any{map[string]any{"error": "private-provider-marker"}}, false, false},
		{"null events", ok(nil), false, false},
		{"wrong kind", []any{map[string]any{"integration": map[string]any{"id": "sportarr-owned", "kind": "sonarr"}, "events": []any{}}}, false, false},
		{"wrong integration", []any{map[string]any{"integration": map[string]any{"id": "other", "kind": "sportarr"}, "events": []any{}}}, false, false},
		{"missing title", ok([]any{map[string]any{"startDate": "2026-11-03T20:00:00Z"}}), false, false},
		{"invalid date", ok([]any{map[string]any{"title": "Event", "startDate": "soon"}}), false, false},
		{"two results", []any{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &sportarrConnectorFixture{calendar: tc.calendar, calendarFails: tc.fails}
			err := sportarrTarget().verifyData(t.Context(), sportarrConnectorHomarr(t, f), sportarrConnectorPeer(t, f), "sportarr-owned")
			if (err == nil) != tc.valid || err != nil && strings.Contains(err.Error(), "private-provider-marker") || f.calendarReqs.Load() != 1 {
				t.Fatalf("valid=%v error=%v calendar requests=%d", tc.valid, err, f.calendarReqs.Load())
			}
		})
	}
}
