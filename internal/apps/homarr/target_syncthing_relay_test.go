package homarr

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

const relayStatus = `{"version":"v2.1.7","startTime":"2026-01-01T00:00:00Z","uptimeSeconds":10,"options":{"provided-by":"private"},"numActiveSessions":0,"numConnections":0,"bytesProxied":0}`

func relayServer(t *testing.T, status int, contentType, body string, requests *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if r.URL.Path != "/status" || r.Header.Get("Authorization") != "" {
			t.Error("relay read used the wrong path or sent authentication")
		}
		if status == http.StatusFound {
			http.Redirect(w, r, "/elsewhere", status)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func relayHelpers() *helpers.H {
	return helpers.New(helpers.Config{App: "homarr", Log: events.Discard()})
}

func TestSyncthingRelayInputIsStatusOnlyAndCredentialFree(t *testing.T) {
	c := helpers.ExternalConnection{ID: "0123456789abcdef0123456789abcdef", Kind: "syncthingRelay", Endpoint: "http://relay.example.invalid:22070"}
	d, err := externalConnectionDescriptor(c)
	if err != nil || d.kind != "syncthingRelay" || d.id != "external-"+c.ID || !d.integrationOnly || len(d.widgets) != 1 || len(d.widgets[0].metrics) != 3 || len(d.credentials(nil, nil)) != 0 {
		t.Fatalf("descriptor: %v", err)
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Kind = "syncthingrelay" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "/status" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "?token=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://user:private@relay.example.invalid:22070" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "ftp://relay.example.invalid" },
		func(c *helpers.ExternalConnection) { c.Endpoint = "http://relay.example.invalid:70000" },
		func(c *helpers.ExternalConnection) {
			c.Credentials = []helpers.ExternalCredential{{Kind: "apiKey", Value: "private"}}
		},
		func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"scope": "x"} },
	} {
		input := c
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid Syncthing Relay connection was accepted")
		}
	}
}

func TestSyncthingRelayStatusReadRejectsEverythingButAMeasuredStatusDocument(t *testing.T) {
	c := helpers.ExternalConnection{ID: "0123456789abcdef0123456789abcdef", Kind: "syncthingRelay"}
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErr     bool
	}{
		{"zero counters", 200, "application/json", relayStatus, false},
		{"nonzero counters", 200, "application/json", `{"version":"v2.1.7","startTime":"t","numActiveSessions":3,"numConnections":9,"bytesProxied":1024}`, false},
		{"html gui", 200, "text/html", "<html>Syncthing</html>", true},
		{"unauthorized", 401, "application/json", relayStatus, true},
		{"forbidden", 403, "application/json", relayStatus, true},
		{"redirect", 302, "application/json", relayStatus, true},
		{"missing version", 200, "application/json", `{"startTime":"t","numActiveSessions":0,"numConnections":0,"bytesProxied":0}`, true},
		{"missing start time", 200, "application/json", `{"version":"v","numActiveSessions":0,"numConnections":0,"bytesProxied":0}`, true},
		{"missing counter", 200, "application/json", `{"version":"v","startTime":"t","numActiveSessions":0,"numConnections":0}`, true},
		{"null counter", 200, "application/json", `{"version":"v","startTime":"t","numActiveSessions":null,"numConnections":0,"bytesProxied":0}`, true},
		{"string counter", 200, "application/json", `{"version":"v","startTime":"t","numActiveSessions":"0","numConnections":0,"bytesProxied":0}`, true},
		{"negative counter", 200, "application/json", `{"version":"v","startTime":"t","numActiveSessions":0,"numConnections":-1,"bytesProxied":0}`, true},
		{"fractional counter", 200, "application/json", `{"version":"v","startTime":"t","numActiveSessions":0,"numConnections":0,"bytesProxied":1.5}`, true},
		{"not json", 200, "application/json", "nope", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			srv := relayServer(t, tc.status, tc.contentType, tc.body, &requests)
			c.Endpoint = srv.URL
			d, err := syncthingRelayExternalTarget(c)
			if err != nil {
				t.Fatal(err)
			}
			for name, run := range map[string]func() error{
				"preflight": func() error { return d.preflight(t.Context(), relayHelpers(), nil) },
				"verify":    func() error { return d.verify(t.Context(), relayHelpers(), nil, "id") },
			} {
				err := run()
				if tc.wantErr && !errors.Is(err, helpers.ErrManual) || !tc.wantErr && err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
			if requests != 2 {
				t.Fatalf("expected two bounded status reads, got %d", requests)
			}
		})
	}
}

func TestSyncthingRelayStatsKeepUnitsClockAndMeasuredZeros(t *testing.T) {
	valid := func() statsSample {
		return statsSample{
			values:  map[string]any{"numActiveSessions": float64(0), "numConnections": float64(0), "bytesProxied": float64(0)},
			units:   map[string]string{"numActiveSessions": "count", "numConnections": "count", "bytesProxied": "bytes"},
			retryAt: float64(0), startedAt: 10000, completedAt: 10010, updatedAt: 10005,
		}
	}
	if err := syncthingRelayValidateStats(valid()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*statsSample){
		func(s *statsSample) { delete(s.values, "bytesProxied") },
		func(s *statsSample) { s.values["numConnections"] = nil },
		func(s *statsSample) { s.values["numActiveSessions"] = float64(-1) },
		func(s *statsSample) { s.values["bytesProxied"] = 1.5 },
		func(s *statsSample) { s.values["bytesProxied"] = math.Inf(1) },
		func(s *statsSample) { s.units["bytesProxied"] = "count" },
		func(s *statsSample) { s.units["numConnections"] = "bytes" },
		func(s *statsSample) { s.retryAt = nil },
		func(s *statsSample) { s.retryAt = float64(12000) },
		func(s *statsSample) { s.updatedAt = 20000 },
		func(s *statsSample) { s.updatedAt = 0 },
		func(s *statsSample) { s.startedAt = 100000 },
	} {
		sample := valid()
		change(&sample)
		if err := syncthingRelayValidateStats(sample); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("invalid Syncthing Relay Stats were accepted")
		}
	}
}

func TestSyncthingRelayRemovalNeverReadsTheRelay(t *testing.T) {
	f := &externalConnectionFixture{}
	h := f.helpers(t)
	requests := 0
	srv := relayServer(t, 200, "application/json", relayStatus, &requests)
	c := helpers.ExternalConnection{ID: "0123456789abcdef0123456789abcdef", Kind: "syncthingRelay", Endpoint: srv.URL}
	h.ExternalConnections = func() ([]helpers.ExternalConnection, error) { return []helpers.ExternalConnection{c}, nil }
	if _, err := (App{}).SetupExternalConnections(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if f.created != 1 || h.Secret(h.Key("linked_external-"+c.ID)) == "" {
		t.Fatal("relay connection was not created through the shared owner")
	}
	reads := requests
	c.Removing = true
	removed, err := (App{}).SetupExternalConnections(t.Context(), h)
	if err != nil || len(removed) != 1 || requests != reads || h.Secret(h.Key("linked_external-"+c.ID)) != "" {
		t.Fatalf("removal contacted the relay or kept ownership: %v", err)
	}
	if len(f.integrations) != 1 || f.integrations[0]["id"] != "user-matching" {
		t.Fatal("removal disturbed an unrelated integration")
	}
}
