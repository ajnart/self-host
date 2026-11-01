package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stepstatus "github.com/ajnart/self-host/internal/status"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestUnraidInstallRefusesUnsafeLocationsBeforeCreatingProject(t *testing.T) {
	for _, tc := range []struct {
		name, homeFS, binaryFS, want string
		explicit, readOnly, noExec   bool
	}{
		{name: "implicit Home folder", homeFS: "xfs", binaryFS: "xfs", want: "SELF_HOST_HOME"},
		{name: "memory Home folder", explicit: true, homeFS: "tmpfs", binaryFS: "xfs", want: "memory-backed"},
		{name: "read-only Home folder", explicit: true, homeFS: "xfs", binaryFS: "xfs", readOnly: true, want: "read-only"},
		{name: "memory binary", explicit: true, homeFS: "xfs", binaryFS: "rootfs", want: "binary"},
		{name: "noexec binary", explicit: true, homeFS: "xfs", binaryFS: "xfs", noExec: true, want: "noexec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
			root := h.root()
			if tc.explicit {
				systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_HOME": root})
			}
			h.deps.UnraidRelease = func() ([]byte, error) { return []byte("version=\"7.2.0\"\n"), nil }
			h.deps.Storage = func(path string) (system.Storage, error) {
				storage := system.Storage{Path: path, Existing: path, MountPoint: "/mnt/pool", Type: tc.binaryFS, NoExec: tc.noExec}
				if path == root {
					storage.Type, storage.ReadOnly = tc.homeFS, tc.readOnly
				}
				return storage, nil
			}
			err := h.svc.Install(context.Background(), InstallOptions{Targets: []string{"one"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("refusal created the Home folder: %v", err)
			}
			if calls := h.runner.Calls(); len(calls) != 0 {
				t.Fatalf("refusal reached Docker: %v", calls)
			}
		})
	}
}

func TestUnraidExplicitLocationsReuseTheSameProjectInAFreshService(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}})
	root := filepath.Join(t.TempDir(), "persistent-home")
	systemtest.SetEnv(h.deps, map[string]string{"SELF_HOST_HOME": root})
	h.deps.UnraidRelease = func() ([]byte, error) { return []byte("version=\"7.2.0\"\n"), nil }
	h.deps.Storage = func(path string) (system.Storage, error) {
		return system.Storage{Path: path, Existing: filepath.Dir(path), MountPoint: "/mnt/pool", Type: "xfs"}, nil
	}
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	fresh := New(h.deps, h.svc.Log, h.svc.Catalog)
	p, err := fresh.Project()
	if err != nil || p.Root != root || !p.Exists() {
		t.Fatalf("fresh service lost the Project: %+v, %v", p, err)
	}
	state, err := p.LoadState()
	if err != nil || state.Apps["one"] == nil || state.Apps["one"].Setup != stepstatus.SetupOK {
		t.Fatalf("Setup state did not persist: %+v, %v", state, err)
	}
	for _, name := range []string{"compose.yml", "state.json", "apps/one/compose.yml"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("missing Project file %s: %v", name, err)
		}
	}
	if _, err := os.Stat(h.root()); !os.IsNotExist(err) {
		t.Fatalf("created the default Home folder despite override: %v", err)
	}
}
