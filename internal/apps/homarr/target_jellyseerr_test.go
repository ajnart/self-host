package homarr

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/helpers"
)

type jellyseerrPeer struct {
	api         any
	version     any
	initialized any
	mediaServer any
	meStatus    int
	meID        any
	countStatus int
	counts      map[string]any
	requests    []string
	authed      []string
	redirect    string
}

func newJellyseerrPeer() *jellyseerrPeer {
	return &jellyseerrPeer{
		api: "Jellyseerr API", version: "2.7.3", initialized: true, mediaServer: float64(2),
		meStatus: http.StatusOK, meID: float64(1), countStatus: http.StatusOK,
		counts: map[string]any{"total": 0, "movie": 0, "tv": 0, "pending": 0, "approved": 0, "declined": 0, "processing": 0, "available": 0},
	}
}

func (p *jellyseerrPeer) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.requests = append(p.requests, r.URL.Path)
		if r.Header.Get("X-Api-Key") != "" {
			p.authed = append(p.authed, r.URL.Path)
		}
		if p.redirect != "" {
			http.Redirect(w, r, p.redirect, http.StatusFound)
			return
		}
		switch r.URL.Path {
		case "/api/v1":
			json.NewEncoder(w).Encode(map[string]any{"api": p.api})
		case "/api/v1/status":
			json.NewEncoder(w).Encode(map[string]any{"version": p.version})
		case "/api/v1/settings/public":
			json.NewEncoder(w).Encode(map[string]any{"initialized": p.initialized, "mediaServerType": p.mediaServer})
		case "/api/v1/auth/me":
			if r.Header.Get("X-Api-Key") != "fixture-jellyseerr-key" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(p.meStatus)
			json.NewEncoder(w).Encode(map[string]any{"id": p.meID})
		case "/api/v1/request/count":
			if r.Header.Get("X-Api-Key") != "fixture-jellyseerr-key" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(p.countStatus)
			json.NewEncoder(w).Encode(p.counts)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jellyseerrConnection(endpoint string) helpers.ExternalConnection {
	return helpers.ExternalConnection{
		ID: "abc", Kind: "jellyseerr", Endpoint: endpoint,
		Credentials: []helpers.ExternalCredential{{Kind: "apiKey", Value: "fixture-jellyseerr-key"}},
	}
}

func jellyseerrHomarr(t *testing.T, f *statsFixture, peer *httptest.Server) *helpers.H {
	t.Helper()
	h := f.helpers(t)
	h.Config.HTTP = peer.Client()
	return h
}

func TestJellyseerrExternalTargetRequiresExactEndpointAndKey(t *testing.T) {
	good := jellyseerrConnection("https://jellyseerr.example.invalid:5055")
	if _, err := externalConnectionDescriptor(good); err != nil {
		t.Fatalf("valid connection rejected: %v", err)
	}
	for name, change := range map[string]func(*helpers.ExternalConnection){
		"alias kind":          func(c *helpers.ExternalConnection) { c.Kind = "seerr" },
		"plain http":          func(c *helpers.ExternalConnection) { c.Endpoint = "http://jellyseerr.example.invalid" },
		"userinfo":            func(c *helpers.ExternalConnection) { c.Endpoint = "https://user:pass@jellyseerr.example.invalid" },
		"path":                func(c *helpers.ExternalConnection) { c.Endpoint = "https://jellyseerr.example.invalid/api/v1" },
		"query":               func(c *helpers.ExternalConnection) { c.Endpoint = "https://jellyseerr.example.invalid?x=1" },
		"fragment":            func(c *helpers.ExternalConnection) { c.Endpoint = "https://jellyseerr.example.invalid#x" },
		"bad port":            func(c *helpers.ExternalConnection) { c.Endpoint = "https://jellyseerr.example.invalid:99999" },
		"empty key":           func(c *helpers.ExternalConnection) { c.Credentials[0].Value = "" },
		"no key":              func(c *helpers.ExternalConnection) { c.Credentials = nil },
		"wrong credential":    func(c *helpers.ExternalConnection) { c.Credentials[0].Kind = "password" },
		"two keys":            func(c *helpers.ExternalConnection) { c.Credentials = append(c.Credentials, c.Credentials[0]) },
		"padded key":          func(c *helpers.ExternalConnection) { c.Credentials[0].Value = " key" },
		"selection supplied":  func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"node": "x"} },
		"empty endpoint host": func(c *helpers.ExternalConnection) { c.Endpoint = "https://" },
	} {
		t.Run(name, func(t *testing.T) {
			c := good
			c.Credentials = append([]helpers.ExternalCredential(nil), good.Credentials...)
			change(&c)
			if _, err := externalConnectionDescriptor(c); err == nil {
				t.Fatal("invalid connection accepted")
			}
		})
	}
}

func TestJellyseerrDescriptorIsExactStatsOnlyIntegration(t *testing.T) {
	descriptor, err := externalConnectionDescriptor(jellyseerrConnection("https://jellyseerr.example.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.kind != "jellyseerr" || !descriptor.integrationOnly || len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" {
		t.Fatalf("unexpected descriptor %+v", descriptor)
	}
	if got := strings.Join(descriptor.statsMetrics(), ","); got != "pending,approved,available,processing" {
		t.Fatalf("stats metrics %s", got)
	}
	secrets := descriptor.credentials(nil, nil)
	if len(secrets) != 1 || secrets[0].Kind != "apiKey" || secrets[0].Value != "fixture-jellyseerr-key" {
		t.Fatal("credential not forwarded as the single apiKey leaf")
	}
}

func TestJellyseerrPreflightProbesIdentityThenHoldsBeforeHomarr(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		change     func(*jellyseerrPeer)
		wantHold   bool
		wantManual bool
	}{
		{"valid identity reaches transport hold", nil, true, true},
		{"emby media server preserved", func(p *jellyseerrPeer) { p.mediaServer = float64(3) }, true, true},
		{"overseerr marker rejected", func(p *jellyseerrPeer) { p.api = "Overseerr API" }, false, true},
		{"missing marker rejected", func(p *jellyseerrPeer) { p.api = nil }, false, true},
		{"missing version rejected", func(p *jellyseerrPeer) { p.version = nil }, false, true},
		{"uninitialized never initialized", func(p *jellyseerrPeer) { p.initialized = false }, false, true},
		{"missing media server rejected", func(p *jellyseerrPeer) { p.mediaServer = nil }, false, true},
		{"rejected key", func(p *jellyseerrPeer) { p.meStatus = http.StatusForbidden }, false, true},
		{"non numeric account", func(p *jellyseerrPeer) { p.meID = "1" }, false, true},
		{"zero account", func(p *jellyseerrPeer) { p.meID = float64(0) }, false, true},
		{"fractional account", func(p *jellyseerrPeer) { p.meID = 1.5 }, false, true},
		{"redirect not followed", func(p *jellyseerrPeer) { p.redirect = "https://redirect.example.invalid/api/v1" }, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			peer := newJellyseerrPeer()
			if scenario.change != nil {
				scenario.change(peer)
			}
			srv := peer.server(t)
			descriptor, err := externalConnectionDescriptor(jellyseerrConnection(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			homarrRequests := 0
			homarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				homarrRequests++
				w.WriteHeader(http.StatusNotFound)
			}))
			t.Cleanup(homarr.Close)
			from := newStatsFixture().helpers(t)
			from.BaseURL, from.Config.HTTP = homarr.URL, srv.Client()
			err = descriptor.preflight(t.Context(), from, nil)
			if err == nil || errors.Is(err, helpers.ErrManual) != scenario.wantManual {
				t.Fatalf("preflight = %v", err)
			}
			if held := strings.Contains(err.Error(), "native fetch sets no redirect override"); held != scenario.wantHold {
				t.Fatalf("hold=%v want %v: %v", held, scenario.wantHold, err)
			}
			if homarrRequests != 0 {
				t.Fatalf("preflight reached Homarr %d times", homarrRequests)
			}
			for _, path := range peer.authed {
				if path != "/api/v1/auth/me" {
					t.Fatalf("API key sent to %s before identity was proven", path)
				}
			}
			if strings.Contains(err.Error(), "fixture-jellyseerr-key") {
				t.Fatal("error leaks the API key")
			}
		})
	}
}

func TestJellyseerrVerifyDataUsesOneSemanticStatsSample(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		change        func(*statsFixture, *jellyseerrPeer)
		wantError     bool
		wantManual    bool
		wantRefreshes int
	}{
		{"legitimate zeros", nil, false, false, 1},
		{"populated counts", func(f *statsFixture, p *jellyseerrPeer) {
			f.values = map[string]any{"pending": 3, "approved": 2, "available": 1, "processing": 1}
			p.counts = map[string]any{"total": 5, "movie": 3, "tv": 2, "pending": 3, "approved": 2, "declined": 0, "processing": 1, "available": 1}
		}, false, false, 1},
		{"peer count missing", func(_ *statsFixture, p *jellyseerrPeer) { delete(p.counts, "declined") }, true, true, 0},
		{"peer count null", func(_ *statsFixture, p *jellyseerrPeer) { p.counts["tv"] = nil }, true, true, 0},
		{"peer count string", func(_ *statsFixture, p *jellyseerrPeer) { p.counts["pending"] = "0" }, true, true, 0},
		{"peer count negative", func(_ *statsFixture, p *jellyseerrPeer) { p.counts["movie"] = -1 }, true, true, 0},
		{"peer count fractional", func(_ *statsFixture, p *jellyseerrPeer) { p.counts["total"] = 0.5 }, true, true, 0},
		{"peer count error status", func(_ *statsFixture, p *jellyseerrPeer) { p.countStatus = http.StatusInternalServerError }, true, true, 0},
		{"stats count missing", func(f *statsFixture, _ *jellyseerrPeer) { delete(f.values, "processing") }, true, false, 1},
		{"stats count null", func(f *statsFixture, _ *jellyseerrPeer) { f.values["available"] = nil }, true, false, 1},
		{"stats count negative", func(f *statsFixture, _ *jellyseerrPeer) { f.values["pending"] = -1 }, true, false, 1},
		{"stats count fractional", func(f *statsFixture, _ *jellyseerrPeer) { f.values["approved"] = 0.5 }, true, false, 1},
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
			peer := newJellyseerrPeer()
			if scenario.change != nil {
				scenario.change(f, peer)
			}
			srv := peer.server(t)
			descriptor, err := externalConnectionDescriptor(jellyseerrConnection(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			err = descriptor.verifyData(t.Context(), jellyseerrHomarr(t, f, srv), nil, "owned")
			if (err != nil) != scenario.wantError {
				t.Fatalf("verifyData error = %v, wantError %v", err, scenario.wantError)
			}
			if err != nil && errors.Is(err, helpers.ErrManual) != scenario.wantManual {
				t.Fatalf("Manual classification = %v: %v", errors.Is(err, helpers.ErrManual), err)
			}
			if f.refreshes != scenario.wantRefreshes || f.catalogs != scenario.wantRefreshes {
				t.Fatalf("refreshes=%d catalogs=%d want=%d", f.refreshes, f.catalogs, scenario.wantRefreshes)
			}
			if f.refreshes == 1 && !f.force {
				t.Fatal("Stats refresh was not forced")
			}
		})
	}
}
