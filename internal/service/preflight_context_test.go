package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

type preflightContextObservation struct {
	settings map[string]string
	inputs   map[string]string
	early    bool
}

type preflightContextApp struct {
	testApp
	t            *testing.T
	observations []preflightContextObservation
	mutate       bool
	media        bool
	onEarly      func()
	renders      []string
}

func newPreflightContextApp(t *testing.T) *preflightContextApp {
	return &preflightContextApp{testApp: testApp{id: "context", done: map[string]bool{}}, t: t}
}

func (a *preflightContextApp) Info() app.Info {
	info := a.testApp.Info()
	info.UsesMedia = a.media
	return info
}

func (*preflightContextApp) Inputs() []app.Input {
	return []app.Input{{Key: "library", Default: "library"}, {Key: "token", Private: true}}
}

func (*preflightContextApp) Secrets() []app.SecretSpec {
	return []app.SecretSpec{{Name: "persistent_key", Length: 32}}
}

func (a *preflightContextApp) Compose(rc app.RenderContext) string {
	a.renders = append(a.renders, rc.Settings["MEDIA_ROOT"])
	return a.testApp.Compose(rc) + fmt.Sprintf("    volumes:\n      - %s:/library:ro\n", filepath.Join(rc.Settings["MEDIA_ROOT"], rc.Inputs["library"]))
}

func (a *preflightContextApp) Preflight(_ context.Context, h *helpers.H) error {
	early := h.Secrets == nil
	if early {
		if h.Config.Exec != nil || h.Inputs["token"] != "" {
			a.t.Fatal("pure prerequisites received private inputs or execution capability")
		}
		for key := range h.Settings {
			if !slices.Contains(project.SettingKeys, key) {
				a.t.Fatalf("pure prerequisites received a non-public setting: %s", key)
			}
		}
	}
	a.observations = append(a.observations, preflightContextObservation{maps.Clone(h.Settings), maps.Clone(h.Inputs), early})
	if h.Settings["MEDIA_ROOT"] == "" {
		return &helpers.ManualError{Hint: "effective media root is missing"}
	}
	dir, err := os.Stat(filepath.Join(h.Settings["MEDIA_ROOT"], h.Inputs["library"]))
	if err != nil || !dir.IsDir() {
		return &helpers.ManualError{Hint: "prepare the selected library directory"}
	}
	if early {
		if a.mutate {
			h.Settings["MEDIA_ROOT"] = "/unapproved-mutated-root"
			h.Inputs["library"] = "unapproved-mutated-library"
		}
		if a.onEarly != nil {
			f := a.onEarly
			a.onEarly = nil
			f()
		}
	}
	return nil
}

func prepareContextLibrary(t *testing.T, root, library string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, library), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightContextInstallEffectiveSettings(t *testing.T) {
	for _, mode := range []string{"default shared", "data root", "legacy", "explicit shared", "rootless"} {
		t.Run(mode, func(t *testing.T) {
			a := newPreflightContextApp(t)
			a.mutate = true
			h := newHarness(t, a)
			p, err := h.svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			opts := InstallOptions{Targets: []string{"context"}, PrivateInputs: map[string]map[string]string{"context": {"token": "synthetic-private-input"}}, Settings: map[string]string{"ADMIN_PASSWORD": "synthetic-private-setting", "UNKNOWN_VALUE": "synthetic-unknown-setting"}}
			root := filepath.Join(p.Owner.Home, "data", "media")
			layout := "shared"
			switch mode {
			case "data root":
				opts.DataRoot = filepath.Join(t.TempDir(), "chosen-data")
				root = filepath.Join(opts.DataRoot, "media")
			case "legacy":
				opts.MediaRoot = filepath.Join(t.TempDir(), "chosen-media")
				opts.DownloadsRoot = filepath.Join(t.TempDir(), "chosen-downloads")
				root, layout = opts.MediaRoot, "legacy"
			case "explicit shared":
				opts.MediaRoot = filepath.Join(t.TempDir(), "ignored-media")
				opts.Settings["MEDIA_LAYOUT"] = "shared"
			case "rootless":
				h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			}
			prepareContextLibrary(t, root, "library")
			lanReads, zoneReads, dockerReads := 0, 0, 0
			h.deps.LANIP = func() (string, error) { lanReads++; return "192.168.1.20", nil }
			h.deps.TimeZone = func() string { zoneReads++; return "UTC" }
			if mode != "rootless" {
				h.runner.OnFunc("docker info", func(system.Cmd) system.Result {
					dockerReads++
					if dockerReads == 1 {
						return system.Result{Stdout: []byte(`["name=seccomp"]`)}
					}
					return system.Result{Stdout: []byte(`["name=rootless"]`)}
				})
			}
			if err := h.svc.Install(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			settings, err := p.LoadSettings()
			if err != nil || settings["MEDIA_ROOT"] != root || settings["MEDIA_LAYOUT"] != layout || settings["TZ"] != "UTC" {
				t.Fatal("persisted settings differ from the approved roots")
			}
			if lanReads != 1 || zoneReads != 1 || mode != "rootless" && dockerReads != 2 {
				t.Fatal("initialization repeated settings detection")
			}
			if mode != "rootless" && settings["PUID"] != fmt.Sprint(p.Owner.UID) {
				t.Fatal("initialization redetected identity instead of reusing the approved snapshot")
			}
			if mode == "rootless" && (settings["PUID"] != "0" || settings["PGID"] != "0") {
				t.Fatal("rootless effective identities were lost")
			}
			if len(a.observations) != 4 || !a.observations[0].early || !a.observations[1].early || a.observations[2].early || a.observations[3].early {
				t.Fatal("early pure, locked pure, pre-generation and final startup checks were not retained")
			}
			for _, observed := range a.observations {
				if !maps.Equal(observed.settings, settings) || observed.inputs["library"] != "library" {
					t.Fatal("a prerequisite mutated the snapshot or received inconsistent settings")
				}
			}
			if len(a.renders) != 1 || a.renders[0] != root {
				t.Fatal("rendering used a different media root")
			}
			fragment, err := p.ReadFragment("context")
			if err != nil || !strings.Contains(string(fragment), filepath.Join(root, "library")) {
				t.Fatal("persisted Compose does not use the approved root")
			}
		})
	}
}

func contextArchive(t *testing.T, root string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "context.tar.zst")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := backup.Create(f, "", backup.Manifest{Version: 1, Apps: []backup.ManifestApp{{ID: "context", Managed: true}, {ID: "other", Managed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		".env":                     "TZ=UTC\nPUID=1000\nPGID=1000\nUMASK=022\nEXPOSURE=local\nLAN_IP=127.0.0.1\nMEDIA_ROOT=" + root + "\nADMIN_PASSWORD=synthetic-private-setting\nUNKNOWN_VALUE=synthetic-unknown-setting\n",
		"state.json":               `{"version":1,"apps":{"context":{"port":9000,"inputs":{"library":"archived-library","token":"synthetic-private-archived-input"}},"other":{"port":9001}}}`,
		"apps/context/compose.yml": "services:\n  context:\n    image: example/test:latest\n",
		"apps/context/config/db":   "archived-data",
		"apps/other/compose.yml":   "services:\n  other:\n    image: example/test:latest\n",
		"apps/other/config/db":     "archived-other-data",
	} {
		if err := w.AddBytes(name, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPreflightContextRestoreFreshArchive(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archived-root")
	prepareContextLibrary(t, root, "archived-library")
	a := newPreflightContextApp(t)
	a.mutate = true
	h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
	if err := h.svc.Restore(t.Context(), contextArchive(t, root), nil, ""); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	settings, err := p.LoadSettings()
	if err != nil || settings["MEDIA_ROOT"] != root || settings["MEDIA_LAYOUT"] != "" {
		t.Fatal("fresh Restore lost or normalized archived legacy Host settings")
	}
	if len(a.observations) != 3 || !a.observations[0].early || !a.observations[1].early || a.observations[2].early {
		t.Fatal("Restore must validate the extracted archive, locked context and final startup")
	}
	for _, observed := range a.observations {
		if !maps.Equal(observed.settings, settings) || observed.inputs["library"] != "archived-library" {
			t.Fatal("Restore did not preserve archived public inputs and isolated settings")
		}
	}
}

func TestPreflightContextInstallMissingLibrary(t *testing.T) {
	for _, mode := range []string{"missing", "file", "wrong root"} {
		t.Run(mode, func(t *testing.T) {
			a := newPreflightContextApp(t)
			h := newHarness(t, a)
			root := t.TempDir()
			if mode == "file" {
				if err := os.WriteFile(filepath.Join(root, "library"), []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "wrong root" {
				prepareContextLibrary(t, t.TempDir(), "library")
			}
			opts := InstallOptions{Targets: []string{"context"}, MediaRoot: root}
			if err := h.svc.Install(t.Context(), opts); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("unprepared effective library was admitted: %v", err)
			}
			p, _ := h.svc.Project()
			if p.Exists() || len(a.renders) != 0 || len(h.composeCalls()) != 0 {
				t.Fatal("failed pure admission initialized, rendered or ran Compose")
			}
			if _, err := os.Stat(p.Root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed pure admission created the Project directory")
			}
			if mode == "file" {
				if err := os.Remove(filepath.Join(root, "library")); err != nil {
					t.Fatal(err)
				}
			}
			prepareContextLibrary(t, root, "library")
			if err := h.svc.Install(t.Context(), opts); err != nil {
				t.Fatalf("preparing the actual selected library did not recover Install: %v", err)
			}
		})
	}
}

func TestPreflightContextInstallIgnoresNewProjectFlags(t *testing.T) {
	a := newPreflightContextApp(t)
	a.mutate = true
	h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
	if err := h.install("other"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	settings["MEDIA_ROOT"] = root
	delete(settings, "MEDIA_LAYOUT")
	delete(settings, "DATA_ROOT")
	if err := p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSecrets(map[string]string{"ADMIN_PASSWORD": "synthetic-saved-private", "UNKNOWN_VALUE": "synthetic-saved-unknown"}); err != nil {
		t.Fatal(err)
	}
	wrong := t.TempDir()
	prepareContextLibrary(t, wrong, "library")
	opts := InstallOptions{Targets: []string{"context"}, Exposure: ExposureLocal, EasyNames: true, MediaRoot: wrong, DownloadsRoot: wrong, DataRoot: wrong, SeparateMediaUsers: true, MediaCopyFallback: true, Settings: map[string]string{"MEDIA_ROOT": wrong, "MEDIA_LAYOUT": "shared", "EASY_NAMES": "on", "ADMIN_PASSWORD": "synthetic-option-private"}}
	if err := h.svc.Install(t.Context(), opts); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("a library at only the ignored root passed admission: %v", err)
	}
	prepareContextLibrary(t, root, "library")
	h.deps.LANIP = func() (string, error) { t.Fatal("existing Project redetected its LAN address"); return "", nil }
	h.deps.TimeZone = func() string { t.Fatal("existing Project redetected its time zone"); return "" }
	if err := h.svc.Install(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	after, err := p.LoadSettings()
	if err != nil || !maps.Equal(settings, after) {
		t.Fatal("existing legacy settings were changed or normalized")
	}
	for _, observed := range a.observations {
		if !maps.Equal(observed.settings, settings) {
			t.Fatal("existing Install used ignored flags or leaked arbitrary environment settings")
		}
	}
}

func TestPreflightContextInstallRevalidatesLockedSettings(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint("initially existing=", existing), func(t *testing.T) {
			a := newPreflightContextApp(t)
			h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
			if existing {
				if err := h.install("other"); err != nil {
					t.Fatal(err)
				}
			}
			p, _ := h.svc.Project()
			root := filepath.Join(p.Owner.Home, "data", "media")
			prepareContextLibrary(t, root, "library")
			missing := filepath.Join(t.TempDir(), "changed-media")
			a.onEarly = func() {
				settings := maps.Clone(a.observations[0].settings)
				settings["MEDIA_ROOT"] = missing
				if err := p.MkdirAll(); err != nil {
					t.Fatal(err)
				}
				if err := p.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			before := len(h.composeCalls())
			if err := h.install("context"); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("changed locked settings bypassed prerequisites: %v", err)
			}
			settings, err := p.LoadSettings()
			if err != nil || settings["MEDIA_ROOT"] != missing {
				t.Fatal("initial snapshot overwrote a concurrently created or changed Project")
			}
			if len(a.observations) != 2 || !a.observations[1].early || a.observations[1].settings["MEDIA_ROOT"] != missing {
				t.Fatal("locked Install did not reload and purely revalidate changed settings")
			}
			if len(h.composeCalls()) != before || len(a.renders) != 0 || h.rec.find("context", "secret") != nil {
				t.Fatal("changed-root refusal generated secrets, rendered or started an App")
			}
		})
	}
}

func TestPreflightContextRestoreDestinationSettings(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(fmt.Sprint("whole=", whole), func(t *testing.T) {
			a := newPreflightContextApp(t)
			a.mutate = true
			h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
			p, _ := h.svc.Project()
			root := t.TempDir()
			prepareContextLibrary(t, root, "library")
			prepareContextLibrary(t, root, "archived-library")
			if whole {
				h.runner.On("docker info", system.Result{Stdout: []byte(`["name=rootless"]`)})
			}
			if err := h.svc.Install(t.Context(), InstallOptions{Targets: []string{"context", "other"}, MediaRoot: root}); err != nil {
				t.Fatal(err)
			}
			if err := p.WriteAppFile("other", "config/db", []byte("keep-unrelated-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			settings, err := p.LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			h.runner.On("docker compose --project-directory", system.Result{Stdout: []byte("[{\"Service\":\"context\",\"State\":\"running\"}]")})
			a.observations = nil
			ids := []string{"context"}
			if whole {
				ids = nil
			}
			archiveRoot := filepath.Join(t.TempDir(), "unprepared-archive-root")
			if err := h.svc.Restore(t.Context(), contextArchive(t, archiveRoot), ids, ""); err != nil {
				t.Fatal(err)
			}
			after, err := p.LoadSettings()
			if err != nil || !maps.Equal(settings, after) {
				t.Fatal("Restore replaced destination Host settings")
			}
			if len(a.observations) != 3 || !a.observations[0].early || !a.observations[1].early || a.observations[2].early {
				t.Fatal("existing Restore lost locked or final startup checks")
			}
			for _, observed := range a.observations {
				if !maps.Equal(observed.settings, settings) || observed.inputs["library"] != "archived-library" {
					t.Fatal("Restore used archived Host settings or current App inputs")
				}
			}
			data, err := os.ReadFile(p.Path("apps", "context", "config", "db"))
			if err != nil || string(data) != "archived-data" {
				t.Fatal("Restore did not install archived App data")
			}
			if !whole {
				data, err := os.ReadFile(p.Path("apps", "other", "config", "db"))
				if err != nil || string(data) != "keep-unrelated-data" {
					t.Fatal("partial Restore changed unrelated data")
				}
			}
		})
	}
}

func TestPreflightContextRestoreRejectsWrongRootBeforeActions(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint("locked drift=", drift), func(t *testing.T) {
			a := newPreflightContextApp(t)
			h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
			root := t.TempDir()
			prepareContextLibrary(t, root, "library")
			if drift {
				prepareContextLibrary(t, root, "archived-library")
			}
			if err := h.svc.Install(t.Context(), InstallOptions{Targets: []string{"context", "other"}, MediaRoot: root}); err != nil {
				t.Fatal(err)
			}
			p, _ := h.svc.Project()
			for _, id := range []string{"context", "other"} {
				if err := p.WriteAppFile(id, "config/db", []byte("keep-original-data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			settings, err := p.LoadSettings()
			if err != nil {
				t.Fatal(err)
			}
			originalCompose, err := p.ReadCompose()
			if err != nil {
				t.Fatal(err)
			}
			originalState, err := p.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			originalSecrets, err := p.LoadSecrets()
			if err != nil {
				t.Fatal(err)
			}
			a.observations = nil
			if drift {
				a.onEarly = func() {
					settings["MEDIA_ROOT"] = filepath.Join(t.TempDir(), "changed-media")
					if err := p.SaveSettings(settings); err != nil {
						t.Fatal(err)
					}
				}
			}
			archiveRoot := t.TempDir()
			prepareContextLibrary(t, archiveRoot, "archived-library")
			archive := contextArchive(t, archiveRoot)
			h.runner.OnFunc("docker compose --project-directory", func(cmd system.Cmd) system.Result {
				if strings.Contains(systemtest.Line(cmd), " ps ") {
					return system.Result{Stdout: []byte("[{\"Service\":\"context\",\"State\":\"running\"}]")}
				}
				return system.Result{ExitCode: 1}
			})
			before := len(h.runner.Calls())
			if err := h.svc.Restore(t.Context(), archive, []string{"context"}, ""); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("Restore admitted the wrong effective library: %v", err)
			}
			if len(h.runner.Calls()) != before {
				t.Fatal("refused prerequisites reached inspection, removal, startup or rollback Up")
			}
			pending, err := p.LoadRestoreStartup()
			if err != nil || pending != nil {
				t.Fatal("refused prerequisites registered startup intent")
			}
			afterSettings, err := p.LoadSettings()
			if err != nil || !maps.Equal(settings, afterSettings) {
				t.Fatal("refused Restore modified effective destination settings")
			}
			afterCompose, err := p.ReadCompose()
			if err != nil || string(afterCompose) != string(originalCompose) {
				t.Fatal("refused Restore replaced original Compose")
			}
			afterState, err := p.LoadState()
			if err != nil || !reflect.DeepEqual(originalState, afterState) {
				t.Fatal("refused Restore modified App records")
			}
			afterSecrets, err := p.LoadSecrets()
			if err != nil || !maps.Equal(originalSecrets, afterSecrets) {
				t.Fatal("refused Restore replaced private values")
			}
			for _, id := range []string{"context", "other"} {
				data, err := os.ReadFile(p.Path("apps", id, "config", "db"))
				if err != nil || string(data) != "keep-original-data" {
					t.Fatal("refused Restore replaced selected or unrelated data")
				}
			}
			if drift && (len(a.observations) != 2 || a.observations[1].settings["MEDIA_ROOT"] != settings["MEDIA_ROOT"]) {
				t.Fatal("Restore did not revalidate changed settings under lock")
			}
		})
	}
}

func TestPreflightContextRestoreRechecksConcurrentlyCreatedProject(t *testing.T) {
	root := t.TempDir()
	prepareContextLibrary(t, root, "archived-library")
	a := newPreflightContextApp(t)
	h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
	p, _ := h.svc.Project()
	missing := filepath.Join(t.TempDir(), "concurrent-media")
	a.onEarly = func() {
		settings := maps.Clone(a.observations[0].settings)
		settings["MEDIA_ROOT"] = missing
		if err := p.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.svc.Restore(t.Context(), contextArchive(t, root), nil, ""); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("Restore overwrote a concurrent Project without revalidation: %v", err)
	}
	settings, err := p.LoadSettings()
	if err != nil || settings["MEDIA_ROOT"] != missing || len(a.observations) != 2 || len(h.runner.Calls()) != 0 {
		t.Fatal("Restore used archived settings after another command created the Project")
	}
	pending, err := p.LoadRestoreStartup()
	if err != nil || pending != nil {
		t.Fatal("Restore registered startup before the concurrent context passed")
	}
}

func TestPreflightContextRestoreCanonicalArchive(t *testing.T) {
	root := t.TempDir()
	prepareContextLibrary(t, root, "library")
	prepareContextLibrary(t, root, "archived-library")
	sourceApp := newPreflightContextApp(t)
	source := newHarness(t, sourceApp)
	if err := source.svc.Install(t.Context(), InstallOptions{Targets: []string{"context"}, MediaRoot: root, Sets: []string{"context.library=archived-library"}, PrivateInputs: map[string]map[string]string{"context": {"token": "synthetic-archived-private"}}}); err != nil {
		t.Fatal(err)
	}
	archive, err := source.svc.Backup(t.Context(), nil, filepath.Join(t.TempDir(), "canonical.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	a := newPreflightContextApp(t)
	a.mutate = true
	h := newHarness(t, a)
	if err := h.svc.Restore(t.Context(), archive, nil, ""); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	settings, err := p.LoadSettings()
	if err != nil || settings["MEDIA_ROOT"] != root {
		t.Fatal("fresh canonical Restore did not import archived Host settings")
	}
	if len(a.observations) != 3 {
		t.Fatal("canonical Restore bypassed an extracted, locked or startup check")
	}
	for _, observed := range a.observations {
		if !maps.Equal(observed.settings, settings) || observed.inputs["library"] != "archived-library" {
			t.Fatal("canonical Restore lost its archived public context")
		}
	}
}

func TestPreflightContextRestoreFreshWrongRootDoesNotInitialize(t *testing.T) {
	a := newPreflightContextApp(t)
	h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
	p, _ := h.svc.Project()
	prepareContextLibrary(t, filepath.Join(p.Owner.Home, "data", "media"), "archived-library")
	root := filepath.Join(t.TempDir(), "unprepared-archived-root")
	archive := contextArchive(t, root)
	if err := h.svc.Restore(t.Context(), archive, nil, ""); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("fresh Restore used default settings instead of archived settings: %v", err)
	}
	pending, err := p.LoadRestoreStartup()
	if err != nil || pending != nil || p.Exists() || len(h.runner.Calls()) != 0 {
		t.Fatal("refused fresh Restore initialized settings, startup intent or runtime actions")
	}
	prepareContextLibrary(t, root, "archived-library")
	if err := h.svc.Restore(t.Context(), archive, nil, ""); err != nil {
		t.Fatalf("preparing the archived effective root did not recover Restore: %v", err)
	}
}

func TestPreflightContextInstallLockedAdmissionPrecedesMutations(t *testing.T) {
	for _, tc := range []struct {
		name          string
		existing      bool
		settingsDrift bool
		corrupt       bool
		ready         bool
	}{
		{name: "changed settings", settingsDrift: true},
		{name: "created same settings with changed inputs"},
		{name: "existing same settings with changed inputs", existing: true},
		{name: "unrecoverable locked context", corrupt: true},
		{name: "approved recovery and migrations", ready: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newPreflightContextApp(t)
			a.media = true
			h := newHarness(t, a, &testApp{id: "other", done: map[string]bool{}})
			p, err := h.svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			if tc.existing {
				if err := h.install("other"); err != nil {
					t.Fatal(err)
				}
				settings, err := p.LoadSettings()
				if err != nil {
					t.Fatal(err)
				}
				settings[settingNames] = namesOn
				settings["PUID"] = fmt.Sprint(os.Getuid())
				settings["PGID"] = fmt.Sprint(os.Getgid())
				if err := p.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(p.Owner.Home, "data", "media")
			prepareContextLibrary(t, root, "library")
			if tc.ready {
				prepareContextLibrary(t, root, "selected-library")
			}
			before := map[string][]byte{}
			beforeAdmin := h.rec.find("", "admin-login")
			beforeCompose := len(h.composeCalls())
			a.onEarly = func() {
				settings := maps.Clone(a.observations[0].settings)
				if tc.settingsDrift {
					settings["MEDIA_ROOT"] = filepath.Join(t.TempDir(), "missing-media")
				}
				if err := p.MkdirAll(); err != nil {
					t.Fatal(err)
				}
				if err := p.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
				if err := p.SaveSecrets(map[string]string{"ADMIN_PASSWORD": "synthetic-original-admin", "CONTEXT_PERSISTENT_KEY": "synthetic-original-key"}); err != nil {
					t.Fatal(err)
				}
				state := `{"version":1,"apps":{"context":{"port":9000,"inputs":{"library":"selected-library","token":"synthetic-legacy-private"}}}}` + "\ndamaged tail"
				if tc.corrupt {
					state = "unrecoverable-state"
				}
				for name, content := range map[string]string{
					"state.json":                          state,
					"state.json.corrupt":                  "keep-prior-recovery-copy",
					"compose.yml":                         "include:\n  - apps/context/compose.yml\n",
					"apps/context/compose.yml":            "services:\n  context:\n    image: example/test:latest\n",
					project.NamesFolder + "/runtime.json": "{\n  \"start_action\": \"install context\",\n  \"applied\": \"keep-until-approved\"\n}\n",
				} {
					if err := p.MkdirAll(filepath.Dir(name)); err != nil {
						t.Fatal(err)
					}
					if err := p.WriteHomeFile(name, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				for _, name := range []string{".env", "state.json", "state.json.corrupt", "compose.yml", "apps/context/compose.yml", project.NamesFolder + "/runtime.json"} {
					data, err := p.ReadHomeFile(name)
					if err != nil {
						t.Fatal(err)
					}
					before[name] = data
				}
			}
			opts := InstallOptions{Targets: []string{"context"}, EasyNames: true, Settings: map[string]string{"PUID": fmt.Sprint(os.Getuid()), "PGID": fmt.Sprint(os.Getgid())}, AdminPassword: "synthetic-proposed-admin", PrivateInputs: map[string]map[string]string{"context": {"token": "synthetic-proposed-private"}}}
			if tc.ready {
				opts.PrivateInputs = nil
				opts.AdminPassword = ""
			}
			err = h.svc.Install(t.Context(), opts)
			if tc.ready {
				if err != nil {
					t.Fatal(err)
				}
				kept, err := p.ReadHomeFile("state.json.corrupt")
				if err != nil || string(kept) != string(before["state.json"]) || !p.Canonical() {
					t.Fatal("approved Install bypassed state recovery or Compose migration")
				}
				state, err := p.LoadState()
				if err != nil || state.Apps["context"].Inputs["token"] != "" || state.Apps["context"].Inputs["library"] != "selected-library" {
					t.Fatal("approved Install skipped private-input migration or lost locked public inputs")
				}
				secrets, err := p.LoadSecrets()
				if err != nil || secrets["CONTEXT_TOKEN"] != "synthetic-legacy-private" || secrets["ADMIN_PASSWORD"] != "synthetic-original-admin" {
					t.Fatal("approved Install did not preserve the Admin login and migrate the legacy private input")
				}
				return
			}
			if tc.corrupt {
				if err == nil || !strings.Contains(err.Error(), "restore state.json from a backup") {
					t.Fatalf("unreadable locked context lost its truthful recovery error: %v", err)
				}
			} else {
				if !errors.Is(err, helpers.ErrManual) || len(a.observations) != 2 || !a.observations[1].early || a.observations[1].inputs["library"] != "selected-library" {
					t.Fatalf("locked public context was not reread before refusal: %v", err)
				}
				if !tc.settingsDrift && !maps.Equal(a.observations[0].settings, a.observations[1].settings) {
					t.Fatal("same-settings regression did not exercise identical Host settings")
				}
			}
			for name, original := range before {
				data, err := p.ReadHomeFile(name)
				if err != nil || string(data) != string(original) {
					t.Errorf("locked refusal modified %s before admission", name)
				}
			}
			if _, err := os.Stat(p.Path("compose.legacy.yml")); !errors.Is(err, os.ErrNotExist) {
				t.Error("locked refusal migrated Compose before admission")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "library" {
				t.Error("locked refusal prepared media directories before admission")
			}
			if len(h.composeCalls()) != beforeCompose || len(a.renders) != 0 || h.rec.find("context", "secret") != nil || !reflect.DeepEqual(beforeAdmin, h.rec.find("", "admin-login")) {
				t.Error("locked refusal reached Compose mutation, rendering or credential generation")
			}
		})
	}
}
