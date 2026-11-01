// Package host inspects the machine: Docker availability, Host settings and
// network reachability.
package host

import (
	"context"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/system"
)

type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
)

// Check is one doctor finding.
type Check struct {
	ID     string `json:"id"`
	App    string `json:"app,omitempty"`
	Input  string `json:"input,omitempty"`
	Port   int    `json:"port,omitempty"`
	Title  string `json:"title"`
	Level  Level  `json:"level"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
	Docs   string `json:"docs,omitempty"`
}

const (
	dockerDocs     = "https://docs.docker.com/engine/install/"
	orbstackDocs   = "https://orbstack.dev/download"
	postInstall    = "https://docs.docker.com/engine/install/linux-postinstall/"
	composeDocs    = "https://docs.docker.com/compose/install/"
	daemonDocs     = "https://docs.docker.com/engine/daemon/start/"
	orbstackDaemon = "https://docs.orbstack.dev/quick-start"
)

func installDocs(goos string) string {
	if goos == "darwin" {
		return orbstackDocs
	}
	return dockerDocs
}

// Docker checks that the docker CLI exists, that the daemon answers through
// the active context, and that Compose v2 is available. Later checks are
// skipped when an earlier one fails.
func Docker(ctx context.Context, deps *system.Deps) []Check {
	unraid := false
	if deps.GOOS == "linux" && deps.UnraidRelease != nil {
		data, err := deps.UnraidRelease()
		unraid = err == nil && unraidVersion.Match(data)
	}
	if _, err := deps.LookPath("docker"); err != nil {
		hint := "Install Docker Engine, then run self-host again."
		if deps.GOOS == "darwin" {
			hint = "Install OrbStack, start it once, then run self-host again."
		}
		if unraid {
			hint = "Check Docker availability in Unraid Settings before installing; self-host does not install Docker or plugins."
		}
		return []Check{{ID: "docker", Title: "Docker", Level: Fail, Detail: "docker command not found", Hint: hint, Docs: installDocs(deps.GOOS)}}
	}
	checks := []Check{{ID: "docker", Title: "Docker", Level: OK, Detail: "docker command found"}}

	probe := ProbeDocker(ctx, deps)
	version, err := probe.Result, probe.Err
	switch {
	case probe.SocketDenied():
		return append(checks, socketDeniedCheck())
	case err != nil:
		return append(checks, Check{ID: "docker-socket", Title: "Docker daemon", Level: Fail, Detail: err.Error(), Docs: daemonDocs})
	case version.ExitCode != 0:
		docs, hint := daemonDocs, "Start the Docker daemon, then run self-host again."
		if deps.GOOS == "darwin" {
			docs, hint = orbstackDaemon, "Open OrbStack so the Docker engine starts, then run self-host again."
		}
		return append(checks, Check{ID: "docker-socket", Title: "Docker daemon", Level: Fail,
			Detail: firstLine(string(version.Stderr)), Hint: hint, Docs: docs})
	}
	checks = append(checks, Check{ID: "docker-socket", Title: "Docker daemon", Level: OK,
		Detail: "engine " + strings.TrimSpace(string(version.Stdout))})

	compose, err := deps.Runner.Run(ctx, system.Cmd{Name: "docker", Args: []string{"compose", "version", "--short"}})
	short := strings.TrimPrefix(strings.TrimSpace(string(compose.Stdout)), "v")
	if unraid && (err != nil || compose.ExitCode != 0 || !composeIncludes(short)) {
		return append(checks, Check{ID: "compose", Title: "Docker Compose", Level: Fail,
			Detail: "docker compose with include support is required; reported version: " + short,
			Hint:   "Use Docker Compose 2.20.0 or later with include support, available as docker compose on this Unraid version. Check the operator's version-specific installation method; no particular third-party management plugin is required or installed by self-host.",
			Docs:   unraidDocs})
	}
	if err != nil || compose.ExitCode != 0 || majorVersion(short) < 2 {
		return append(checks, Check{ID: "compose", Title: "Docker Compose", Level: Fail,
			Detail: "Docker Compose v2 plugin not available",
			Hint:   "Install the Docker Compose v2 plugin (docker compose, not docker-compose).",
			Docs:   composeDocs})
	}
	checks = append(checks, Check{ID: "compose", Title: "Docker Compose", Level: OK, Detail: "compose " + short})
	rootless, err := Rootless(ctx, deps)
	if err != nil {
		return append(checks, Check{ID: "docker-userns", Title: "Container IDs", Level: Warn,
			Detail: "Docker user namespace could not be determined: " + err.Error(),
			Hint:   "Inspect docker info before choosing PUID/PGID for bind-mounted folders.", Docs: rootlessDocs})
	}
	detail := "rootless mode is not enabled; container IDs default to the Project owner's UID/GID"
	if rootless {
		detail = "rootless Docker: container 0:0 maps to the daemon's host user; new Projects default to PUID=0 and PGID=0. Images with fixed nonzero users need separate bind-mount ownership; backups may require additional read access"
	}
	return append(checks, Check{ID: "docker-userns", Title: "Container IDs", Level: OK, Detail: detail, Docs: rootlessDocs})
}

func composeIncludes(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false
	}
	major := majorVersion(version)
	minor, err := strconv.Atoi(parts[1])
	return err == nil && (major > 2 || major == 2 && minor >= 20)
}

func majorVersion(v string) int {
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// Worst returns the most severe level among checks.
func Worst(checks []Check) Level {
	worst := OK
	for _, c := range checks {
		if c.Level == Fail {
			return Fail
		}
		if c.Level == Warn {
			worst = Warn
		}
	}
	return worst
}
