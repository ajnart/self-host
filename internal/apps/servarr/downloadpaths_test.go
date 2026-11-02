package servarr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/porttest"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/system"
)

type servarrPathExecutor struct {
	helpers.Executor
	paths map[string]string
	calls int
}

func (e *servarrPathExecutor) Exec(_ context.Context, service string, _ map[string]string, args ...string) (system.Result, error) {
	e.calls++
	if len(args) != 8 || strings.Join(args[:3], " ") != "s6-setuidgid abc sh" || args[5] != downloadDirectoryProbe {
		return system.Result{}, errors.New("unexpected path probe")
	}
	value, ok := e.paths[service+":"+args[7]]
	if !ok {
		return system.Result{ExitCode: 1}, nil
	}
	return system.Result{Stdout: []byte(value)}, nil
}

func TestDownloadPathsVerifySharedAndExistingLegacyMappings(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "legacy"}[legacy], func(t *testing.T) {
			remote, local, library := "/data/usenet/complete/tv", "/data/usenet/complete/tv", "/data/media/tv"
			mappings := []map[string]any{}
			if legacy {
				remote, local, library = "/data/complete/tv", "/downloads/sab/tv", "/media/tv"
				mappings = append(mappings, map[string]any{"id": 1, "host": "contract", "remotePath": "/data/complete", "localPath": "/downloads/sab"})
			}
			exec := &servarrPathExecutor{paths: map[string]string{"contract:" + remote: remote + "\n1:42\n", "sonarr:" + local: local + "\n1:42\n", "sonarr:" + library: library + "\n1:99\n"}}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v3/rootfolder", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "path": library}})
			})
			mux.HandleFunc("GET /api/v3/remotepathmapping", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(mappings) })
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			from := porttest.New(t, porttest.Options{App: "sonarr", Service: "sonarr", Secrets: porttest.Secrets{}}, server, &porttest.Recorder{})
			from.Config.Exec = exec
			to := helpers.New(helpers.Config{App: "contract", Service: "contract", Exec: exec, Log: from.Log})
			p := providerLink{api: V3, target: "contract", name: "Contract"}
			if err := p.verifyDownloadPath(context.Background(), from, to, remote); err != nil {
				t.Fatal(err)
			}
			exec.paths["sonarr:"+local] = local + "\n1:77\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("separate App-local storage accepted", err)
			}
			exec.paths["sonarr:"+local] = library + "\n1:42\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("canonical library overlap accepted", err)
			}
			delete(exec.paths, "contract:"+remote)
			if err := p.verifyDownloadPath(context.Background(), from, to, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("unreadable target path accepted", err)
			}
		})
	}
}

func TestDownloadPathsRejectMissingRootsAmbiguousMappingsAndTraversal(t *testing.T) {
	for _, scenario := range []string{"roots", "ambiguous", "traversal", "prefix"} {
		t.Run(scenario, func(t *testing.T) {
			roots := []map[string]any{{"id": 1, "path": "/media/tv"}}
			mappings := []map[string]any{}
			if scenario == "roots" {
				roots = []map[string]any{}
			}
			if scenario == "ambiguous" {
				mappings = []map[string]any{{"id": 1, "host": "contract", "remotePath": "/data", "localPath": "/downloads"}, {"id": 2, "host": "contract", "remotePath": "/data/complete", "localPath": "/elsewhere"}}
			}
			if scenario == "traversal" {
				mappings = []map[string]any{{"id": 1, "host": "contract", "remotePath": "/data/../data", "localPath": "/downloads"}}
			}
			if scenario == "prefix" {
				mappings = []map[string]any{{"id": 1, "host": "contract", "remotePath": "/data/com", "localPath": "/downloads"}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Error("path verifier mutated configuration")
				}
				if strings.HasSuffix(r.URL.Path, "rootfolder") {
					json.NewEncoder(w).Encode(roots)
					return
				}
				json.NewEncoder(w).Encode(mappings)
			}))
			t.Cleanup(server.Close)
			from := porttest.New(t, porttest.Options{App: "lidarr", Service: "lidarr", Secrets: porttest.Secrets{}}, server, &porttest.Recorder{})
			from.Config.Exec = &servarrPathExecutor{paths: map[string]string{}}
			to := helpers.New(helpers.Config{App: "contract", Service: "contract", Log: from.Log})
			p := providerLink{api: V1, target: "contract", name: "Contract"}
			if err := p.verifyDownloadPath(context.Background(), from, to, "/data/complete"); !errors.Is(err, helpers.ErrManual) {
				t.Fatal(err)
			}
		})
	}
}
