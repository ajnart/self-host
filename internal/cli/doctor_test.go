package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ajnart/self-host/internal/cli"
	"github.com/ajnart/self-host/internal/system"
	"github.com/ajnart/self-host/internal/systemtest"
)

func decodeEvents(t *testing.T, out string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line is not JSON: %q", line)
		}
		events = append(events, event)
	}
	return events
}

func findCheck(events []map[string]any, id string) map[string]any {
	for _, event := range events {
		if event["step"] == id && strings.HasPrefix(event["event"].(string), "step.") && event["event"] != "step.start" {
			return event
		}
	}
	return nil
}

func run(t *testing.T, deps *system.Deps, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	deps.Out = &out
	deps.Err = &out
	code := cli.Run(context.Background(), deps, args)
	return code, out.String()
}

func TestDoctorReportsMissingDockerWithDocsLink(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.LookPath = func(string) (string, error) { return "", system.ErrNotFound }

	code, out := run(t, deps, "doctor", "--json")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1\n%s", code, out)
	}
	check := findCheck(decodeEvents(t, out), "docker")
	if check == nil || check["event"] != "step.fail" {
		t.Fatalf("missing failed docker check:\n%s", out)
	}
	if check["docs"] != "https://docs.docker.com/engine/install/" {
		t.Fatalf("docs = %v", check["docs"])
	}
}

func TestDoctorPointsMacUsersToOrbStack(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.GOOS = "darwin"
	deps.LookPath = func(string) (string, error) { return "", system.ErrNotFound }

	_, out := run(t, deps, "doctor", "--json")

	check := findCheck(decodeEvents(t, out), "docker")
	if check["docs"] != "https://orbstack.dev/download" {
		t.Fatalf("docs = %v", check["docs"])
	}
}

func TestDoctorReportsSocketPermissionDenied(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.Runner.(*systemtest.Runner).On("docker version", system.Result{ExitCode: 1,
		Stderr: []byte("permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock")})

	code, out := run(t, deps, "doctor", "--json")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	check := findCheck(decodeEvents(t, out), "docker-socket")
	if check == nil || check["event"] != "step.fail" {
		t.Fatalf("missing failed socket check:\n%s", out)
	}
	if check["docs"] != "https://docs.docker.com/engine/install/linux-postinstall/" {
		t.Fatalf("docs = %v", check["docs"])
	}
	if hint, _ := check["hint"].(string); !strings.Contains(hint, "sudo usermod -aG docker $USER") || !strings.Contains(hint, `sudo "$(command -v self-host)"`) {
		t.Fatalf("hint = %v", check["hint"])
	}
}

func TestDoctorRequiresComposeV2(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.Runner.(*systemtest.Runner).On("docker compose version", system.Result{ExitCode: 1,
		Stderr: []byte("docker: 'compose' is not a docker command.")})

	code, out := run(t, deps, "doctor", "--json")

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if check := findCheck(decodeEvents(t, out), "compose"); check == nil || check["event"] != "step.fail" {
		t.Fatalf("missing failed compose check:\n%s", out)
	}
}

func TestDoctorPassesOnHealthyHost(t *testing.T) {
	deps := systemtest.NewDeps(t)

	code, out := run(t, deps, "doctor", "--json")

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", code, out)
	}
	events := decodeEvents(t, out)
	for _, id := range []string{"docker", "docker-socket", "compose"} {
		if check := findCheck(events, id); check == nil || check["event"] != "step.ok" {
			t.Fatalf("check %s not ok:\n%s", id, out)
		}
	}
	if events[0]["event"] != "run.start" || events[len(events)-1]["event"] != "run.end" {
		t.Fatalf("run not framed by run.start/run.end:\n%s", out)
	}
}

func TestDoctorHumanOutputShowsHints(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.LookPath = func(string) (string, error) { return "", system.ErrNotFound }

	_, out := run(t, deps, "doctor")

	if !strings.Contains(out, "✗") || !strings.Contains(out, "https://docs.docker.com/engine/install/") {
		t.Fatalf("human output lacks failure mark or docs link:\n%s", out)
	}
}

func TestDoctorUnraidFindingsSurviveMissingProjectAndDocker(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		deps := systemtest.NewDeps(t)
		root := filepath.Join(t.TempDir(), "not-created")
		systemtest.SetEnv(deps, map[string]string{"SELF_HOST_HOME": root})
		deps.UnraidRelease = func() ([]byte, error) { return []byte("version=\"7.2.0\"\n"), nil }
		deps.Storage = func(path string) (system.Storage, error) {
			if path != root {
				t.Fatalf("inspected %q, want override %q", path, root)
			}
			return system.Storage{Path: path, Existing: filepath.Dir(path), MountPoint: "/", Type: "tmpfs"}, nil
		}
		deps.FileSystem = func(string) (system.FileSystem, error) {
			t.Fatal("Doctor used the mutating filesystem probe")
			return system.FileSystem{}, nil
		}
		deps.LookPath = func(string) (string, error) { return "", system.ErrNotFound }
		args := []string{"doctor"}
		if jsonOutput {
			args = append(args, "--json")
		}
		code, out := run(t, deps, args...)
		if code != 1 || !strings.Contains(out, "7.2.0") || !strings.Contains(out, "memory-backed") || !strings.Contains(out, root) {
			t.Fatalf("missing findings: %d\n%s", code, out)
		}
		if jsonOutput {
			for _, id := range []string{"unraid", "unraid-home"} {
				if check := findCheck(decodeEvents(t, out), id); check == nil || check["event"] != "step.warn" {
					t.Fatalf("missing warning %s:\n%s", id, out)
				}
			}
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("Doctor created the Home folder: %v", err)
		}
		if calls := deps.Runner.(*systemtest.Runner).Calls(); len(calls) != 0 {
			t.Fatalf("unexpected external commands: %v", calls)
		}
	}
}

func TestDoctorChecksPlannedPortsWithoutCreatingProject(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.DiskUsage = func(string) (system.DiskUsage, error) {
		return system.DiskUsage{AvailableBytes: 20 << 30}, nil
	}
	deps.BindTCP = func(_ string, address string) error {
		if strings.HasSuffix(address, ":80") || strings.HasSuffix(address, ":443") {
			return syscall.EADDRINUSE
		}
		return nil
	}
	code, out := runWith(t, deps, testCatalog(&echoApp{id: "one"}), "doctor", "one", "--set", "one.port=443", "--easy-names", "--json")
	if code != 1 || !strings.Contains(out, "selected port 443") || !strings.Contains(out, "port 80") {
		t.Fatalf("planned conflicts missing: %d\n%s", code, out)
	}
	owner, _ := deps.Owner()
	if _, err := os.Stat(filepath.Join(owner.Home, ".self-host")); !os.IsNotExist(err) {
		t.Fatalf("planned Doctor created a Project: %v", err)
	}
	for _, call := range deps.Runner.(*systemtest.Runner).Calls() {
		if !strings.HasPrefix(call, "docker version ") && !strings.HasPrefix(call, "docker compose version ") && !strings.HasPrefix(call, "docker info ") {
			t.Fatalf("unexpected command: %s", call)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	deps := systemtest.NewDeps(t)
	deps.Version = "1.2.3"

	code, out := run(t, deps, "--version")

	if code != 0 || !strings.Contains(out, "1.2.3") {
		t.Fatalf("code=%d out=%q", code, out)
	}
}
