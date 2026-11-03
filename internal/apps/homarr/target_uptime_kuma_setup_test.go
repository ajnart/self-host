package homarr_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ajnart/self-host/internal/apps/homarr"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

func TestUptimeKumaReviewCompleteSetupStopsBeforeTileWrites(t *testing.T) {
	for _, scenario := range []string{"unsupported native contract", "first integration install", "first Homarr install", "missing peer helper"} {
		t.Run(scenario, func(t *testing.T) {
			fake := newFake()
			fake.version, fake.apiKey, fake.adminExists, fake.adminName, fake.adminPassword, fake.step = "2.10.1", "keyid.secret", true, "admin", "pw", "finish"
			if scenario == "first Homarr install" {
				fake.apiKey, fake.adminExists, fake.step = "", false, "start"
			}
			var writes atomic.Int64
			next := fake.handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && (strings.HasPrefix(r.URL.Path, "/api/apps") || strings.HasPrefix(r.URL.Path, "/api/boards") || strings.HasPrefix(r.URL.Path, "/api/trpc/board.") || strings.HasPrefix(r.URL.Path, "/api/trpc/integration.") || strings.HasPrefix(r.URL.Path, "/api/trpc/onboard.")) {
					writes.Add(1)
				}
				if r.URL.Path == "/api/trpc/integration.getKinds" {
					trpc(w, []any{map[string]any{"kind": "uptimeKuma", "requiredSecrets": [][]string{{"slug"}}}})
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/trpc/widget.uptimeKuma.") {
					if scenario == "unsupported native contract" {
						w.WriteHeader(http.StatusNotFound)
						w.Write([]byte(`{"error":{"json":{"code":-32004,"message":"unknown native contract"}}}`))
						return
					}
					trpc(w, []any{})
					return
				}
				next.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/heartbeat/") {
					w.Write([]byte(`{"heartbeatList":{},"uptimeList":{}}`))
					return
				}
				w.Write([]byte(`{"config":{"slug":"chosen-page"},"publicGroupList":[]}`))
			}))
			t.Cleanup(peer.Close)
			h := homarrH(t, server, allSecrets())
			if scenario == "first Homarr install" {
				h.Secrets.Set("HOMARR_API_KEY", "")
			}
			h.Peer = func(id string) *helpers.H {
				if id != "uptime-kuma" || scenario == "missing peer helper" {
					return nil
				}
				return helpers.New(helpers.Config{App: id, BaseURL: peer.URL, HTTP: peer.Client(), Log: events.Discard(), Inputs: map[string]string{"homarr_status_page": "chosen-page"}})
			}
			h.InstalledApps = func(context.Context) ([]helpers.InstalledApp, error) {
				return []helpers.InstalledApp{{ID: "uptime-kuma", Name: "Uptime Kuma", URL: peer.URL, Service: "uptime-kuma", ContainerPort: 3001}}, nil
			}
			err := (homarr.App{}).Setup(t.Context(), h)
			if !errors.Is(err, helpers.ErrManual) || writes.Load() != 0 {
				t.Fatalf("complete Setup did not stop before tiles/boards: err=%v writes=%d", err, writes.Load())
			}
		})
	}
}

func TestUptimeKumaReviewAbsentPeerDoesNotGateSetup(t *testing.T) {
	fake := newFake()
	fake.apiKey, fake.adminExists, fake.adminName, fake.adminPassword, fake.step = "keyid.secret", true, "admin", "pw", "finish"
	server := serve(t, fake)
	h := homarrH(t, server, allSecrets())
	h.InstalledApps = func(context.Context) ([]helpers.InstalledApp, error) { return []helpers.InstalledApp{}, nil }
	h.Peer = func(string) *helpers.H {
		t.Error("absent Kuma was resolved for admission")
		return nil
	}
	if err := h.SetSecret(h.Key("linked_uptime-kuma"), "previous-owned-id"); err != nil {
		t.Fatal(err)
	}
	if err := h.SetSecret(h.Key("widgets_previous-owned-id"), "{}"); err != nil {
		t.Fatal(err)
	}
	if err := (homarr.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
}
