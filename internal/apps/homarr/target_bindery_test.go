package homarr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ajnart/self-host/internal/apps/bindery"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
)

const binderyFixtureBrowser = "https://books.example.test/bindery"

type binderyFixtureStore struct {
	values map[string]string
	writes int
}

func (s *binderyFixtureStore) Get(key string) string { return s.values[key] }
func (s *binderyFixtureStore) Set(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

type binderyNativeFixture struct {
	mu            sync.Mutex
	result        map[string]any
	nativeWire    string
	rawMissing    any
	rawQueue      any
	rejectKey     bool
	omitKey       bool
	setupRequired bool
	nativeCalls   int
	unexpected    int
}

func newBinderyNativeFixture() *binderyNativeFixture {
	return &binderyNativeFixture{
		result: map[string]any{
			"integrationId": "bindery-owned", "integrationName": "Bindery",
			"missing": []any{}, "missingCount": 0, "queued": []any{}, "queuedCount": 0,
		},
		rawMissing: []any{}, rawQueue: map[string]any{"items": []any{}},
	}
}

func (f *binderyNativeFixture) helpers(t *testing.T) (*helpers.H, *helpers.H, *[]helpers.StepResult) {
	t.Helper()
	peerStore := &binderyFixtureStore{values: map[string]string{
		"BINDERY_API_KEY": "bindery-existing-key", "ADMIN_USER": "admin", "ADMIN_PASSWORD": "bindery-private-password",
	}}
	if f.omitKey {
		delete(peerStore.values, "BINDERY_API_KEY")
	}
	expectedKey := peerStore.Get("BINDERY_API_KEY")
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/status" {
			cookie, err := r.Cookie("bindery_session")
			signed := err == nil && cookie.Value == "bindery-private-session"
			body := map[string]any{"authenticated": signed, "setupRequired": f.setupRequired, "mode": "enabled", "localAuthEnabled": true}
			if signed {
				body["username"], body["role"] = "admin", "admin"
			}
			json.NewEncoder(w).Encode(body)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login" {
			var login struct{ Username, Password string }
			if json.NewDecoder(r.Body).Decode(&login) != nil || login.Username != "admin" || login.Password != "bindery-private-password" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "bindery_session", Value: "bindery-private-session", Path: "/", HttpOnly: true})
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "username": "admin"})
			return
		}
		if r.Method != http.MethodGet {
			f.unexpected++
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if f.rejectKey || r.Header.Get("X-Api-Key") != "bindery-existing-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/status":
			json.NewEncoder(w).Encode(map[string]any{"version": "1.46.1", "commit": "ca24961c4c04f58a1cd96c2b8382b87f7eda215f"})
		case "/api/v1/rootfolder":
			w.Write([]byte(`[]`))
		case "/api/v1/wanted/missing":
			json.NewEncoder(w).Encode(f.rawMissing)
		case "/api/v1/queue":
			json.NewEncoder(w).Encode(f.rawQueue)
		default:
			f.unexpected++
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.nativeCalls++
		var input struct {
			JSON struct {
				IDs      []string `json:"integrationIds"`
				PageSize int      `json:"pageSize"`
			} `json:"json"`
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/trpc/widget.mediaOrganizer.getData" || r.Header.Get("ApiKey") != "homarr-existing-key" || json.Unmarshal([]byte(r.URL.Query().Get("input")), &input) != nil || len(input.JSON.IDs) != 1 || input.JSON.IDs[0] != "bindery-owned" || input.JSON.PageSize != 50 {
			f.unexpected++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.nativeWire != "" {
			w.Write([]byte(f.nativeWire))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": []any{f.result}}}})
	}))
	t.Cleanup(server.Close)
	var records []helpers.StepResult
	from := helpers.New(helpers.Config{
		App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(),
		Secrets: &binderyFixtureStore{values: map[string]string{"HOMARR_API_KEY": "homarr-existing-key"}},
		Record:  func(result helpers.StepResult) { records = append(records, result) },
	})
	to := helpers.New(helpers.Config{
		App: "bindery", Service: "bindery", ContainerPort: 8787,
		BaseURL: peer.URL, URL: binderyFixtureBrowser, HTTP: peer.Client(), Log: events.Discard(), Secrets: peerStore,
	})
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.unexpected != 0 || peerStore.writes != 0 || bindery.APIKey(to) != expectedKey {
			t.Error("Bindery verification copied a workflow, issued credentials, changed a saved key or made an unsupported/Stats request")
		}
	})
	return from, to, &records
}

func binderyFixtureMissing(id int) map[string]any {
	return map[string]any{
		"id": id, "title": "Existing book", "type": "book", "year": 1987,
		"seriesTitle": "Existing author", "imageUrl": nil,
		"link": binderyFixtureBrowser + "/book/" + strconv.Itoa(id),
	}
}

func binderyFixtureQueued() map[string]any {
	return map[string]any{
		"id": 91, "title": "Existing book", "type": "book", "status": "importBlocked",
		"percentComplete": 100, "timeLeft": nil, "imageUrl": nil,
		"link": binderyFixtureBrowser + "/book/17",
	}
}

func TestBinderyDescriptorUsesOnlyItsNativeSurfaceAndExistingKey(t *testing.T) {
	f := newBinderyNativeFixture()
	from, to, _ := f.helpers(t)
	descriptor := binderyTarget()
	if descriptor.id != "bindery" || descriptor.kind != "bindery" || descriptor.internal(to) != "http://bindery:8787" || descriptor.stats != nil || len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "mediaMissing" || len(descriptor.widgets[0].metrics) != 0 {
		t.Fatal("Bindery was treated as Readarr, admitted to Stats or given an unsupported widget")
	}
	credentials := descriptor.credentials(from, to)
	if len(credentials) != 1 || credentials[0].Kind != "apiKey" || credentials[0].Value != bindery.APIKey(to) {
		t.Fatal("descriptor did not consume Bindery's existing credential resolver")
	}
	for range 2 {
		if err := descriptor.verifyData(t.Context(), from, to, "bindery-owned"); err != nil {
			t.Fatalf("positive authenticated identity and meaningful native empty state: %v", err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nativeCalls != 2 {
		t.Fatal("verification did not use one reviewed media read per invocation")
	}
}

func TestBinderyNativeBookAndQueueSemantics(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*binderyNativeFixture)
		valid  bool
	}{
		{name: "populated independent samples", valid: true},
		{name: "failed item is useful data", valid: true, change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["status"] = "failed" }},
		{name: "absent year", valid: true, change: func(f *binderyNativeFixture) { delete(f.result["missing"].([]any)[0].(map[string]any), "year") }},
		{name: "absent author", valid: true, change: func(f *binderyNativeFixture) { delete(f.result["missing"].([]any)[0].(map[string]any), "seriesTitle") }},
		{name: "null year", valid: true, change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["year"] = nil }},
		{name: "null author", valid: true, change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["seriesTitle"] = nil }},
		{name: "null queue author", valid: true, change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["seriesTitle"] = nil }},
		{name: "public artwork", valid: true, change: func(f *binderyNativeFixture) {
			f.result["missing"].([]any)[0].(map[string]any)["imageUrl"] = "https://covers.example.test/17.jpg"
		}},
		{name: "active queue fallback", valid: true, change: func(f *binderyNativeFixture) {
			item := f.result["queued"].([]any)[0].(map[string]any)
			item["link"], item["percentComplete"], item["timeLeft"], item["status"] = binderyFixtureBrowser+"/queue", 63, "10m", "downloading"
		}},
		{name: "paginated full native slice", valid: true, change: func(f *binderyNativeFixture) {
			items := make([]any, 50)
			for i := range items {
				items[i] = binderyFixtureMissing(i + 1)
			}
			f.result["missing"], f.result["missingCount"] = items, 100
		}},
		{name: "wrong integration", change: func(f *binderyNativeFixture) { f.result["integrationId"] = "unrelated-owned" }},
		{name: "body error", change: func(f *binderyNativeFixture) { f.result["error"] = "bindery-private-response" }},
		{name: "null array", change: func(f *binderyNativeFixture) { f.result["missing"] = nil }},
		{name: "missing count", change: func(f *binderyNativeFixture) { delete(f.result, "missingCount") }},
		{name: "partial page", change: func(f *binderyNativeFixture) { f.result["missingCount"] = 2 }},
		{name: "wrong media type", change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["type"] = "episode" }},
		{name: "missing ID", change: func(f *binderyNativeFixture) { delete(f.result["missing"].([]any)[0].(map[string]any), "id") }},
		{name: "fractional ID", change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["id"] = 1.5 }},
		{name: "missing title", change: func(f *binderyNativeFixture) { delete(f.result["missing"].([]any)[0].(map[string]any), "title") }},
		{name: "duplicate ID", change: func(f *binderyNativeFixture) {
			f.result["missing"] = []any{binderyFixtureMissing(17), binderyFixtureMissing(17)}
			f.result["missingCount"] = 2
		}},
		{name: "wrong book destination", change: func(f *binderyNativeFixture) {
			f.result["missing"].([]any)[0].(map[string]any)["link"] = binderyFixtureBrowser + "/book/18"
		}},
		{name: "internal DNS destination", change: func(f *binderyNativeFixture) {
			f.result["missing"].([]any)[0].(map[string]any)["link"] = "http://bindery:8787/book/17"
		}},
		{name: "cross-origin destination", change: func(f *binderyNativeFixture) {
			f.result["queued"].([]any)[0].(map[string]any)["link"] = "https://unrelated.example.test/queue"
		}},
		{name: "relative artwork", change: func(f *binderyNativeFixture) {
			f.result["missing"].([]any)[0].(map[string]any)["imageUrl"] = "/api/v1/images"
		}},
		{name: "text year", change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["year"] = "1987" }},
		{name: "negative year", change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["year"] = -1 }},
		{name: "fractional year", change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["year"] = 1987.5 }},
		{name: "excess year", change: func(f *binderyNativeFixture) { f.result["missing"].([]any)[0].(map[string]any)["year"] = 10000 }},
		{name: "object author", change: func(f *binderyNativeFixture) {
			f.result["missing"].([]any)[0].(map[string]any)["seriesTitle"] = map[string]any{"name": "Existing author"}
		}},
		{name: "numeric queue author", change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["seriesTitle"] = 17 }},
		{name: "fractional progress", change: func(f *binderyNativeFixture) {
			f.result["queued"].([]any)[0].(map[string]any)["percentComplete"] = 99.5
		}},
		{name: "null progress", change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["percentComplete"] = nil }},
		{name: "over-complete progress", change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["percentComplete"] = 101 }},
		{name: "unfiltered imported history", change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["status"] = "imported" }},
		{name: "unknown state", change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["status"] = "paused" }},
		{name: "stale completed time", change: func(f *binderyNativeFixture) { f.result["queued"].([]any)[0].(map[string]any)["timeLeft"] = "721h 20m" }},
		{name: "missing time field", change: func(f *binderyNativeFixture) { delete(f.result["queued"].([]any)[0].(map[string]any), "timeLeft") }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBinderyNativeFixture()
			f.result["missing"], f.result["missingCount"] = []any{binderyFixtureMissing(17)}, 1
			f.result["queued"], f.result["queuedCount"] = []any{binderyFixtureQueued()}, 1
			if scenario.change != nil {
				scenario.change(f)
			}
			from, to, _ := f.helpers(t)
			err := binderyTarget().verifyData(t.Context(), from, to, "bindery-owned")
			if (err == nil) != scenario.valid || err != nil && strings.Contains(err.Error(), "bindery-private-response") {
				t.Fatalf("native validity=%v error=%v", scenario.valid, err)
			}
		})
	}
}

func TestBinderyOptionalMetadataThroughSuperJSONWire(t *testing.T) {
	const missing = `[
		{"id":17,"title":"Existing book","releaseDate":null,"author":{"authorName":"Existing author"}},
		{"id":18,"title":"Dated book","releaseDate":"1987-01-01"},
		{"id":19,"title":"Undated book"}
	]`
	const queue = `{"items":[
		{"id":91,"title":"Existing download","status":"downloading","percentage":"63.0","timeLeft":"10m"},
		{"id":92,"title":"Completed download","status":"importBlocked","percentage":"100.0","timeLeft":"721h 20m","book":{"id":17,"title":"Existing book"}}
	]}`
	const wire = `{"result":{"data":{
		"json":[{
			"integrationId":"bindery-owned","integrationName":"Bindery",
			"missing":[
				{"id":17,"title":"Existing book","type":"book","year":null,"seriesTitle":"Existing author","imageUrl":null,"link":"https://books.example.test/bindery/book/17"},
				{"id":18,"title":"Dated book","type":"book","year":1987,"seriesTitle":null,"imageUrl":null,"link":"https://books.example.test/bindery/book/18"},
				{"id":19,"title":"Undated book","type":"book","year":null,"seriesTitle":null,"imageUrl":null,"link":"https://books.example.test/bindery/book/19"}
			],"missingCount":3,
			"queued":[
				{"id":91,"title":"Existing download","type":"book","status":"downloading","timeLeft":"10m","percentComplete":63,"seriesTitle":null,"imageUrl":null,"link":"https://books.example.test/bindery/queue"},
				{"id":92,"title":"Existing book","type":"book","status":"importBlocked","timeLeft":null,"percentComplete":100,"seriesTitle":null,"imageUrl":null,"link":"https://books.example.test/bindery/book/17"}
			],"queuedCount":2,"error":null
		}],
		"meta":{"values":{
			"0.missing.0.year":["undefined"],
			"0.missing.1.seriesTitle":["undefined"],
			"0.missing.2.year":["undefined"],
			"0.missing.2.seriesTitle":["undefined"],
			"0.queued.0.seriesTitle":["undefined"],
			"0.queued.1.seriesTitle":["undefined"],
			"0.error":["undefined"]
		},"v":1}
	}}}`
	for _, scenario := range []struct {
		name        string
		old         string
		replacement string
		valid       bool
		rawFailure  bool
	}{
		{name: "source undefined metadata", valid: true},
		{name: "missing required ID", old: `"id":17,"title":"Existing book"`, replacement: `"id":null,"title":"Existing book"`},
		{name: "missing required title", old: `"id":18,"title":"Dated book"`, replacement: `"id":18,"title":null`},
		{name: "null missing array", old: `"missing":[`, replacement: `"missing":null,"ignored":[`},
		{name: "null queued array", old: `"queued":[`, replacement: `"queued":null,"ignored":[`},
		{name: "null missing count", old: `"missingCount":3`, replacement: `"missingCount":null`},
		{name: "fractional queued count", old: `"queuedCount":2`, replacement: `"queuedCount":1.5`},
		{name: "null destination", old: `"link":"https://books.example.test/bindery/book/17"`, replacement: `"link":null`},
		{name: "wrong destination", old: `"link":"https://books.example.test/bindery/book/18"`, replacement: `"link":"https://other.example.test/book/18"`},
		{name: "null progress", old: `"percentComplete":63`, replacement: `"percentComplete":null`},
		{name: "wrong integration", old: `"integrationId":"bindery-owned"`, replacement: `"integrationId":"unrelated-owned"`},
		{name: "native error", old: `"error":null`, replacement: `"error":"bindery-private-response"`},
		{name: "malformed raw release date", old: `"releaseDate":null`, replacement: `"releaseDate":"unknown"`, rawFailure: true},
		{name: "null raw book ID", old: `"id":17`, replacement: `"id":null`, rawFailure: true},
		{name: "null raw author object", old: `"author":{"authorName":"Existing author"}`, replacement: `"author":null`, rawFailure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBinderyNativeFixture()
			rawMissing := missing
			f.nativeWire = wire
			if scenario.old != "" {
				if scenario.rawFailure {
					if !strings.Contains(rawMissing, scenario.old) {
						t.Fatal("raw source fixture mutation did not match")
					}
					rawMissing = strings.Replace(rawMissing, scenario.old, scenario.replacement, 1)
				} else {
					if !strings.Contains(f.nativeWire, scenario.old) {
						t.Fatal("wire fixture mutation did not match")
					}
					f.nativeWire = strings.Replace(f.nativeWire, scenario.old, scenario.replacement, 1)
				}
			}
			if err := json.Unmarshal([]byte(rawMissing), &f.rawMissing); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(queue), &f.rawQueue); err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(f.nativeWire)) {
				t.Fatal("wire regression must reach the real decoder with valid JSON")
			}
			from, to, _ := f.helpers(t)
			err := binderyTarget().verifyData(t.Context(), from, to, "bindery-owned")
			if (err == nil) != scenario.valid || err != nil && strings.Contains(err.Error(), "bindery-private-response") {
				t.Fatalf("source/wire validity=%v error=%v", scenario.valid, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			wantCalls := 1
			if scenario.rawFailure {
				wantCalls = 0
			}
			if f.nativeCalls != wantCalls {
				t.Fatalf("raw preflight/native read ordering changed: calls=%d want=%d", f.nativeCalls, wantCalls)
			}
		})
	}
}

func TestBinderyReadinessStopsBeforeHomarrMutation(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(*binderyNativeFixture)
		manual bool
	}{
		{name: "existing rejected key", manual: true, change: func(f *binderyNativeFixture) { f.rejectKey = true }},
		{name: "missing existing key", manual: true, change: func(f *binderyNativeFixture) { f.omitKey = true }},
		{name: "first administrator", manual: true, change: func(f *binderyNativeFixture) { f.setupRequired = true }},
		{name: "partial raw queue", change: func(f *binderyNativeFixture) { f.rawQueue = map[string]any{"items": []any{}, "partial": true} }},
		{name: "stale raw queue", change: func(f *binderyNativeFixture) {
			f.rawQueue = map[string]any{"items": []any{}, "staleClients": []any{map[string]any{"clientId": 1}}}
		}},
		{name: "raw error body", change: func(f *binderyNativeFixture) {
			f.rawQueue = map[string]any{"items": []any{}, "error": "bindery-private-response"}
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBinderyNativeFixture()
			scenario.change(f)
			from, to, records := f.helpers(t)
			err := binderyTarget().link().Apply(t.Context(), from, to)
			if err == nil || errors.Is(err, helpers.ErrManual) != scenario.manual {
				t.Fatalf("prerequisite Manual=%v error=%v", scenario.manual, err)
			}
			if len(*records) != 1 {
				t.Fatal("prerequisite was not recorded on the consuming Homarr Link")
			}
			want := stepstatus.StepFail
			if scenario.manual {
				want = stepstatus.StepManual
			}
			if (*records)[0].Status != want {
				t.Fatal("prerequisite classification was lost by the existing Link pipeline")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.nativeCalls != 0 {
				t.Fatal("unready Bindery reached Homarr compatibility or resource work")
			}
		})
	}
}

func TestBinderyEmptyNativeDataStillRequiresAValidBrowserAddress(t *testing.T) {
	f := newBinderyNativeFixture()
	from, to, _ := f.helpers(t)
	to.URL = "not-a-browser-address"
	if err := binderyTarget().verifyData(t.Context(), from, to, "bindery-owned"); err == nil {
		t.Fatal("empty lists bypassed browser Address validation")
	}
}
