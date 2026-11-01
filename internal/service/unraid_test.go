package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/host"
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

func TestDoctorPlanChecksAdditionsAgainstExistingProjectPortsWithoutMutation(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}}, &testApp{id: "two", done: map[string]bool{}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	h.deps.DiskUsage = func(string) (system.DiskUsage, error) {
		return system.DiskUsage{AvailableBytes: 20 << 30}, nil
	}
	statePath := filepath.Join(h.root(), "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(h.runner.Calls())
	checks := h.svc.DoctorPlan(context.Background(), InstallOptions{Targets: []string{"one", "two"}, Sets: []string{"two.port=9000"}})
	found := false
	for _, check := range checks {
		if check.ID == "port" && check.App == "two" && check.Level == host.Fail && strings.Contains(check.Detail, "9000") {
			found = true
		}
	}
	if !found {
		t.Fatalf("existing Project port was not reserved: %+v", checks)
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(before) != string(after) || len(h.runner.Calls()) != calls {
		t.Fatalf("planned checks mutated state or ran external commands: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root(), "apps", "two")); !os.IsNotExist(err) {
		t.Fatalf("planned checks created App files: %v", err)
	}
}

func TestDoctorPlanReservesExistingEasyNamesAndSecondaryPorts(t *testing.T) {
	h := newHarness(t, &portInputApp{testApp{id: "paired", done: map[string]bool{}}}, &testApp{id: "new", done: map[string]bool{}})
	if err := h.install("paired"); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	settings, err := p.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	settings[settingNames] = namesOn
	if err := p.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	h.deps.DiskUsage = func(string) (system.DiskUsage, error) {
		return system.DiskUsage{AvailableBytes: 20 << 30}, nil
	}
	for _, port := range []string{"80", "6881"} {
		checks := h.svc.DoctorPlan(context.Background(), InstallOptions{Targets: []string{"new"}, Sets: []string{"new.port=" + port}})
		if host.Worst(checks) != host.Fail || !strings.Contains(checksText(checks), "selected port "+port) {
			t.Fatalf("existing reserved port %s was not checked: %+v", port, checks)
		}
	}
}

func TestDoctorPlanAlternativeMatchesInstallWithStoppedSecondaryPortOwner(t *testing.T) {
	h := newHarness(t, &portInputApp{testApp{id: "paired", done: map[string]bool{}}}, &testApp{id: "new", done: map[string]bool{}})
	if err := h.svc.Install(context.Background(), InstallOptions{Targets: []string{"paired"}, Sets: []string{"paired.port=9002", "paired.peer-port=9000"}}); err != nil {
		t.Fatal(err)
	}
	h.deps.DiskUsage = func(string) (system.DiskUsage, error) {
		return system.DiskUsage{AvailableBytes: 20 << 30}, nil
	}
	checks := h.svc.DoctorPlan(context.Background(), InstallOptions{Targets: []string{"new"}})
	if !strings.Contains(checksText(checks), "install will use 9001 instead") {
		t.Fatalf("missing planned alternative: %+v", checks)
	}
	if err := h.install("new"); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	state, err := p.LoadState()
	if err != nil || state.Apps["new"] == nil || state.Apps["new"].Port != 9001 {
		t.Fatalf("installed port differs from approved alternative: %+v, %v", state, err)
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
