package homarr

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

func newOverseerrPeer() *jellyseerrPeer {
	p := newJellyseerrPeer()
	p.api, p.version = "Overseerr API", "1.35.0"
	return p
}

func overseerrConnection(endpoint string) helpers.ExternalConnection {
	c := jellyseerrConnection(endpoint)
	c.Kind = "overseerr"
	return c
}

func TestOverseerrDescriptorIsExactStatsOnlyIntegration(t *testing.T) {
	descriptor, err := externalConnectionDescriptor(overseerrConnection("https://overseerr.example.invalid:5055"))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.kind != "overseerr" || !descriptor.integrationOnly || len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" {
		t.Fatalf("unexpected descriptor %+v", descriptor)
	}
	if got := strings.Join(descriptor.statsMetrics(), ","); got != "pending,approved,available,processing" {
		t.Fatalf("stats metrics %s", got)
	}
	for name, change := range map[string]func(*helpers.ExternalConnection){
		"seerr alias":      func(c *helpers.ExternalConnection) { c.Kind = "seerr" },
		"jellyseerr alias": func(c *helpers.ExternalConnection) { c.Kind = "jellyseerr" },
		"plain http":       func(c *helpers.ExternalConnection) { c.Endpoint = "http://overseerr.example.invalid" },
		"path":             func(c *helpers.ExternalConnection) { c.Endpoint = "https://overseerr.example.invalid/api/v1" },
		"no key":           func(c *helpers.ExternalConnection) { c.Credentials = nil },
	} {
		t.Run(name, func(t *testing.T) {
			c := overseerrConnection("https://overseerr.example.invalid")
			change(&c)
			if c.Kind == "jellyseerr" {
				descriptor, err := externalConnectionDescriptor(c)
				if err != nil || descriptor.kind != "jellyseerr" {
					t.Fatal("jellyseerr kind must stay distinct")
				}
				return
			}
			if _, err := externalConnectionDescriptor(c); err == nil {
				t.Fatal("invalid connection accepted")
			}
		})
	}
}

func TestOverseerrPreflightRequiresOverseerrMarkerThenHolds(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		change   func(*jellyseerrPeer)
		wantHold bool
	}{
		{"valid identity reaches transport hold", nil, true},
		{"jellyseerr marker rejected", func(p *jellyseerrPeer) { p.api = "Jellyseerr API" }, false},
		{"uninitialized never initialized", func(p *jellyseerrPeer) { p.initialized = false }, false},
		{"rejected key", func(p *jellyseerrPeer) { p.meStatus = http.StatusForbidden }, false},
		{"redirect not followed", func(p *jellyseerrPeer) { p.redirect = "https://redirect.example.invalid/api/v1" }, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			peer := newOverseerrPeer()
			if scenario.change != nil {
				scenario.change(peer)
			}
			srv := peer.server(t)
			descriptor, err := externalConnectionDescriptor(overseerrConnection(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			from := newStatsFixture().helpers(t)
			from.Config.HTTP = srv.Client()
			err = descriptor.preflight(t.Context(), from, nil)
			if err == nil || !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("preflight = %v", err)
			}
			if held := strings.Contains(err.Error(), "native fetch sets no redirect override"); held != scenario.wantHold {
				t.Fatalf("hold=%v want %v: %v", held, scenario.wantHold, err)
			}
			if !strings.Contains(err.Error(), "Overseerr") || strings.Contains(err.Error(), "fixture-jellyseerr-key") {
				t.Fatalf("unexpected message %v", err)
			}
			for _, path := range peer.authed {
				if path != "/api/v1/auth/me" {
					t.Fatalf("API key sent to %s before identity was proven", path)
				}
			}
		})
	}
}

func TestOverseerrVerifyDataEnforcesApprovedPartition(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		change        func(*statsFixture, *jellyseerrPeer)
		wantError     bool
		wantManual    bool
		wantRefreshes int
	}{
		{"legitimate zeros", nil, false, false, 1},
		{"partitioned counts", func(f *statsFixture, p *jellyseerrPeer) {
			f.values = map[string]any{"pending": 3, "approved": 2, "available": 1, "processing": 1}
			p.counts = map[string]any{"total": 5, "movie": 3, "tv": 2, "pending": 3, "approved": 2, "declined": 0, "processing": 1, "available": 1}
		}, false, false, 1},
		{"peer partition broken", func(_ *statsFixture, p *jellyseerrPeer) { p.counts["approved"], p.counts["available"] = 3, 2 }, true, true, 0},
		{"peer count missing", func(_ *statsFixture, p *jellyseerrPeer) { delete(p.counts, "declined") }, true, true, 0},
		{"stats partition broken", func(f *statsFixture, _ *jellyseerrPeer) { f.values["approved"] = 1 }, true, false, 1},
		{"stats count null", func(f *statsFixture, _ *jellyseerrPeer) { f.values["available"] = nil }, true, false, 1},
		{"wrong unit", func(f *statsFixture, _ *jellyseerrPeer) { f.metrics[0].(map[string]any)["unit"] = "bytes" }, true, false, 1},
		{"cached no-op", func(f *statsFixture, _ *jellyseerrPeer) { f.advance = false }, true, false, 1},
		{"retry scheduled", func(f *statsFixture, _ *jellyseerrPeer) { f.retryAt, f.hasRetryAt = float64(9000), true }, true, false, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newStatsFixture()
			f.values = map[string]any{"pending": 0, "approved": 0, "available": 0, "processing": 0}
			f.metrics = nil
			for _, key := range []string{"pending", "approved", "available", "processing"} {
				f.metrics = append(f.metrics, map[string]any{"key": key, "unit": "count"})
			}
			f.retryAt, f.hasRetryAt = float64(0), true
			peer := newOverseerrPeer()
			if scenario.change != nil {
				scenario.change(f, peer)
			}
			srv := peer.server(t)
			descriptor, err := externalConnectionDescriptor(overseerrConnection(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			h := f.helpers(t)
			h.Config.HTTP = srv.Client()
			err = descriptor.verifyData(t.Context(), h, nil, "owned")
			if (err != nil) != scenario.wantError {
				t.Fatalf("verifyData error = %v, wantError %v", err, scenario.wantError)
			}
			if err != nil && errors.Is(err, helpers.ErrManual) != scenario.wantManual {
				t.Fatalf("Manual classification = %v: %v", errors.Is(err, helpers.ErrManual), err)
			}
			if f.refreshes != scenario.wantRefreshes || f.catalogs != scenario.wantRefreshes {
				t.Fatalf("refreshes=%d catalogs=%d want=%d", f.refreshes, f.catalogs, scenario.wantRefreshes)
			}
		})
	}
}
