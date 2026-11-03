package homarr

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type plantitFixtureOptions struct {
	identity    map[string]any
	proxyID     any
	proxyRefuse bool
	proxyOther  bool
}

func plantitVerificationFixture(t *testing.T, stats *statsFixture, options plantitFixtureOptions) (*helpers.H, *helpers.H) {
	t.Helper()
	statsHelper := stats.helpers(t)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/authentication/login":
			var login struct{ Username, Password string }
			json.NewDecoder(r.Body).Decode(&login)
			if login.Username != "self-host-homarr" || login.Password != "fixture-password" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "username": "self-host-homarr", "jwt": map[string]any{"value": "fixture-jwt"}})
		case "/api/user":
			if r.Header.Get("Authorization") != "Bearer fixture-jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "username": "self-host-homarr"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(peer.Close)
	parsed, err := url.Parse(peer.URL)
	if err != nil {
		t.Fatal(err)
	}
	to := helpers.New(helpers.Config{
		App: "plant-it", BaseURL: peer.URL, HTTP: peer.Client(), Log: events.Discard(),
		Inputs: map[string]string{"api-port": parsed.Port()},
		Secrets: statsSecrets{
			"PLANT_IT_HOMARR_USERNAME": "self-host-homarr", "PLANT_IT_HOMARR_PASSWORD": "fixture-password", "PLANT_IT_OWNER_ID": "7",
		},
	})
	identity := options.identity
	if identity == nil {
		identity = map[string]any{"id": "plant-owned", "kind": "plantit", "url": "http://plant-it:8080", "secrets": []any{
			map[string]any{"kind": "username", "value": "self-host-homarr"}, map[string]any{"kind": "password", "value": nil},
		}}
	}
	var proxyID any = float64(7)
	if options.proxyID != nil {
		proxyID = options.proxyID
	}
	homarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiKey") != "fixture-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.stats.") {
			request, err := http.NewRequestWithContext(r.Context(), r.Method, statsHelper.BaseURL+r.URL.RequestURI(), r.Body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			request.Header = r.Header.Clone()
			response, err := statsHelper.Config.HTTP.Do(request)
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			defer response.Body.Close()
			w.WriteHeader(response.StatusCode)
			io.Copy(w, response.Body)
			return
		}
		var value any
		switch r.URL.Path {
		case "/api/trpc/integration.byId":
			value = identity
		case "/api/trpc/integration.request":
			if options.proxyRefuse {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			integrationID := "plant-owned"
			if options.proxyOther {
				integrationID = "other"
			}
			value = map[string]any{
				"ok": true, "status": 200, "integration": map[string]any{"id": integrationID, "kind": "plantit"},
				"data": map[string]any{"id": proxyID},
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": value}}})
	}))
	t.Cleanup(homarr.Close)
	from := helpers.New(helpers.Config{App: "homarr", BaseURL: homarr.URL, HTTP: homarr.Client(), Log: events.Discard(), Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_LINKED_PLANT_IT": "plant-owned"}})
	return from, to
}

func plantitStatsFixture() *statsFixture {
	fixture := newStatsFixture()
	fixture.values = map[string]any{"plants": float64(5), "species": float64(3), "photos": float64(9), "events": float64(12)}
	fixture.metrics = []any{}
	for _, metric := range []string{"plants", "species", "photos", "events"} {
		fixture.metrics = append(fixture.metrics, map[string]any{"key": metric, "unit": "count"})
	}
	return fixture
}

func TestPlantitStatsVerification(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		change     func(*statsFixture, *plantitFixtureOptions)
		wantError  bool
		wantManual bool
	}{
		{"populated", nil, false, false},
		{"explicit zeros", func(f *statsFixture, _ *plantitFixtureOptions) {
			for key := range f.values {
				f.values[key] = float64(0)
			}
		}, false, false},
		{"negative", func(f *statsFixture, _ *plantitFixtureOptions) { f.values["plants"] = float64(-1) }, true, false},
		{"fractional", func(f *statsFixture, _ *plantitFixtureOptions) { f.values["photos"] = 1.5 }, true, false},
		{"null", func(f *statsFixture, _ *plantitFixtureOptions) { f.values["events"] = nil }, true, false},
		{"missing", func(f *statsFixture, _ *plantitFixtureOptions) { delete(f.values, "species") }, true, false},
		{"string", func(f *statsFixture, _ *plantitFixtureOptions) { f.values["plants"] = "5" }, true, false},
		{"species above plants", func(f *statsFixture, _ *plantitFixtureOptions) { f.values["species"] = float64(6) }, true, false},
		{"wrong units", func(f *statsFixture, _ *plantitFixtureOptions) { f.metrics[0].(map[string]any)["unit"] = "bytes" }, true, false},
		{"missing metric", func(f *statsFixture, _ *plantitFixtureOptions) { f.metrics = f.metrics[1:] }, true, false},
		{"no advancement", func(f *statsFixture, _ *plantitFixtureOptions) { f.advance = false }, true, false},
		{"wrong saved username", func(_ *statsFixture, o *plantitFixtureOptions) {
			o.identity = map[string]any{"id": "plant-owned", "kind": "plantit", "url": "http://plant-it:8080", "secrets": []any{
				map[string]any{"kind": "username", "value": "other"}, map[string]any{"kind": "password", "value": nil},
			}}
		}, true, true},
		{"plaintext password", func(_ *statsFixture, o *plantitFixtureOptions) {
			o.identity = map[string]any{"id": "plant-owned", "kind": "plantit", "url": "http://plant-it:8080", "secrets": []any{
				map[string]any{"kind": "username", "value": "self-host-homarr"}, map[string]any{"kind": "password", "value": "fixture-password"},
			}}
		}, true, true},
		{"wrong URL", func(_ *statsFixture, o *plantitFixtureOptions) {
			o.identity = map[string]any{"id": "plant-owned", "kind": "plantit", "url": "http://other:8080", "secrets": []any{
				map[string]any{"kind": "username", "value": "self-host-homarr"}, map[string]any{"kind": "password", "value": nil},
			}}
		}, true, true},
		{"wrong kind", func(_ *statsFixture, o *plantitFixtureOptions) {
			o.identity = map[string]any{"id": "plant-owned", "kind": "other", "url": "http://plant-it:8080", "secrets": []any{
				map[string]any{"kind": "username", "value": "self-host-homarr"}, map[string]any{"kind": "password", "value": nil},
			}}
		}, true, true},
		{"unowned ID", func(_ *statsFixture, o *plantitFixtureOptions) {
			o.identity = map[string]any{"id": "unowned", "kind": "plantit", "url": "http://plant-it:8080", "secrets": []any{
				map[string]any{"kind": "username", "value": "self-host-homarr"}, map[string]any{"kind": "password", "value": nil},
			}}
		}, true, true},
		{"proxied ID mismatch", func(_ *statsFixture, o *plantitFixtureOptions) { o.proxyID = float64(8) }, true, true},
		{"proxy refused", func(_ *statsFixture, o *plantitFixtureOptions) { o.proxyRefuse = true }, true, false},
		{"proxy for another integration", func(_ *statsFixture, o *plantitFixtureOptions) { o.proxyOther = true }, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := plantitStatsFixture()
			options := plantitFixtureOptions{}
			if scenario.change != nil {
				scenario.change(fixture, &options)
			}
			from, to := plantitVerificationFixture(t, fixture, options)
			err := plantitTarget().verifyData(t.Context(), from, to, "plant-owned")
			if (err != nil) != scenario.wantError || errors.Is(err, helpers.ErrManual) != scenario.wantManual {
				t.Fatalf("verifyData error=%v wantError=%v wantManual=%v", err, scenario.wantError, scenario.wantManual)
			}
		})
	}
}

func TestPlantitTargetShape(t *testing.T) {
	descriptor := plantitTarget()
	if descriptor.id != "plant-it" || descriptor.kind != "plantit" || descriptor.internal(nil) != "http://plant-it:8080" {
		t.Fatalf("unexpected identity %q %q", descriptor.id, descriptor.kind)
	}
	metrics := descriptor.statsMetrics()
	if strings.Join(metrics, ",") != "plants,species,photos,events" {
		t.Fatalf("unexpected metrics %v", metrics)
	}
	_, to := plantitVerificationFixture(t, plantitStatsFixture(), plantitFixtureOptions{})
	credentials := descriptor.secrets(nil, to)
	if len(credentials) != 2 || credentials[0].Value != "self-host-homarr" || credentials[1].Value != "fixture-password" || credentials[1].Value == to.AdminPassword() {
		t.Fatal("Homarr must receive the saved owner login")
	}
}

func TestPlantitPreflightRequiresRecordedOwner(t *testing.T) {
	from, to := plantitVerificationFixture(t, plantitStatsFixture(), plantitFixtureOptions{})
	if err := plantitTarget().preflight(t.Context(), from, to); err != nil {
		t.Fatalf("preflight rejected the recorded owner: %v", err)
	}
	to.SetSecret(to.Key("homarr_password"), "rejected-password")
	if err := plantitTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("preflight must hold Manual on a rejected owner, got %v", err)
	}
	to.SetSecret(to.Key("owner_id"), "")
	if err := plantitTarget().preflight(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("preflight must hold Manual with no recorded owner, got %v", err)
	}
}
