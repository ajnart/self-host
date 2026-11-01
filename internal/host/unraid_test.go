package host_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func TestUnraidChecksUseReleaseAndResolvedStorageEvidence(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.UnraidRelease = func() ([]byte, error) { return []byte("version=\"7.2.0\"\n"), nil }
	deps.Storage = func(path string) (system.Storage, error) {
		if path != "/root/.self-host" {
			t.Fatalf("inspected %q", path)
		}
		return system.Storage{Path: "/root/.self-host", Existing: "/root", MountPoint: "/", Type: "rootfs"}, nil
	}
	checks := host.Unraid(deps, "/root/.self-host")
	if len(checks) != 2 || checks[0].ID != "unraid" || !strings.Contains(checks[0].Detail, "7.2.0") || !strings.Contains(checks[0].Detail, "/etc/unraid-version") {
		t.Fatalf("missing release evidence: %+v", checks)
	}
	if checks[1].ID != "unraid-home" || checks[1].Level != host.Warn || !strings.Contains(checks[1].Detail, "memory-backed") || !strings.Contains(checks[1].Hint, "SELF_HOST_HOME") {
		t.Fatalf("missing storage warning: %+v", checks)
	}
	if !strings.Contains(checks[0].Detail, "unverified") {
		t.Fatalf("detection must not claim tested support: %+v", checks[0])
	}
}

func TestUnraidComposeRequiresIncludeWithoutMandatingManagementPlugin(t *testing.T) {
	for _, tc := range []struct {
		version string
		level   host.Level
	}{
		{"2.19.1", host.Fail},
		{"2.20.0", host.OK},
		{"5.5.1", host.OK},
	} {
		t.Run(tc.version, func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			deps.UnraidRelease = func() ([]byte, error) { return []byte("version=\"7.2.0\"\n"), nil }
			deps.Runner.(*systemtest.Runner).On("docker compose version", system.Result{Stdout: []byte(tc.version)})
			for _, check := range host.Docker(context.Background(), deps) {
				if check.ID == "compose" {
					if check.Level != tc.level {
						t.Fatalf("compose check = %+v", check)
					}
					if check.Level == host.Fail && (!strings.Contains(check.Hint, "include") || !strings.Contains(check.Hint, "Unraid")) {
						t.Fatalf("missing capability guidance: %+v", check)
					}
					return
				}
			}
			t.Fatal("missing Compose check")
		})
	}
}

func TestUncertainUnraidEvidenceStillRequiresComposeInclude(t *testing.T) {
	for _, read := range []func() ([]byte, error){
		func() ([]byte, error) { return nil, os.ErrPermission },
		func() ([]byte, error) { return []byte("version=unrecognized\n"), nil },
	} {
		deps := systemtest.NewDeps(t)
		deps.UnraidRelease = read
		deps.Runner.(*systemtest.Runner).On("docker compose version", system.Result{Stdout: []byte("2.19.1")})
		if checks := host.Docker(context.Background(), deps); host.Worst(checks) != host.Fail {
			t.Fatalf("uncertain identity bypassed Compose compatibility: %+v", checks)
		}
	}
}

func TestUnraidDoesNotClassifyGenericLinuxOrMacOS(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			deps := systemtest.NewDeps(t)
			deps.GOOS = goos
			deps.UnraidRelease = func() ([]byte, error) { return nil, os.ErrNotExist }
			deps.Storage = func(string) (system.Storage, error) {
				t.Fatal("inspected storage without Unraid evidence")
				return system.Storage{}, nil
			}
			if checks := host.Unraid(deps, "/root/.self-host"); len(checks) != 0 {
				t.Fatalf("classified %s as Unraid: %+v", goos, checks)
			}
		})
	}
}
