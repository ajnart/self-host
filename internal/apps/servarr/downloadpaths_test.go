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
	paths          map[string]string
	backing        map[string]string
	mounts         string
	calls          int
	identity       downloadPathIdentity
	identityOutput string
	result         *system.Result
	err            error
}

func (e *servarrPathExecutor) Exec(_ context.Context, service string, _ map[string]string, args ...string) (system.Result, error) {
	e.calls++
	if len(args) != 8 || args[3] != "-eu" || args[4] != "-c" || args[6] != "self-host-download-path" {
		return system.Result{}, errors.New("unexpected path probe")
	}
	identityOutput := ""
	if e.identity == downloadPathSuExecUserUsers {
		if strings.Join(args[:3], " ") != "su-exec user:users sh" || !strings.HasSuffix(args[5], downloadDirectoryProbe) {
			return system.Result{}, errors.New("unexpected su-exec path probe")
		}
		for _, required := range []string{`test "$(id -u)" -ne 0`, `test "$(id -g)" -ne 0`, "id -un\n", "id -gn\n"} {
			if !strings.Contains(args[5], required) {
				return system.Result{}, errors.New("missing App-user identity check")
			}
		}
		identityOutput = "user\nusers\n"
		if e.identityOutput != "" {
			identityOutput = e.identityOutput
		}
	} else if strings.Join(args[:3], " ") != "s6-setuidgid abc sh" || args[5] != downloadDirectoryProbe {
		return system.Result{}, errors.New("unexpected s6 path probe")
	}
	if e.result != nil || e.err != nil {
		if e.result == nil {
			return system.Result{}, e.err
		}
		return *e.result, e.err
	}
	value, ok := e.paths[service+":"+args[7]]
	if !ok {
		return system.Result{ExitCode: 1}, nil
	}
	canonical := strings.Split(value, "\n")[0]
	root := "/shared/downloads"
	if strings.Contains(value, "1:99") {
		root = "/shared/library"
	}
	if custom := e.backing[service+":"+args[7]]; custom != "" {
		root = custom
	}
	return system.Result{Stdout: []byte(identityOutput + value + "1 0 0:5 " + root + " " + canonical + " rw - ext4 /dev/fixture rw\n" + e.mounts)}, nil
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
			exec := &servarrPathExecutor{paths: map[string]string{"contract:" + remote: remote + "\n1:42\n", "sonarr:" + local: local + "\n1:42\n", "sonarr:" + library: library + "\n1:99\n"}, backing: map[string]string{}}
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
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); err != nil {
				t.Fatal(err)
			}
			exec.mounts = "2 1 0:6 /nested " + remote + "/nested rw - ext4 /dev/nested rw\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("nested mount was accepted without review", err)
			}
			exec.mounts = "2 1 0:5 /stacked " + remote + " rw - ext4 /dev/stacked rw\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("stacked mount was accepted without review", err)
			}
			exec.mounts = ""
			exec.paths["sonarr:"+library] = library + "\n1:42\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("aliased library directory accepted", err)
			}
			exec.paths["sonarr:"+library] = library + "\n1:99\n"
			exec.backing["sonarr:"+library] = "/shared"
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("library mounted as a backing ancestor was accepted", err)
			}
			delete(exec.backing, "sonarr:"+library)
			exec.paths["sonarr:"+local] = local + "\n1:77\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("separate App-local storage accepted", err)
			}
			exec.paths["sonarr:"+local] = library + "\n1:42\n"
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("canonical library overlap accepted", err)
			}
			delete(exec.paths, "contract:"+remote)
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, remote); !errors.Is(err, helpers.ErrManual) {
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
			if err := p.verifyDownloadPath(context.Background(), from, to, to.Service, "/data/complete"); !errors.Is(err, helpers.ErrManual) {
				t.Fatal(err)
			}
		})
	}
}

func TestDownloadMappingMatchesSelectedHostWithoutDNSEquivalence(t *testing.T) {
	for _, scenario := range []struct {
		mappingHost, selectedHost string
		mapped                    bool
	}{
		{"selected-sabnzbd", "selected-sabnzbd", true},
		{"SELECTED-SABNZBD", "selected-sabnzbd", true},
		{"selected-sabnzbd.", "selected-sabnzbd", false},
		{"selected-sabnzbd", "selected-sabnzbd.", false},
		{"SELECTED-SABNZBD.", "selected-sabnzbd.", true},
		{"sabnzbd", "selected-sabnzbd", false},
	} {
		t.Run(scenario.mappingHost+"/"+scenario.selectedHost, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "host": scenario.mappingHost, "remotePath": "/remote", "localPath": "/local"}})
			}))
			t.Cleanup(server.Close)
			from := porttest.New(t, porttest.Options{App: "sonarr", Secrets: porttest.Secrets{}}, server, &porttest.Recorder{})
			p := providerLink{api: V3, target: "sabnzbd", name: "SABnzbd"}
			got, err := p.mappedDownloadPath(context.Background(), from, scenario.selectedHost, "/remote/tv")
			want := "/remote/tv"
			if scenario.mapped {
				want = "/local/tv"
			}
			if err != nil || got != want {
				t.Fatalf("mapped path %q, want %q: %v", got, want, err)
			}
		})
	}
}

func TestDownloadPathsSuExecTargetKeepsS6ConsumersAndReadOnlySemantics(t *testing.T) {
	for _, scenario := range []string{"shared", "mapped", "missing-executor", "missing-binary", "wrapper-error", "wrong-user", "wrong-group", "root-uid", "root-gid", "missing-identity", "unsupported-mechanism", "unsupported-mapping", "different-directory", "overlap"} {
		t.Run(scenario, func(t *testing.T) {
			completed, local, library := "/internal/downloads/tv", "/internal/downloads/tv", "/internal/media/tv"
			mappings := []map[string]any{}
			if scenario == "mapped" {
				local = "/downloads/tv"
				mappings = append(mappings, map[string]any{"id": 1, "host": "selected-downloader", "remotePath": "/internal/downloads", "localPath": "/downloads"})
			}
			if scenario == "unsupported-mapping" {
				mappings = append(mappings, map[string]any{"id": 1, "host": "selected-downloader", "remotePath": "/internal/../internal", "localPath": "/downloads"})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("X-Api-Key") != "consumer-key" {
					t.Error("path verification must remain authenticated and read-only")
					w.WriteHeader(400)
					return
				}
				switch r.URL.Path {
				case V3 + "/rootfolder":
					json.NewEncoder(w).Encode([]map[string]any{{"id": 1, "path": library}})
				case V3 + "/remotepathmapping":
					json.NewEncoder(w).Encode(mappings)
				default:
					t.Error("unexpected path-verifier request")
					w.WriteHeader(400)
				}
			}))
			t.Cleanup(server.Close)
			secrets := porttest.Secrets{"SONARR_API_KEY": "consumer-key"}
			from := porttest.New(t, porttest.Options{App: "sonarr", Service: "selected-arr", Secrets: secrets}, server, &porttest.Recorder{})
			consumerExec := &servarrPathExecutor{paths: map[string]string{"selected-arr:" + local: local + "\n1:42\n", "selected-arr:" + library: library + "\n1:99\n"}}
			targetExec := &servarrPathExecutor{identity: downloadPathSuExecUserUsers, paths: map[string]string{"selected-downloader:" + completed: completed + "\n1:42\n"}}
			from.Config.Exec = consumerExec
			to := helpers.New(helpers.Config{App: "compiled-provider", Service: "selected-downloader", Exec: targetExec, Secrets: secrets, Log: from.Log})
			identity := downloadPathSuExecUserUsers
			switch scenario {
			case "missing-executor":
				to.Config.Exec = nil
			case "missing-binary":
				targetExec.result = &system.Result{ExitCode: 127}
			case "wrapper-error":
				targetExec.err = errors.New("private wrapper failure must not escape")
			case "wrong-user":
				targetExec.identityOutput = "root\nusers\n"
			case "wrong-group":
				targetExec.identityOutput = "user\nroot\n"
			case "root-uid", "root-gid":
				targetExec.result = &system.Result{ExitCode: 1}
			case "missing-identity":
				targetExec.identityOutput = "\n"
			case "unsupported-mechanism":
				identity = downloadPathIdentity(255)
			case "different-directory":
				targetExec.paths["selected-downloader:"+completed] = completed + "\n1:77\n"
			case "overlap":
				consumerExec.backing = map[string]string{"selected-arr:" + library: "/shared"}
			}
			p := providerLink{api: V3, target: "compiled-provider", name: "Compiled provider"}
			err := p.verifyDownloadPathAs(context.Background(), from, to, to.Service, completed, identity)
			if len(secrets) != 1 || secrets["SONARR_API_KEY"] != "consumer-key" {
				t.Fatal("path verifier wrote private state")
			}
			if scenario == "shared" || scenario == "mapped" {
				if err != nil || targetExec.calls != 1 || consumerExec.calls != 2 {
					t.Fatalf("separate target/consumer identities were not verified: target=%d consumer=%d err=%v", targetExec.calls, consumerExec.calls, err)
				}
				return
			}
			if !errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "private wrapper failure") {
				t.Fatal("unsupported identity/storage did not produce fixed Manual recovery", err)
			}
			if targetExec.calls > 1 || (scenario == "unsupported-mechanism" || scenario == "missing-executor" || scenario == "unsupported-mapping") && targetExec.calls != 0 {
				t.Fatal("unsupported identity triggered a fallback executor")
			}
			if scenario != "different-directory" && scenario != "overlap" && consumerExec.calls != 0 {
				t.Fatal("unsupported target identity reached consumer probes")
			}
		})
	}
}
