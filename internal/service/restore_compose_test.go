package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/project"
	"github.com/ajnart/self-host/internal/systemtest"
)

type restoreBindApp struct{ *testApp }

func (a restoreBindApp) Compose(rc app.RenderContext) string {
	return a.testApp.Compose(rc) + "    volumes:\n      - ./config:/config\n"
}

func TestRestoreCanonicalComposePreservesUnrelatedManualEditsAndRelativeData(t *testing.T) {
	h := newHarness(t,
		restoreBindApp{&testApp{id: "one", image: "example/one:v1", done: map[string]bool{}}},
		restoreBindApp{&testApp{id: "two", image: "example/two:v1", done: map[string]bool{}}})
	if err := h.install("one", "two"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	if err := p.WriteAppFile("one", "config/db", []byte("archived-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := h.svc.Backup(context.Background(), []string{"one"}, filepath.Join(t.TempDir(), "one.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := p.ReadCompose()
	current := "# Keep this manual comment\n" + strings.ReplaceAll(strings.ReplaceAll(string(root), "example/one:v1", "example/one:v2"), "example/two:v1", "example/two:v2")
	if err := p.WriteCompose([]byte(current)); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteAppFile("one", "config/db", []byte("newer-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Restore(context.Background(), file, []string{"one"}, ""); err != nil {
		t.Fatal(err)
	}
	root, _ = p.ReadCompose()
	for _, want := range []string{"example/one:v1", "example/two:v2", "# Keep this manual comment", "./apps/one/config:/config"} {
		if !strings.Contains(string(root), want) {
			t.Fatalf("restored Compose lost %q", want)
		}
	}
	if strings.Contains(string(root), "apps/one/apps/one") || strings.Contains(string(root), "include:") {
		t.Fatal("canonical restoration rebased paths twice or restored includes")
	}
	data, _ := os.ReadFile(p.Path("apps", "one", "config", "db"))
	if string(data) != "archived-data" {
		t.Fatal("selected configuration data was not restored")
	}
}

func TestFreshRestoreCanonicalManualServiceKeepsGlobalsAndEnvironment(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	root, _ := p.ReadCompose()
	current := strings.Replace(string(root), "services:\n", "services:\n  manual:\n    image: example/manual:v1\n    environment:\n      TOKEN: ${MANUAL_TOKEN}\n    volumes:\n      - manual-data:/data\n", 1)
	current += "volumes:\n  manual-data:\n    name: keep-manual-volume\nx-user-notes:\n  purpose: keep this\n"
	if err := p.WriteCompose([]byte(current)); err != nil {
		t.Fatal(err)
	}
	env, _ := p.ReadHomeFile(".env")
	env = append(env, []byte("\n# Keep this interpolation value\nMANUAL_TOKEN='fixture-manual-token'\n")...)
	if err := p.WriteHomeFile(".env", env, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := h.svc.Backup(context.Background(), nil, filepath.Join(t.TempDir(), "all.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p.Root); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Restore(context.Background(), file, nil, ""); err != nil {
		t.Fatal(err)
	}
	gotEnv, _ := p.ReadHomeFile(".env")
	if string(gotEnv) != string(env) {
		t.Fatal("fresh Restore did not preserve private interpolation values and comments")
	}
	root, _ = p.ReadCompose()
	for _, want := range []string{"manual:", "manual-data:", "name: keep-manual-volume", "x-user-notes:", "purpose: keep this"} {
		if !strings.Contains(string(root), want) {
			t.Fatalf("fresh Restore lost %q", want)
		}
	}
	if !p.IsExtra("manual") {
		t.Fatal("restored manual service lost Unmanaged app ownership")
	}
}

func TestLegacyRestoreExtraAppRebasesItsOriginalDataOnce(t *testing.T) {
	h := newHarness(t)
	p, _ := h.svc.Project()
	file := filepath.Join(t.TempDir(), "legacy.tar.zst")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := backup.Create(f, "", backup.Manifest{Version: 1, Apps: []backup.ManifestApp{{ID: "manual", Extra: true}}})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		".env":                     "TZ=UTC\nPUID=1000\nPGID=1000\nUMASK=022\nEXPOSURE=local\nLAN_IP=127.0.0.1\n",
		"secrets.env":              "ADMIN_USER=fixture-admin\nADMIN_PASSWORD=fixture-password\n",
		"state.json":               `{"version":1,"apps":{}}`,
		"extra/manual/compose.yml": "services:\n  manual:\n    image: example/manual:v1\n    volumes:\n      - ./config:/config\n",
		"extra/manual/config/db":   "legacy-data",
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
	if err := h.svc.Restore(context.Background(), file, nil, ""); err != nil {
		t.Fatal(err)
	}
	root, _ := p.ReadCompose()
	if !p.IsExtra("manual") || !strings.Contains(string(root), "./extra/manual/config:/config") || strings.Contains(string(root), "extra/manual/extra/manual") {
		t.Fatal("legacy Unmanaged app lost its folder ownership or original relative data path")
	}
	data, _ := os.ReadFile(p.Path(project.ExtraDirName, "manual", "config", "db"))
	if string(data) != "legacy-data" {
		t.Fatal("legacy data was not restored")
	}
}

func TestFreshRestoreScopedCanonicalBackupDoesNotRestoreOtherServices(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}}, &testApp{id: "two", done: map[string]bool{}})
	if err := h.install("one", "two"); err != nil {
		t.Fatal(err)
	}
	p, _ := h.svc.Project()
	file, err := h.svc.Backup(context.Background(), []string{"one"}, filepath.Join(t.TempDir(), "one.tar.zst"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(p.Root); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Restore(context.Background(), file, nil, ""); err != nil {
		t.Fatal(err)
	}
	ids, err := p.AppIDs()
	if err != nil || len(ids) != 1 || ids[0] != "one" {
		t.Fatalf("fresh scoped Restore brought back Apps without their data: %v, %v", ids, err)
	}
}

func TestRestoreCustomizedComposeStillKeepsManualEditsOnUpdate(t *testing.T) {
	for _, missingHash := range []bool{false, true} {
		t.Run(map[bool]string{false: "recorded-generation", true: "old-record-without-hash"}[missingHash], func(t *testing.T) {
			a := &testApp{id: "one", image: "example/one:v1", done: map[string]bool{}}
			h := newHarness(t, a)
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			p, _ := h.svc.Project()
			root, err := p.ReadCompose()
			if err != nil {
				t.Fatal(err)
			}
			custom := strings.Replace(string(root), "image: example/one:v1", "image: example/one:manual # Keep my pinned image\n    environment:\n      USER_SETTING: preserve-this", 1)
			if err := p.WriteCompose([]byte(custom)); err != nil {
				t.Fatal(err)
			}
			if missingHash {
				state, err := p.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				state.Apps["one"].FragmentSHA256 = ""
				if err := p.SaveState(state); err != nil {
					t.Fatal(err)
				}
			}
			file, err := h.svc.Backup(context.Background(), nil, filepath.Join(t.TempDir(), "custom.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := h.svc.Restore(context.Background(), file, nil, ""); err != nil {
				t.Fatal(err)
			}
			a.image = "example/one:v2"
			if err := h.svc.Update(context.Background(), []string{"one"}); err != nil {
				t.Fatal(err)
			}
			root, err = p.ReadCompose()
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"example/one:manual", "# Keep my pinned image", "USER_SETTING: preserve-this"} {
				if !strings.Contains(string(root), want) {
					t.Fatalf("Update overwrote restored manual Compose content %q", want)
				}
			}
			if strings.Contains(string(root), "example/one:v2") {
				t.Fatal("restoring a customization incorrectly marked it as generated")
			}
		})
	}
}

func TestFreshRestoreKeepsArchivedComposeNameUnlessExplicitlyOverridden(t *testing.T) {
	for _, override := range []string{"", "chosen-target"} {
		t.Run("override-"+override, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
			systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_COMPOSE_PROJECT": "original-project"})
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			p, _ := h.svc.Project()
			file, err := h.svc.Backup(context.Background(), nil, filepath.Join(t.TempDir(), "name.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(p.Root); err != nil {
				t.Fatal(err)
			}
			systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_COMPOSE_PROJECT": override})
			if err := h.svc.Restore(context.Background(), file, nil, ""); err != nil {
				t.Fatal(err)
			}
			p, err = h.svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			want := "original-project"
			if override != "" {
				want = override
			}
			name, err := p.StoredComposeName()
			if err != nil || name != want || p.ComposeName != want {
				t.Fatalf("restored Project identity = %q / %q, want %q: %v", name, p.ComposeName, want, err)
			}
		})
	}
}

func TestRestoreManualServiceRestoresOnlyItsReferencedEnvironment(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "private-reference", true: "shared-conflict"}[shared], func(t *testing.T) {
			h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			p, _ := h.svc.Project()
			root, _ := p.ReadCompose()
			current := strings.Replace(string(root), "services:\n", "services:\n  manual:\n    image: example/manual:v1\n    environment:\n      TOKEN: ${MANUAL_TOKEN}\n      OPTIONAL: ${OPTIONAL:-default}\n", 1)
			if shared {
				current = strings.Replace(current, "services:\n", "services:\n  neighbor:\n    image: example/neighbor:v1\n    environment:\n      TOKEN: ${MANUAL_TOKEN}\n", 1)
			}
			if err := p.WriteCompose([]byte(current)); err != nil {
				t.Fatal(err)
			}
			secrets, err := p.LoadSecrets()
			if err != nil {
				t.Fatal(err)
			}
			secrets["MANUAL_TOKEN"] = "fixture-old-token"
			secrets["UNRELATED_TOKEN"] = "fixture-unrelated-old"
			if err := p.SaveSecrets(secrets); err != nil {
				t.Fatal(err)
			}
			file, err := h.svc.Backup(context.Background(), []string{"manual"}, filepath.Join(t.TempDir(), "manual.tar.zst"), "")
			if err != nil {
				t.Fatal(err)
			}
			secrets["MANUAL_TOKEN"] = "fixture-new-token"
			secrets["UNRELATED_TOKEN"] = "fixture-unrelated-new"
			if err := p.SaveSecrets(secrets); err != nil {
				t.Fatal(err)
			}
			beforeCalls := len(h.runner.Calls())
			err = h.svc.Restore(context.Background(), file, []string{"manual"}, "")
			if shared {
				if err == nil || !strings.Contains(err.Error(), "used by another App") {
					t.Fatalf("shared credential change should fail preflight: %v", err)
				}
				for _, call := range h.runner.Calls()[beforeCalls:] {
					if strings.Contains(call, " rm ") || strings.Contains(call, " up ") || strings.Contains(call, " stop ") {
						t.Fatal("shared environment conflict changed containers")
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := p.LoadSecrets()
			if err != nil {
				t.Fatal(err)
			}
			want := "fixture-old-token"
			if shared {
				want = "fixture-new-token"
			}
			if after["MANUAL_TOKEN"] != want || after["UNRELATED_TOKEN"] != "fixture-unrelated-new" {
				t.Fatal("Restore lost the selected credential or changed unrelated environment values")
			}
		})
	}
}
