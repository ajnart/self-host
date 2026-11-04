package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/maintainerr"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func maintainerrDirectoryHTTP(t *testing.T, h *harness, configured bool) func() int {
	t.Helper()
	requests := 0
	h.deps.HTTP = &http.Client{Transport: tailscaleHTTP(func(r *http.Request) (*http.Response, error) {
		requests++
		responses := map[string]string{
			"/api/health/ready":           `{"status":"ok","database":"ok"}`,
			"/api/settings/test/setup":    strconv.FormatBool(configured),
			"/api/media-server/type":      `{"type":"jellyfin"}`,
			"/api/media-server":           `{"machineId":"synthetic-server","version":"1.2.3"}`,
			"/api/media-server/users":     `[{"id":"synthetic-owner"}]`,
			"/api/media-server/libraries": `[]`,
		}
		body, ok := responses[r.URL.Path]
		if !ok || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Errorf("unexpected consumer request: %s %s", r.Method, r.URL.Path)
			return &http.Response{StatusCode: 404, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	return func() int { return requests }
}

func TestMaintainerrDirectoryMissingInstallReachesSharedNativeAdmission(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this case checks the unsupported native non-Linux ACL boundary")
	}
	h := newDirectoryHarness(t, maintainerr.App{})
	requests := maintainerrDirectoryHTTP(t, h, true)
	if err := h.install("maintainerr"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("unsupported native storage did not remain Manual: %v", err)
	}
	if e := h.rec.find("maintainerr", "directories"); e == nil || e.Event != events.StepManual || !strings.Contains(e.Hint, "ACL") {
		t.Fatal("fresh Install did not reach shared native directory admission")
	}
	if _, err := os.Lstat(filepath.Join(h.root(), "apps", "maintainerr", "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("early preflight created or adopted missing storage")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
	if requests() != 0 {
		t.Fatal("native storage refusal reached HTTP")
	}
}

func TestMaintainerrDirectoryFreshInstallPreparesBeforeFirstUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, configured := range []bool{true, false} {
		t.Run("configured="+strconv.FormatBool(configured), func(t *testing.T) {
			h := newDirectoryHarness(t, maintainerr.App{})
			requests := maintainerrDirectoryHTTP(t, h, configured)
			data := filepath.Join(h.root(), "apps", "maintainerr", "data")
			if _, err := os.Lstat(filepath.Dir(data)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fresh Install fixture already has an App namespace")
			}
			starts := 0
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), "up -d") {
					starts++
					info, err := os.Stat(data)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Fatal("first Up preceded private data preparation")
					}
					owner := info.Sys().(*syscall.Stat_t)
					p, _ := h.svc.Project()
					settings, err := p.LoadSettings()
					if err != nil || settings["PUID"] != strconv.Itoa(int(owner.Uid)) || settings["PGID"] != strconv.Itoa(int(owner.Gid)) {
						t.Fatal("prepared ownership does not match resolved runtime identity")
					}
					if event := h.rec.find("maintainerr", "directories"); event == nil || event.Event != events.StepOK || requests() != 0 {
						t.Fatal("first Up did not follow shared preparation before any HTTP")
					}
				}
				return system.Result{}
			})
			err := h.install("maintainerr")
			if configured && err != nil || !configured && !errors.Is(err, ErrManual) {
				t.Fatalf("unexpected configured-server admission: %v", err)
			}
			if starts != 1 || requests() == 0 {
				t.Fatalf("fresh Install did not reach startup then backend verification: starts=%d, requests=%d", starts, requests())
			}
			if !configured {
				if event := h.rec.find("maintainerr", "media-server"); event == nil || event.Event != events.StepManual || !strings.Contains(event.Hint, "Services") {
					t.Fatal("unconfigured backend was not held separately from directory admission")
				}
			}
			if _, err := os.Lstat(filepath.Join(data, ".keep")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fresh Install manufactured a sentinel file")
			}
		})
	}
}

func TestMaintainerrDirectoryInstallPreservesPrivateDataAndOldMarker(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	h := newDirectoryHarness(t, maintainerr.App{})
	maintainerrDirectoryHTTP(t, h, true)
	data := filepath.Join(h.root(), "apps", "maintainerr", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(data, ".keep")
	if err := os.WriteFile(marker, []byte("operator-owned marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(data)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := h.install("maintainerr"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(data)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
		t.Fatal("Install changed existing private directory metadata")
	}
	if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "operator-owned marker" {
		t.Fatal("Install removed or rewrote an old marker")
	}
	if e := h.rec.find("maintainerr", "directories"); e == nil || e.Event != events.StepOK {
		t.Fatal("compatible Install did not reach the shared declaration action")
	}
}

func TestMaintainerrDirectoryInstallUnsupportedNativeACLModelStaysManual(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this case checks the unsupported native non-Linux ACL boundary")
	}
	h := newDirectoryHarness(t, maintainerr.App{})
	data := filepath.Join(h.root(), "apps", "maintainerr", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.install("maintainerr"); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("fake Linux daemon information bypassed the real ACL boundary: %v", err)
	}
	if e := h.rec.find("maintainerr", "directories"); e == nil || e.Event != events.StepManual || !strings.Contains(e.Hint, "ACL") {
		t.Fatal("Install did not reach the shared ACL admission boundary")
	}
	after, err := os.Stat(data)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("unsupported native storage was changed")
	}
	assertNoDirectoryRepairOrStartup(t, h.runner.Calls())
}

type maintainerrDirectoryRefreshApp struct {
	maintainerr.App
	revised bool
}

func (a *maintainerrDirectoryRefreshApp) Compose(rc app.RenderContext) string {
	definition := a.App.Compose(rc)
	if a.revised {
		definition = strings.Replace(definition, "restart: unless-stopped", "restart: unless-stopped\n    labels:\n      self-host-consumer-revision: updated", 1)
	}
	return definition
}

func TestMaintainerrDirectoryGeneratedRefreshPreparesMissingDataBeforeUp(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	a := &maintainerrDirectoryRefreshApp{}
	h := newDirectoryHarness(t, a)
	maintainerrDirectoryHTTP(t, h, true)
	data := filepath.Join(h.root(), "apps", "maintainerr", "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.install("maintainerr"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	a.revised = true
	starts := 0
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if strings.Contains(systemtest.Line(cmd), "up -d") {
			starts++
			info, err := os.Stat(data)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("generated refresh reached Up before private storage preparation")
			}
			owner := info.Sys().(*syscall.Stat_t)
			if int(owner.Uid) != os.Geteuid() || int(owner.Gid) != os.Getegid() {
				t.Error("generated refresh prepared the wrong runtime ownership")
			}
		}
		return system.Result{}
	})
	if err := h.svc.Update(t.Context(), []string{"maintainerr"}); err != nil || starts != 1 {
		t.Fatalf("generated refresh did not prepare before startup: %v, starts=%d", err, starts)
	}
}

func TestMaintainerrDirectoryGeneratedRefreshPreservesIncompatibleData(t *testing.T) {
	for _, conflict := range []string{"public", "symlink"} {
		t.Run(conflict, func(t *testing.T) {
			h := newDirectoryHarness(t, &testApp{id: "maintainerr", done: map[string]bool{}})
			if err := h.install("maintainerr"); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(h.root(), "apps", "maintainerr", "data")
			if conflict == "public" {
				if err := os.Mkdir(data, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(data, ".keep"), []byte("preserve marker"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(t.TempDir(), data); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(data)
			if err != nil {
				t.Fatal(err)
			}
			h.svc.Catalog = catalog.New([]app.App{maintainerr.App{}}, nil)
			calls := len(h.runner.Calls())
			if err := h.svc.Update(t.Context(), []string{"maintainerr"}); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("incompatible generated-refresh data did not remain Manual: %v", err)
			}
			after, err := os.Lstat(data)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("generated refresh changed existing storage")
			}
			if conflict == "public" {
				if bytes, err := os.ReadFile(filepath.Join(data, ".keep")); err != nil || string(bytes) != "preserve marker" {
					t.Fatal("generated refresh modified an old marker")
				}
			}
			assertNoDirectoryRepairOrStartup(t, h.runner.Calls()[calls:])
		})
	}
}

func TestMaintainerrDirectoryRuntimeStorageDriftRejectsBeforeHTTPOrStartup(t *testing.T) {
	for _, conflict := range []string{"unsupported-native", "missing", "mode", "symlink", "owner"} {
		t.Run(conflict, func(t *testing.T) {
			if conflict == "unsupported-native" {
				if runtime.GOOS == "linux" {
					t.Skip("this case checks the unsupported native non-Linux ACL boundary")
				}
			} else {
				requireNativeDirectoryFilesystem(t)
			}
			for _, operation := range []string{"start", "restart", "setup", "check"} {
				t.Run(operation, func(t *testing.T) {
					var installed app.App = maintainerr.App{}
					if conflict == "unsupported-native" {
						installed = &testApp{id: "maintainerr", done: map[string]bool{}}
					}
					h := newDirectoryHarness(t, installed)
					requests := maintainerrDirectoryHTTP(t, h, true)
					if err := h.install("maintainerr"); err != nil {
						t.Fatal(err)
					}
					h.svc.Catalog = catalog.New([]app.App{maintainerr.App{}}, nil)
					p, _ := h.svc.Project()
					data := p.Path("apps", "maintainerr", "data")
					if conflict == "missing" || conflict == "symlink" {
						if err := os.Remove(data); err != nil {
							t.Fatal(err)
						}
					}
					if conflict == "unsupported-native" {
						if err := os.Mkdir(data, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					if conflict == "symlink" {
						if err := os.Symlink(t.TempDir(), data); err != nil {
							t.Fatal(err)
						}
					}
					if conflict == "mode" {
						if err := os.Chmod(data, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					if conflict != "missing" && conflict != "symlink" {
						if err := os.WriteFile(filepath.Join(data, ".keep"), []byte("preserve marker"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					if conflict == "owner" {
						settings, err := p.LoadSettings()
						if err != nil {
							t.Fatal(err)
						}
						settings["PUID"] = strconv.Itoa(os.Geteuid() + 1)
						if err := p.SaveSettings(settings); err != nil {
							t.Fatal(err)
						}
					}
					before, _ := os.Lstat(data)
					calls := len(h.runner.Calls())
					httpBefore := requests()
					var err error
					switch operation {
					case "start":
						err = h.svc.Start(context.Background(), []string{"maintainerr"})
					case "restart":
						err = h.svc.Restart(context.Background(), []string{"maintainerr"})
					case "setup":
						err = h.svc.Setup(t.Context(), []string{"maintainerr"})
					case "check":
						unlock, lockErr := p.Lock(os.Getpid(), func(int) bool { return false })
						if lockErr != nil {
							t.Fatal(lockErr)
						}
						defer unlock()
						ss, openErr := h.svc.open(t.Context())
						if openErr != nil {
							t.Fatal(openErr)
						}
						err = ss.check(t.Context(), "maintainerr")
					}
					if !errors.Is(err, helpers.ErrManual) && !errors.Is(err, ErrManual) {
						t.Fatalf("%s lost the missing-data guard: %v", operation, err)
					}
					assertNoDirectoryMutationCommands(t, h.runner.Calls()[calls:])
					if requests() != httpBefore {
						t.Fatal("storage refusal reached Maintainerr HTTP")
					}
					if operation == "setup" {
						if event := h.rec.find("maintainerr", "directories"); event == nil || event.Event != events.StepManual {
							t.Fatal("Setup lost the Service-owned directory hold")
						}
						state, loadErr := p.LoadState()
						if loadErr != nil || state.Apps["maintainerr"].Steps["directories"].Status != stepstatus.StepManual {
							t.Fatal("Setup did not persist the directory Manual step")
						}
					}
					after, statErr := os.Lstat(data)
					if conflict == "missing" {
						if !errors.Is(statErr, os.ErrNotExist) {
							t.Fatal("runtime validation created missing data")
						}
					} else if statErr != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Sys().(*syscall.Stat_t).Uid != after.Sys().(*syscall.Stat_t).Uid || before.Sys().(*syscall.Stat_t).Gid != after.Sys().(*syscall.Stat_t).Gid {
						t.Fatal("runtime validation changed incompatible data")
					}
					if conflict != "missing" && conflict != "symlink" {
						if bytes, err := os.ReadFile(filepath.Join(data, ".keep")); err != nil || string(bytes) != "preserve marker" {
							t.Fatal("runtime validation changed an old marker")
						}
					}
				})
			}
		})
	}
}

func TestMaintainerrDirectoryRestoreRejectsStagedStorageBeforeDestruction(t *testing.T) {
	for _, conflict := range []string{"unsupported-native", "missing", "mode"} {
		t.Run(conflict, func(t *testing.T) {
			if conflict == "unsupported-native" {
				if runtime.GOOS == "linux" {
					t.Skip("this case checks the unsupported native non-Linux ACL boundary")
				}
			} else {
				requireNativeDirectoryFilesystem(t)
			}
			for _, running := range []bool{true, false} {
				t.Run("running="+strconv.FormatBool(running), func(t *testing.T) {
					peer := &testApp{id: "one", done: map[string]bool{}}
					var installed app.App = maintainerr.App{}
					if conflict == "unsupported-native" {
						installed = &testApp{id: "maintainerr", done: map[string]bool{}}
					}
					h := newDirectoryHarness(t, installed, peer)
					requests := maintainerrDirectoryHTTP(t, h, true)
					if err := h.install("maintainerr", "one"); err != nil {
						t.Fatal(err)
					}
					p, _ := h.svc.Project()
					data := p.Path("apps", "maintainerr", "data")
					if conflict == "mode" {
						if err := os.Chmod(data, 0o755); err != nil {
							t.Fatal(err)
						}
					} else if conflict == "missing" {
						if err := os.Remove(data); err != nil {
							t.Fatal(err)
						}
					}
					archive, err := h.svc.Backup(t.Context(), nil, filepath.Join(t.TempDir(), "staged.tar.zst"), "")
					if err != nil {
						t.Fatal(err)
					}
					if conflict == "mode" {
						if err := os.Chmod(data, 0o700); err != nil {
							t.Fatal(err)
						}
					} else if err := os.Mkdir(data, 0o700); err != nil {
						t.Fatal(err)
					}
					marker := filepath.Join(data, "keep-current")
					if err := os.WriteFile(marker, []byte("current data"), 0o600); err != nil {
						t.Fatal(err)
					}
					beforeInfo, err := os.Stat(data)
					if err != nil {
						t.Fatal(err)
					}
					beforeState, err := p.ReadHomeFile("state.json")
					if err != nil {
						t.Fatal(err)
					}
					beforeCompose, err := p.ReadCompose()
					if err != nil {
						t.Fatal(err)
					}
					h.svc.Catalog = catalog.New([]app.App{maintainerr.App{}, peer}, nil)
					h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
						if strings.Contains(systemtest.Line(cmd), " ps ") {
							output := `{"Service":"one","State":"running"}`
							if running {
								output += "\n" + `{"Service":"maintainerr","State":"running"}`
							}
							return system.Result{Stdout: []byte(output)}
						}
						return system.Result{}
					})
					calls := len(h.runner.Calls())
					httpBefore := requests()
					if err := h.svc.Restore(t.Context(), archive, nil, ""); !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "backup declared directories for maintainerr") {
						t.Fatalf("invalid staged storage escaped pre-destructive admission: %v", err)
					}
					assertNoDirectoryMutationCommands(t, h.runner.Calls()[calls:])
					if requests() != httpBefore || h.rec.find("maintainerr", "restore") != nil {
						t.Fatal("staged storage refusal reached App HTTP or replacement")
					}
					if pending, err := p.LoadRestoreStartup(); err != nil || pending != nil {
						t.Fatal("invalid staged storage acquired startup intent")
					}
					afterInfo, err := os.Stat(data)
					if err != nil || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Mode() != afterInfo.Mode() {
						t.Fatal("staged refusal replaced current data")
					}
					if bytes, err := os.ReadFile(marker); err != nil || string(bytes) != "current data" {
						t.Fatal("staged refusal changed current content")
					}
					if state, err := p.ReadHomeFile("state.json"); err != nil || string(state) != string(beforeState) {
						t.Fatal("staged refusal changed current records")
					}
					if definition, err := p.ReadCompose(); err != nil || string(definition) != string(beforeCompose) {
						t.Fatal("staged refusal changed Compose")
					}
				})
			}
		})
	}
}

func TestMaintainerrDirectoryRollbackRetainsRecoveryBeforeUnsafeUp(t *testing.T) {
	h := newDirectoryHarness(t, &testApp{id: "maintainerr", done: map[string]bool{}})
	if err := h.install("maintainerr"); err != nil {
		t.Fatal(err)
	}
	h.svc.Catalog = catalog.New([]app.App{maintainerr.App{}}, nil)
	requests := maintainerrDirectoryHTTP(t, h, true)
	p, _ := h.svc.Project()
	unlock, err := p.Lock(os.Getpid(), func(int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ss, err := h.svc.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	staging, err := p.NewStaging(func(string) string { return "apps" })
	if err != nil {
		t.Fatal(err)
	}
	rs := &restoration{s: h.svc, staging: staging, dockerApps: map[string]bool{}}
	calls := len(h.runner.Calls())
	err = rs.rollback(t.Context(), ss, ss.state.Apps, ss.secrets.values, []string{"maintainerr"}, []string{"maintainerr"})
	if !errors.Is(err, helpers.ErrManual) || !rs.keepRecovery || !strings.Contains(err.Error(), "original App maintainerr storage is not admitted") {
		t.Fatalf("rollback did not hold unsafe original storage: %v", err)
	}
	assertNoDirectoryMutationCommands(t, h.runner.Calls()[calls:])
	if requests() != 0 {
		t.Fatal("rollback refusal reached HTTP")
	}
	if _, err := os.Stat(staging.Dir()); err != nil {
		t.Fatal("unsafe rollback discarded recovery")
	}
	if _, err := os.Lstat(p.Path("apps", "maintainerr", "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback validation created missing storage")
	}
}
