package service

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/catalog"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
	"golang.org/x/sys/unix"
)

type validationDirectoryApp struct {
	*testApp
	files, setups, checks int
	contexts              []app.RenderContext
}

func (a *validationDirectoryApp) Directories(rc app.RenderContext) []app.Directory {
	a.contexts = append(a.contexts, rc)
	return []app.Directory{{Path: "config", Mode: 0o700, UID: rc.Settings["PUID"], GID: rc.Settings["PGID"]}}
}

func (a *validationDirectoryApp) Compose(app.RenderContext) string {
	return "services:\n  " + a.id + ":\n    image: " + a.Info().Image + "\n    user: ${PUID}:${PGID}\n    volumes:\n      - type: bind\n        source: ./config\n        target: /config\n        bind:\n          create_host_path: false\n"
}

func (a *validationDirectoryApp) Files(app.RenderContext, func(string) string) []app.File {
	a.files++
	return nil
}

func (a *validationDirectoryApp) Setup(ctx context.Context, h *helpers.H) error {
	a.setups++
	return a.testApp.Setup(ctx, h)
}

func (a *validationDirectoryApp) Check(context.Context, *helpers.H) error {
	a.checks++
	return nil
}

func newValidationDirectoryApp(id string) *validationDirectoryApp {
	return &validationDirectoryApp{testApp: &testApp{id: id, done: map[string]bool{}}}
}

func seedDirectoryValidationHarness(t *testing.T, a *validationDirectoryApp) *harness {
	t.Helper()
	h := newDirectoryHarness(t, &testApp{id: a.id, done: map[string]bool{}})
	if err := h.install(a.id); err != nil {
		t.Fatal(err)
	}
	h.svc.Catalog = catalog.New([]app.App{a}, nil)
	return h
}

func assertNoDirectoryMutationCommands(t *testing.T, calls []string) {
	t.Helper()
	assertNoDirectoryRepairOrStartup(t, calls)
	for _, call := range calls {
		for _, verb := range []string{" restart ", " stop ", " rm ", " down"} {
			if strings.Contains(call, verb) {
				t.Fatalf("directory admission changed containers: %s", call)
			}
		}
	}
}

func TestDirectoryValidationInstallWithoutAppPreflightPreparesThenValidates(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	a := newValidationDirectoryApp("one")
	h := newDirectoryHarness(t, a)
	starts := 0
	h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
		if strings.Contains(systemtest.Line(cmd), "up -d") {
			starts++
			info, err := os.Stat(filepath.Join(h.root(), "apps", "one", "config"))
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatal("first Up preceded private directory preparation")
			}
		}
		return system.Result{}
	})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || a.files != 1 || a.setups != 1 || a.checks != 1 {
		t.Fatalf("Install did not prepare once before guarded startup/Setup/Check: starts=%d, app=%+v", starts, a)
	}
	for _, run := range []func() error{
		func() error { return h.svc.Start(t.Context(), []string{"one"}) },
		func() error { return h.svc.Restart(t.Context(), []string{"one"}) },
		func() error { return h.svc.Setup(t.Context(), []string{"one"}) },
	} {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	if a.files != 1 {
		t.Fatal("runtime validation evaluated File declarations")
	}
}

func TestDirectoryValidationLifecycleRefusesMissingOrIncompatibleWithoutAppPreflight(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, operation := range []string{"start", "restart", "setup", "check"} {
		for _, conflict := range []string{"missing", "mode", "symlink", "owner"} {
			t.Run(operation+"/"+conflict, func(t *testing.T) {
				a := newValidationDirectoryApp("one")
				h := seedDirectoryValidationHarness(t, a)
				leaf := filepath.Join(h.root(), "apps", "one", "config")
				if conflict == "symlink" {
					if err := os.Symlink(t.TempDir(), leaf); err != nil {
						t.Fatal(err)
					}
				} else if conflict != "missing" {
					if err := os.Mkdir(leaf, 0o700); err != nil {
						t.Fatal(err)
					}
					if conflict == "mode" {
						if err := os.Chmod(leaf, 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
				p, _ := h.svc.Project()
				if conflict == "owner" {
					settings, err := p.LoadSettings()
					if err != nil {
						t.Fatal(err)
					}
					settings["PUID"] = "23001"
					if err := p.SaveSettings(settings); err != nil {
						t.Fatal(err)
					}
				}
				beforeInfo, _ := os.Lstat(leaf)
				before := len(h.runner.Calls())
				var err error
				switch operation {
				case "start":
					err = h.svc.Start(t.Context(), []string{"one"})
				case "restart":
					err = h.svc.Restart(t.Context(), []string{"one"})
				case "setup":
					err = h.svc.Setup(t.Context(), []string{"one"})
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
					err = ss.check(t.Context(), "one")
				}
				if !errors.Is(err, helpers.ErrManual) && !errors.Is(err, ErrManual) {
					t.Fatalf("required-existing directory was not held: %v", err)
				}
				assertNoDirectoryMutationCommands(t, h.runner.Calls()[before:])
				afterInfo, statErr := os.Lstat(leaf)
				if conflict == "missing" {
					if !errors.Is(statErr, os.ErrNotExist) {
						t.Fatal("runtime guard created missing storage")
					}
				} else if statErr != nil || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Mode() != afterInfo.Mode() {
					t.Fatal("runtime guard changed incompatible storage")
				}
				if a.files != 0 || a.setups != 0 || a.checks != 0 {
					t.Fatal("storage refusal reached App file/Setup/Check work")
				}
			})
		}
	}
}

func TestDirectoryValidationUnsupportedNativeModelRefusesServiceWork(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("unsupported native non-Linux boundary")
	}
	for _, operation := range []string{"start", "restart", "setup", "restore"} {
		t.Run(operation, func(t *testing.T) {
			a := newValidationDirectoryApp("one")
			h := seedDirectoryValidationHarness(t, a)
			leaf := filepath.Join(h.root(), "apps", "one", "config")
			if err := os.Mkdir(leaf, 0o700); err != nil {
				t.Fatal(err)
			}
			var archive string
			if operation == "restore" {
				var err error
				archive, err = h.svc.Backup(t.Context(), nil, filepath.Join(t.TempDir(), "directories.tar.zst"), "")
				if err != nil {
					t.Fatal(err)
				}
			}
			beforeInfo, _ := os.Stat(leaf)
			before := len(h.runner.Calls())
			var err error
			switch operation {
			case "start":
				err = h.svc.Start(t.Context(), []string{"one"})
			case "restart":
				err = h.svc.Restart(t.Context(), []string{"one"})
			case "setup":
				err = h.svc.Setup(t.Context(), []string{"one"})
			case "restore":
				err = h.svc.Restore(t.Context(), archive, nil, "")
			}
			if !errors.Is(err, helpers.ErrManual) && !errors.Is(err, ErrManual) {
				t.Fatalf("fake Linux daemon metadata bypassed native inspection: %v", err)
			}
			if operation == "setup" {
				if event := h.rec.find("one", "directories"); event == nil || !strings.Contains(event.Hint, "ACL") {
					t.Fatal("Setup lost the native inspection Manual hint")
				}
			} else if !strings.Contains(err.Error(), "ACL") {
				t.Fatal("native inspection refusal lost its reason")
			}
			assertNoDirectoryMutationCommands(t, h.runner.Calls()[before:])
			afterInfo, _ := os.Stat(leaf)
			if !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Mode() != afterInfo.Mode() {
				t.Fatal("native-model refusal replaced or repaired live storage")
			}
			p, _ := h.svc.Project()
			pending, err := p.LoadRestoreStartup()
			if err != nil || pending != nil {
				t.Fatal("admission refusal persisted startup intent")
			}
		})
	}
}

func TestDirectoryValidationRestoreChecksStoppedReplacementBeforeDestruction(t *testing.T) {
	requireNativeDirectoryFilesystem(t)
	for _, conflict := range []string{"missing", "mode"} {
		t.Run(conflict, func(t *testing.T) {
			h := newDirectoryHarness(t, &testApp{id: "one", done: map[string]bool{}}, &testApp{id: "two", done: map[string]bool{}})
			if err := h.install("one", "two"); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"one", "two"} {
				if id == "two" && conflict == "missing" {
					continue
				}
				if err := os.Mkdir(filepath.Join(h.root(), "apps", id, "config"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			leaf := filepath.Join(h.root(), "apps", "two", "config")
			if conflict == "mode" {
				if err := os.Chmod(leaf, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			archive, err := h.svc.Backup(t.Context(), nil, filepath.Join(t.TempDir(), "stopped.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			if conflict == "missing" {
				if err := os.Mkdir(leaf, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(leaf, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(leaf, "keep-current")
			if err := os.WriteFile(marker, []byte("current data"), 0o600); err != nil {
				t.Fatal(err)
			}
			h.svc.Catalog = catalog.New([]app.App{newValidationDirectoryApp("one"), newValidationDirectoryApp("two")}, nil)
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), " ps ") {
					return system.Result{Stdout: []byte(`{"Service":"one","State":"running"}`)}
				}
				return system.Result{}
			})
			before := len(h.runner.Calls())
			if err := h.svc.Restore(t.Context(), archive, nil, ""); !errors.Is(err, helpers.ErrManual) || !strings.Contains(err.Error(), "two") {
				t.Fatalf("stopped invalid replacement escaped admission: %v", err)
			}
			assertNoDirectoryMutationCommands(t, h.runner.Calls()[before:])
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "current data" {
				t.Fatal("replacement admission destroyed current data")
			}
			p, _ := h.svc.Project()
			if pending, err := p.LoadRestoreStartup(); err != nil || pending != nil {
				t.Fatal("invalid replacement acquired startup intent")
			}
		})
	}
}

func TestDirectoryValidationRestoreUsesEffectivePublicSettingsAndArchivedRecord(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "current-settings"
		if fresh {
			name = "archived-settings"
		}
		t.Run(name, func(t *testing.T) {
			a := newValidationDirectoryApp("one")
			source := seedDirectoryValidationHarness(t, a)
			if err := os.Mkdir(filepath.Join(source.root(), "apps", "one", "config"), 0o700); err != nil {
				t.Fatal(err)
			}
			p, _ := source.svc.Project()
			settings, err := p.LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			settings["PUID"] = strconv.Itoa(os.Geteuid() + 1)
			if err := p.SaveSettings(settings); err != nil {
				t.Fatal(err)
			}
			state, err := p.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			state.Apps["one"].Inputs = map[string]string{"label": "archived"}
			if err := p.SaveState(state); err != nil {
				t.Fatal(err)
			}
			archive, err := source.svc.Backup(t.Context(), nil, filepath.Join(t.TempDir(), "settings.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			h := source
			wantUID := settings["PUID"]
			if fresh {
				h = newDirectoryHarness(t, a)
			} else {
				wantUID = strconv.Itoa(os.Geteuid() + 2)
				settings["PUID"] = wantUID
				if err := p.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
				state.Apps["one"].Inputs["label"] = "current"
				if err := p.SaveState(state); err != nil {
					t.Fatal(err)
				}
			}
			before := len(h.runner.Calls())
			if err := h.svc.Restore(t.Context(), archive, nil, ""); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("incompatible staged host owner did not hold: %v", err)
			}
			if len(a.contexts) != 1 || a.contexts[0].Settings["PUID"] != wantUID || a.contexts[0].Inputs["label"] != "archived" {
				t.Fatalf("directory admission used the wrong public context: %+v", a.contexts)
			}
			assertNoDirectoryMutationCommands(t, h.runner.Calls()[before:])
			destination, _ := h.svc.Project()
			if pending, err := destination.LoadRestoreStartup(); err != nil || pending != nil {
				t.Fatal("failed staged admission wrote startup intent")
			}
			if fresh && destination.Exists() {
				t.Fatal("failed staged admission initialized the destination")
			}
		})
	}
}

func directoryValidationDockerArchive(t *testing.T, h *harness) string {
	t.Helper()
	p, _ := h.svc.Project()
	file := filepath.Join(t.TempDir(), "docker-directories.tar.zst")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := backup.Create(f, "", backup.Manifest{Version: 1, Apps: []backup.ManifestApp{{ID: "one", OwnershipNamespace: backup.OwnershipDocker}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "state.json", "compose.yml"} {
		data, err := p.ReadHomeFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.AddBytes(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stream bytes.Buffer
	tw := tar.NewWriter(&stream)
	for _, entry := range []struct {
		name string
		mode int64
	}{{".", 0o755}, {"config", 0o700}} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Typeflag: tar.TypeDir, Mode: entry.mode, Uid: os.Geteuid(), Gid: os.Getegid()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.AddTar(&stream, "apps/one"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestDirectoryValidationRestoreMaterializesMetadataOnceInStagingUnderLock(t *testing.T) {
	for _, succeeds := range []bool{false, true} {
		name := "materialization-fails"
		if succeeds {
			name = "supported-native-success"
		}
		t.Run(name, func(t *testing.T) {
			if succeeds {
				requireNativeDirectoryFilesystem(t)
			}
			a := newValidationDirectoryApp("one")
			h := seedDirectoryValidationHarness(t, a)
			archive := directoryValidationDockerArchive(t, h)
			p, _ := h.svc.Project()
			calls := 0
			starts := 0
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				line := systemtest.Line(cmd)
				if strings.Contains(line, " ps ") {
					return system.Result{Stdout: []byte(`{"Service":"one","State":"running"}`)}
				}
				if strings.Contains(line, "up -d") {
					starts++
				}
				return system.Result{}
			})
			denied := errors.New("fixture staged metadata failure")
			h.runner.OnStreamIO("--entrypoint sh", func(_ context.Context, cmd system.Cmd, stdin io.Reader, _, _ io.Writer) error {
				if !strings.Contains(systemtest.Line(cmd), "--cap-add CHOWN") {
					return nil
				}
				calls++
				if !strings.Contains(systemtest.Line(cmd), "src="+p.Root+"/.restore-") || !strings.Contains(systemtest.Line(cmd), "/apps/one,") {
					t.Fatal("ownership materialization escaped staging")
				}
				lock, err := os.Open(p.Path(".lock"))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
					t.Fatalf("metadata materialization ran without the live kernel lock: %v", err)
				}
				if pending, err := p.LoadRestoreStartup(); err != nil || pending != nil {
					t.Fatal("metadata materialization ran after startup intent")
				}
				if _, err := io.Copy(io.Discard, stdin); err != nil {
					return err
				}
				if !succeeds {
					return denied
				}
				return nil
			})
			before := len(h.runner.Calls())
			err := h.svc.Restore(t.Context(), archive, nil, "")
			if succeeds && err != nil || !succeeds && !errors.Is(err, denied) {
				t.Fatalf("staged materialization result: %v", err)
			}
			if calls != 1 {
				t.Fatalf("metadata materialized %d times", calls)
			}
			if succeeds && (starts != 1 || len(a.contexts) != 2) {
				t.Fatalf("Restore did not validate staging and live storage before one Up: starts=%d, inspections=%d", starts, len(a.contexts))
			}
			if !succeeds {
				for _, call := range h.runner.Calls()[before:] {
					if strings.Contains(call, "up -d") || strings.Contains(call, " stop ") || strings.Contains(call, " rm one") {
						t.Fatalf("metadata failure reached live mutation: %s", call)
					}
				}
			}
		})
	}
}

func TestDirectoryValidationRollbackRetainsRecoveryInsteadOfUnsafeDirectUp(t *testing.T) {
	a := newValidationDirectoryApp("one")
	h := seedDirectoryValidationHarness(t, a)
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
	before := len(h.runner.Calls())
	err = rs.rollback(t.Context(), ss, ss.state.Apps, ss.secrets.values, []string{"one"}, []string{"one"})
	if !errors.Is(err, helpers.ErrManual) || !rs.keepRecovery {
		t.Fatalf("unsafe rollback did not retain recovery: %v", err)
	}
	assertNoDirectoryMutationCommands(t, h.runner.Calls()[before:])
	if _, err := os.Stat(staging.Dir()); err != nil {
		t.Fatal("refused rollback removed recovery")
	}
	if _, err := os.Lstat(p.Path("apps", "one", "config")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rollback guard created missing storage")
	}
}
