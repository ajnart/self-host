package bazarr_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/arrtest"
	"github.com/ajnart/self-host/internal/apps/bazarr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/clitest"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func libraryInstallFixture(t *testing.T) (*arrtest.Env, *libraryFixture) {
	t.Helper()
	f := newLibraryFixture(t)
	e := arrtest.New(t, catalog.New([]app.App{bazarr.App{}, arr.Sonarr(), arr.Radarr()}, nil))
	local := (&fakeBazarr{}).handler()
	f.bazarrResponse = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/" || r.URL.Path == "/api/system/status" || r.URL.Path == "/api/badges" {
			local.ServeHTTP(w, r)
			return true
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/system/settings" {
			r.ParseForm()
			if r.PostForm.Has("settings-auth-type") {
				local.ServeHTTP(w, r)
				return true
			}
		}
		return false
	}
	e.Serve("bazarr", f.handler)
	for id, peer := range map[string]*arrtest.Arr{"sonarr": e.Sonarr, "radarr": e.Radarr} {
		handler := peer.Handler()
		e.Serve(id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.Header.Get("X-Api-Key") != peer.Key {
				handler.ServeHTTP(w, r)
				return
			}
			switch r.URL.Path {
			case "/api/v3/system/status":
				json.NewEncoder(w).Encode(map[string]any{"appName": id, "version": "4.0.0", "urlBase": ""})
			case "/api/v3/rootfolder":
				folders := []map[string]any{}
				for index, root := range peer.RootFolders() {
					folders = append(folders, map[string]any{"id": index + 1, "path": root, "accessible": true})
				}
				json.NewEncoder(w).Encode(folders)
			case "/api/v3/series", "/api/v3/movie":
				json.NewEncoder(w).Encode([]any{})
			default:
				handler.ServeHTTP(w, r)
			}
		}))
	}
	config := filepath.Join(e.AppDir("bazarr"), "config", "config")
	if err := os.MkdirAll(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.yaml"), []byte("auth:\n  apikey: "+apiKey+"\n  type: null\n  username: \"\"\n  password: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bazarr", "sonarr", "radarr"} {
		prefix := "docker compose --project-directory " + clitest.Root(t, e.Deps) + " -p selfhost exec -T " + id + " s6-setuidgid abc sh -eu -c [ -d \"$1\" ]"
		e.Deps.Runner.(*systemtest.Runner).On(prefix, system.Result{Stdout: []byte("2049:12345\n")})
	}
	return e, f
}

func TestBazarrLibraryLinksUseProductionSchedulingInBothInstallationOrders(t *testing.T) {
	for _, id := range []string{"sonarr", "radarr"} {
		for _, consumerFirst := range []bool{false, true} {
			t.Run(id+"/consumer-first="+map[bool]string{true: "yes", false: "no"}[consumerFirst], func(t *testing.T) {
				e, f := libraryInstallFixture(t)
				first, second := id, "bazarr"
				if consumerFirst {
					first, second = "bazarr", id
				}
				initial := e.Run("install", first, "--yes", "--json")
				if initial.Code != 0 || len(f.posts) != 0 {
					t.Fatalf("absent targets must not run a Bazarr Link; code=%d events=%v", initial.Code, initial.Events)
				}
				result := e.Run("install", second, "--yes", "--json")
				if result.StepEvent("bazarr", "link-"+id) != "step.manual" || len(f.posts) != 1 {
					t.Fatalf("production scheduler did not record the configured-but-unverified consumer Link: code=%d events=%v", result.Code, result.Events)
				}
				if f.posts[0]["settings-"+id+"-apikey"] != "key-"+id {
					t.Fatal("Bazarr did not consume the target's production Setup credential")
				}
				rerun := e.Run("setup", "bazarr", "--json")
				if len(f.posts) != 1 || rerun.StepEvent("bazarr", "link-"+id) != "step.manual" {
					t.Fatal("rerun changed settings or falsely claimed complete native acceptance")
				}
				removed := e.Run("remove", id, "--yes", "--json")
				if removed.StepEvent("bazarr", "unlink-"+id) != "step.ok" || len(f.posts) != 2 {
					t.Fatal("target removal did not restore its exact owned singleton fields")
				}
			})
		}
	}
}

func TestBazarrUnreadyTargetWaitsOnConsumerAndOtherTargetStillConfigures(t *testing.T) {
	e, f := libraryInstallFixture(t)
	e.Sonarr.RequireRestart = true
	e.Deps.Runner.(*systemtest.Runner).OnFunc("docker compose --project-directory "+clitest.Root(t, e.Deps)+" -p selfhost restart sonarr", func(system.Cmd) system.Result { return system.Result{ExitCode: 1} })
	result := e.Run("install", "bazarr", "sonarr", "radarr", "--yes", "--json")
	wait := result.Step("bazarr", "link-sonarr")
	if wait == nil || wait["event"] != "step.skip" || !strings.Contains(wait["msg"].(string), "finish Setup") {
		t.Fatal("unready target should produce an actionable consumer wait")
	}
	if result.StepEvent("bazarr", "link-radarr") != "step.manual" || len(f.posts) != 1 || f.posts[0]["settings-radarr-ip"] != "radarr" {
		t.Fatalf("one unready target blocked the independent other connection: code=%d events=%v", result.Code, result.Events)
	}
}

func TestBazarrProductionSetupRecoversRemovedTargetWithoutBlockingOtherLink(t *testing.T) {
	e, f := libraryInstallFixture(t)
	_ = e.Run("install", "bazarr", "sonarr", "radarr", "--yes", "--json")
	if len(f.posts) != 2 {
		t.Fatal("both disabled connections should be recorded")
	}
	f.settings["general"].(map[string]any)["use_sonarr"] = true
	removed := e.Run("remove", "sonarr", "--yes", "--json")
	if removed.StepEvent("bazarr", "unlink-sonarr") != "step.manual" || len(f.posts) != 2 {
		t.Fatal("active removal must not restart a configured backend")
	}
	stillActive := e.Run("setup", "bazarr", "--json")
	if stillActive.StepEvent("bazarr", "unlink-sonarr") != "step.manual" || stillActive.StepEvent("bazarr", "link-radarr") != "step.manual" || len(f.posts) != 2 {
		t.Fatal("recorded removal should remain visible without blocking the other installed target")
	}
	f.settings["general"].(map[string]any)["use_sonarr"] = false
	recovered := e.Run("setup", "bazarr", "--json")
	if recovered.StepEvent("bazarr", "unlink-sonarr") != "step.ok" || recovered.StepEvent("bazarr", "link-sonarr") != "" || recovered.StepEvent("bazarr", "link-radarr") != "step.manual" || len(f.posts) != 3 {
		t.Fatal("disabled recorded cleanup should finish without scheduling an absent target or changing the survivor")
	}
}
