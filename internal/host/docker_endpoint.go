package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/ajnart/self-host/internal/system"
)

// DockerEndpoint is the active Docker CLI endpoint. Host is available to
// callers that need the socket source, but reports expose only Kind so local
// paths and remote hostnames do not leak.
type DockerEndpoint struct {
	Host string
	Kind string
}

type DesktopDockerAccess struct {
	SocketDenied bool `json:"socket_denied"`
	CanAuthorize bool `json:"can_authorize"`
}

// DesktopDocker checks whether the selected Docker endpoint can be reached
// with authorization for the default system socket.
func DesktopDocker(ctx context.Context, deps *system.Deps) DesktopDockerAccess {
	access := DesktopDockerAccess{SocketDenied: ProbeDocker(ctx, deps).SocketDenied()}
	if !access.SocketDenied || deps.Getenv("DOCKER_CONTEXT") != "" {
		return access
	}
	if deps.Getenv("DOCKER_HOST") == "" {
		current, err := deps.Runner.Run(ctx, system.Cmd{Name: "docker", Args: []string{"context", "show"}})
		if err != nil || current.ExitCode != 0 || strings.TrimSpace(string(current.Stdout)) != "default" {
			return access
		}
	}
	endpoint, err := ActiveDockerEndpoint(ctx, deps)
	access.CanAuthorize = err == nil && endpoint.Host == "unix:///var/run/docker.sock"
	return access
}

// ActiveDockerEndpoint resolves the endpoint with Docker CLI precedence:
// DOCKER_CONTEXT selects a context even when DOCKER_HOST is also set.
func ActiveDockerEndpoint(ctx context.Context, deps *system.Deps) (DockerEndpoint, error) {
	contextName := deps.Getenv("DOCKER_CONTEXT")
	if contextName == "" {
		if dockerHost := strings.TrimSpace(deps.Getenv("DOCKER_HOST")); dockerHost != "" {
			return dockerEndpoint(dockerHost), nil
		}
	}
	args := []string{"context", "inspect", "--format", `{{(index .Endpoints "docker").Host}}`}
	if contextName != "" {
		args = append(args, contextName)
	}
	result, err := deps.Runner.Run(ctx, system.Cmd{Name: "docker", Args: args})
	if err != nil {
		return DockerEndpoint{}, err
	}
	if result.ExitCode != 0 {
		return DockerEndpoint{}, fmt.Errorf("docker context inspect exited %d", result.ExitCode)
	}
	host := strings.TrimSpace(string(result.Stdout))
	if host == "" {
		return DockerEndpoint{}, fmt.Errorf("active Docker context has no endpoint")
	}
	return dockerEndpoint(host), nil
}

func dockerEndpoint(host string) DockerEndpoint {
	scheme, _, found := strings.Cut(host, "://")
	if !found {
		scheme = "unknown"
	}
	switch scheme {
	case "unix", "tcp", "ssh", "npipe":
	default:
		scheme = "unknown"
	}
	return DockerEndpoint{Host: host, Kind: scheme}
}
