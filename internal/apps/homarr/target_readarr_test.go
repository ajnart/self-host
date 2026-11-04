package homarr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type readarrPeer struct {
	appName string
	books   any
	methods []string
}

func (p *readarrPeer) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.methods = append(p.methods, r.Method)
		if r.Header.Get("X-Api-Key") != "fixture-readarr-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/status":
			json.NewEncoder(w).Encode(map[string]any{"appName": p.appName, "version": "0.4.18.2805"})
		case "/api/v1/book":
			json.NewEncoder(w).Encode(p.books)
		default:
			t.Errorf("unexpected peer request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readarrConnection(endpoint string, credentials ...helpers.ExternalCredential) helpers.ExternalConnection {
	return helpers.ExternalConnection{ID: "abc", Kind: "readarr", Endpoint: endpoint, Credentials: credentials}
}

func readarrKey(value string) helpers.ExternalCredential {
	return helpers.ExternalCredential{Kind: "apiKey", Value: value}
}

func TestReadarrExternalDescriptorRequiresExactKindEndpointAndAPIKey(t *testing.T) {
	for _, scenario := range []struct {
		name string
		c    helpers.ExternalConnection
		ok   bool
	}{
		{"http origin", readarrConnection("http://readarr.lan:8787", readarrKey("k")), true},
		{"https origin", readarrConnection("https://readarr.example.test/", readarrKey("k")), true},
		{"other kind", helpers.ExternalConnection{ID: "abc", Kind: "bindery", Endpoint: "http://h:8787", Credentials: []helpers.ExternalCredential{readarrKey("k")}}, false},
		{"missing key", readarrConnection("http://h:8787"), false},
		{"empty key", readarrConnection("http://h:8787", readarrKey("")), false},
		{"extra credential", readarrConnection("http://h:8787", readarrKey("k"), helpers.ExternalCredential{Kind: "username", Value: "u"}), false},
		{"duplicate key", readarrConnection("http://h:8787", readarrKey("k"), readarrKey("k")), false},
		{"endpoint path", readarrConnection("http://h:8787/api", readarrKey("k")), false},
		{"endpoint credentials", readarrConnection("http://u:p@h:8787", readarrKey("k")), false},
		{"endpoint query", readarrConnection("http://h:8787?x=1", readarrKey("k")), false},
		{"ftp scheme", readarrConnection("ftp://h:8787", readarrKey("k")), false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := externalConnectionDescriptor(scenario.c)
			if (err == nil) != scenario.ok {
				t.Fatalf("descriptor error = %v, want ok %v", err, scenario.ok)
			}
		})
	}
}

func TestReadarrDescriptorSurfaceIsCalendarAndBookStats(t *testing.T) {
	d, err := externalConnectionDescriptor(readarrConnection("http://h:8787", readarrKey("k")))
	if err != nil {
		t.Fatal(err)
	}
	if d.kind != "readarr" || !d.integrationOnly || len(d.widgets) != 2 || d.widgets[0].kind != "calendar" || d.widgets[1].kind != "stats" {
		t.Fatalf("unexpected surface %+v", d)
	}
}

func TestReadarrPreflightAndLibraryAreReadOnlyAndManualWhenUnverified(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		key     string
		peer    readarrPeer
		preOK   bool
		libraOK bool
	}{
		{"valid empty library", "fixture-readarr-key", readarrPeer{appName: "Readarr", books: []any{}}, true, true},
		{"valid library", "fixture-readarr-key", readarrPeer{appName: "Readarr", books: []any{map[string]any{"monitored": false}}}, true, true},
		{"wrong key", "wrong", readarrPeer{appName: "Readarr", books: []any{}}, false, false},
		{"wrong identity", "fixture-readarr-key", readarrPeer{appName: "Sonarr", books: []any{}}, false, true},
		{"null library", "fixture-readarr-key", readarrPeer{appName: "Readarr", books: nil}, true, false},
		{"missing monitored", "fixture-readarr-key", readarrPeer{appName: "Readarr", books: []any{map[string]any{}}}, true, false},
		{"non array library", "fixture-readarr-key", readarrPeer{appName: "Readarr", books: map[string]any{"error": "x"}}, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			peer := scenario.peer
			srv := peer.serve(t)
			d, err := readarrExternalTarget(readarrConnection(srv.URL, readarrKey(scenario.key)))
			if err != nil {
				t.Fatal(err)
			}
			from := helpers.New(helpers.Config{App: "homarr", HTTP: srv.Client(), Log: events.Discard(), Secrets: statsSecrets{}})
			err = d.preflight(t.Context(), from, nil)
			if (err == nil) != scenario.preOK || err != nil && !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("preflight = %v, want ok %v as Manual", err, scenario.preOK)
			}
			stats := newStatsFixture()
			stats.values = map[string]any{"books": 0, "monitored": 0, "upcoming": 0}
			stats.metrics = []any{map[string]any{"key": "books", "unit": "count"}, map[string]any{"key": "monitored", "unit": "count"}, map[string]any{"key": "upcoming", "unit": "count"}}
			err = d.verify(t.Context(), stats.helpers(t), nil, "readarr-owned")
			if scenario.libraOK && err != nil && errors.Is(err, helpers.ErrManual) {
				t.Fatalf("valid library classified Manual: %v", err)
			}
			if !scenario.libraOK && !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("invalid library accepted: %v", err)
			}
			for _, method := range peer.methods {
				if method != http.MethodGet {
					t.Fatalf("peer received %s", method)
				}
			}
		})
	}
}

func TestReadarrStatsRequireCountsAndConsistentLibrary(t *testing.T) {
	d, err := readarrExternalTarget(readarrConnection("http://h:8787", readarrKey("k")))
	if err != nil {
		t.Fatal(err)
	}
	counts := func(books, monitored, upcoming any) statsSample {
		return statsSample{
			values: map[string]any{"books": books, "monitored": monitored, "upcoming": upcoming},
			units:  map[string]string{"books": "count", "monitored": "count", "upcoming": "count"},
		}
	}
	for _, scenario := range []struct {
		name   string
		sample statsSample
		ok     bool
	}{
		{"zeros", counts(0.0, 0.0, 0.0), true},
		{"upcoming above monitored", counts(5.0, 1.0, 4.0), true},
		{"negative", counts(-1.0, 0.0, 0.0), false},
		{"fractional", counts(1.0, 0.0, 0.5), false},
		{"null", counts(1.0, nil, 0.0), false},
		{"monitored above books", counts(1.0, 2.0, 0.0), false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := d.stats(scenario.sample); (err == nil) != scenario.ok {
				t.Fatalf("stats error = %v, want ok %v", err, scenario.ok)
			}
		})
	}
	wrongUnit := counts(1.0, 0.0, 0.0)
	wrongUnit.units["books"] = "bytes"
	if d.stats(wrongUnit) == nil {
		t.Fatal("wrong unit accepted")
	}
}
