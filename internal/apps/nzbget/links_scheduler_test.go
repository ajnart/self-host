package nzbget_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/lidarr"
	"github.com/ajnart/self-host/internal/apps/nzbget"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/system"
)

type nzbgetScheduledConsumer struct {
	app.App
	nzbgetLink app.Link
}

func (a nzbgetScheduledConsumer) Links() []app.Link {
	return append(a.App.Links(), a.nzbgetLink)
}

type nzbgetScheduledRunner struct {
	system.Runner
	paths *nzbgetImageIdentityFixture
}

func (r nzbgetScheduledRunner) Run(ctx context.Context, cmd system.Cmd) (system.Result, error) {
	for index, arg := range cmd.Args {
		if arg == "self-host-download-path" && index >= 7 {
			return r.paths.Exec(ctx, cmd.Args[index-7], nil, cmd.Args[index-6:]...)
		}
	}
	return r.Runner.Run(ctx, cmd)
}

func TestNZBGetProductionSchedulerBothOrdersUnreadyAndOwnedRemoval(t *testing.T) {
	for index, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		for _, order := range []string{"consumer-first", "target-first", "unready-target"} {
			t.Run(consumer.Info().ID+"/"+order, func(t *testing.T) {
				f := newNZBGetLinkFixture(t, nzbgetConsumerCases[index])
				f.pathProbe.supported = true
				id := consumer.Info().ID
				assembled := nzbgetScheduledConsumer{App: consumer, nzbgetLink: f.link}
				e := arrtest.New(t, catalog.New([]app.App{assembled, nzbget.App{}}, nil))
				e.Serve(id, f.handler)
				f.config("InterDir", "/data/usenet/incomplete")
				f.config("UMask", "022")
				active, _ := f.targetSettings.Load("config")
				f.targetSettings.Store("loadconfig", active)
				f.targetSettings.Store("allow_setup", true)
				if order == "unready-target" {
					f.targetSettings.Store("version", "unready")
				}
				handler, _ := f.targetSettings.Load("rpc_handler")
				e.Serve("nzbget", handler.(http.Handler))
				e.Deps.Runner = nzbgetScheduledRunner{Runner: e.Deps.Runner, paths: f.pathProbe}
				getenv := e.Deps.Getenv
				e.Deps.Getenv = func(key string) string {
					if key == "SELF_HOST_ADMIN_PASSWORD" {
						return password
					}
					return getenv(key)
				}
				for _, appID := range []string{"nzbget", "lidarr"} {
					if appID == "lidarr" && id != "lidarr" {
						continue
					}
					directory := filepath.Join(e.AppDir(appID), "config")
					if err := os.MkdirAll(directory, 0700); err != nil {
						t.Fatal(err)
					}
					name, content := "kept-data", "owned-test-data"
					if appID == "lidarr" {
						name, content = "config.xml", "<Config><ApiKey>key-lidarr</ApiKey></Config>"
					}
					if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				f.backend.SeedDownloadClient(map[string]any{"name": "Independent qBittorrent", "implementation": "QBittorrent", "enable": true, "fields": []any{map[string]any{"name": "host", "value": "qbittorrent"}}})
				survivor := f.backend.DownloadClients()[0]["id"]
				if order == "consumer-first" {
					result := e.Run("install", id, "--yes", "--json", "--exposure", "local")
					if result.Code != 0 || result.Step(id, "link-nzbget") != nil {
						t.Fatal("absent target was scheduled", result.Out)
					}
					result = e.Run("install", "nzbget", "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
				} else {
					result := e.Run("install", "nzbget", "--yes", "--json", "--exposure", "local")
					if order == "target-first" && result.Code != 0 {
						t.Fatal(result.Out)
					}
					result = e.Run("install", id, "--yes", "--json")
					if result.Code != 0 {
						t.Fatal(result.Out)
					}
					if order == "unready-target" {
						step := result.Step(id, "link-nzbget")
						if step == nil || !strings.Contains(step["msg"].(string), "waiting for nzbget") || f.nativeTests != 0 {
							t.Fatal("unready target did not retain a consumer-owned wait", result.Out)
						}
						f.targetSettings.Delete("version")
						result = e.Run("setup", "nzbget", "--json")
						if result.Code != 0 {
							t.Fatal(result.Out)
						}
					}
				}
				if len(f.backend.DownloadClients()) != 2 || f.nativeTests < 2 || f.pathProbe.wrongIdentity != 0 {
					t.Fatal("production scheduler did not run the guarded real NZBGet leaf")
				}
				for _, client := range f.backend.DownloadClients() {
					if client["implementation"] == "Nzbget" && client["protocol"] != nzbgetConsumerCases[index].protocol {
						t.Fatal("production scheduler normalized the consumer's protocol")
					}
				}
				requests, native := f.rpcRequests(), f.nativeTests
				result := e.Run("remove", "nzbget", "--yes", "--json")
				items := f.backend.DownloadClients()
				if result.Code != 0 || len(items) != 1 || items[0]["id"] != survivor || f.rpcRequests() != requests || f.nativeTests != native {
					t.Fatal("production unlink changed its survivor or queried target prerequisites", result.Out)
				}
				if _, err := os.Stat(filepath.Join(e.AppDir("nzbget"), "config", "kept-data")); err != nil {
					t.Fatal("non-purge removal lost synthetic target data", err)
				}
			})
		}
	}
}
