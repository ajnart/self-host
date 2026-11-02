package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/ajnart/self-host/internal/system"
)

type containerInfo struct {
	ID              string `json:"id"`
	Image           string `json:"image"`
	ConfiguredImage string `json:"configuredImage"`
	Service         string `json:"service"`
	Project         string `json:"project"`
	Memory          int64  `json:"memory"`
	NanoCPUs        int64  `json:"nanoCPUs"`
	Pids            int64  `json:"pids"`
}

const inspectContainer = `{"id":{{json .Id}},"image":{{json .Image}},"configuredImage":{{json .Config.Image}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"memory":{{json .HostConfig.Memory}},"nanoCPUs":{{json .HostConfig.NanoCpus}},"pids":{{json .HostConfig.PidsLimit}}}`

var dockerID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
var imageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func requireCaps(ctx context.Context, runner system.Runner) error {
	data, err := docker(ctx, runner, "info", "--format", `{{json .}}`)
	if err != nil {
		return err
	}
	var caps struct {
		MemoryLimit bool `json:"MemoryLimit"`
		CPUQuota    bool `json:"CpuCfsQuota"`
		PidsLimit   bool `json:"PidsLimit"`
	}
	if json.Unmarshal(data, &caps) != nil || !caps.MemoryLimit || !caps.CPUQuota || !caps.PidsLimit {
		return startupError(StartupReasonCapabilities, "testkit requires Docker memory, CPU quota and PID limit support")
	}
	return nil
}

func docker(ctx context.Context, runner system.Runner, args ...string) ([]byte, error) {
	result, err := runner.Run(ctx, system.Cmd{Name: "docker", Args: args})
	if err != nil || result.ExitCode != 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, startupError(StartupReasonDocker, "testkit Docker operation failed")
	}
	return result.Stdout, nil
}

func resourceIDs(ctx context.Context, runner system.Runner, name, kind string) ([]string, error) {
	args := []string{"ps", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + name}
	if kind == "network" {
		args = []string{"network", "ls", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=" + name}
	}
	data, err := docker(ctx, runner, args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(data))
	for _, id := range ids {
		if !dockerID.MatchString(id) {
			return nil, startupError(StartupReasonOwnership, "invalid owned Docker identity")
		}
	}
	return ids, nil
}

func readContainer(ctx context.Context, runner system.Runner, id, name string) (containerInfo, error) {
	var info containerInfo
	data, err := docker(ctx, runner, "container", "inspect", "--format", inspectContainer, id)
	if err != nil {
		return info, err
	}
	if json.Unmarshal(data, &info) != nil || info.Project != name || !dockerID.MatchString(info.ID) {
		return info, startupError(StartupReasonOwnership, "testkit could not prove container ownership")
	}
	return info, nil
}

func (r *guardedRunner) verify(ctx context.Context) error {
	ids, err := resourceIDs(ctx, r.base, r.p.ComposeName, "container")
	if err != nil {
		return err
	}
	if len(ids) > 5 {
		return startupError(StartupReasonLimits, "testkit container ceiling exceeded")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		info, err := readContainer(ctx, r.base, id, r.p.ComposeName)
		if err != nil {
			return err
		}
		if !slices.Contains(r.plan.Services, info.Service) || seen[info.Service] || info.Memory <= 0 || info.Memory > 1<<30 || info.NanoCPUs <= 0 || info.NanoCPUs > 1e9 || info.Pids <= 0 || info.Pids > 512 || info.ConfiguredImage != r.plan.Images[info.Service] || !imageID.MatchString(info.Image) {
			return startupError(StartupReasonLimits, "testkit could not verify resource caps or selected image")
		}
		seen[info.Service] = true
		if info.Service == r.policy.Service {
			r.main = info
		}
	}
	return nil
}

func inactive(ctx context.Context, runner system.Runner, name string, remove bool) error {
	for _, kind := range []string{"container", "network"} {
		ids, err := resourceIDs(ctx, runner, name, kind)
		if err != nil {
			return err
		}
		if remove {
			for _, id := range ids {
				if kind == "container" {
					if _, err := readContainer(ctx, runner, id, name); err != nil {
						return err
					}
					if _, err := docker(ctx, runner, "container", "rm", "--force", "--volumes", id); err != nil {
						return err
					}
				} else {
					data, err := docker(ctx, runner, "network", "inspect", "--format", `{{index .Labels "com.docker.compose.project"}}`, id)
					if err != nil || strings.TrimSpace(string(data)) != name {
						return errors.New("testkit network ownership unproven")
					}
					if _, err := docker(ctx, runner, "network", "rm", id); err != nil {
						return err
					}
				}
			}
			ids, err = resourceIDs(ctx, runner, name, kind)
			if err != nil {
				return err
			}
		}
		if len(ids) != 0 {
			return errors.New("testkit resources remain; private recovery marker retained")
		}
	}
	return nil
}
