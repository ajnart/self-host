package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/host"
)

// OnboardingChecks inspects the proposed App inputs and storage before approval.
func (s *Service) OnboardingChecks(ctx context.Context, opts InstallOptions) []host.Check {
	return s.onboardingChecks(ctx, opts, nil)
}

func (s *Service) onboardingChecks(ctx context.Context, opts InstallOptions, reserved []host.PortRequirement) []host.Check {
	p, err := s.Project()
	if err != nil {
		return []host.Check{{ID: "disk", Title: "Disk space", Level: host.Fail,
			Detail: err.Error(), Hint: "Fix the Home folder, then press r to recheck."}}
	}
	var checks []host.Check
	if !p.Exists() {
		if err := host.UnraidInstall(s.Deps, p.Root); err != nil {
			checks = append(checks, host.Check{ID: "unraid-install", Title: "Unraid installation locations", Level: host.Fail, Detail: err.Error()})
		}
	}
	apps, err := s.Resolve(opts.Targets)
	if err != nil {
		return []host.Check{{ID: "plan", Title: "Selected Apps", Level: host.Fail, Detail: err.Error()}}
	}
	inputs, err := resolveInputs(apps, opts.Sets, nil)
	if err != nil {
		return []host.Check{{ID: "plan", Title: "Selected ports", Level: host.Fail, Detail: err.Error()}}
	}
	var ports []host.PortRequirement
	if opts.EasyNames {
		ports = append(ports, host.PortRequirement{Title: "Easy names at home port", Port: 80, Protocols: []string{"tcp"}})
	}
	for _, a := range apps {
		info, values := a.Info(), inputs[a.Info().ID]
		if info.Port > 0 {
			port, parseErr := plannedPort(values["port"], info.Port)
			if parseErr != nil {
				return []host.Check{{ID: "port", App: info.ID, Title: info.Name + " port", Level: host.Fail, Detail: parseErr.Error()}}
			}
			ports = append(ports, host.PortRequirement{App: info.ID, Title: info.Name + " port", Port: port,
				Input: "port", Protocols: []string{"tcp"}, Automatic: !inputWasSet(opts.Sets, info.ID, "port")})
		}
		for _, input := range a.Inputs() {
			if len(input.HostPortProtocols) == 0 {
				continue
			}
			port, parseErr := plannedPort(values[input.Key], 0)
			if parseErr != nil {
				return []host.Check{{ID: "port", App: info.ID, Title: info.Name + " " + input.Prompt, Level: host.Fail, Detail: parseErr.Error()}}
			}
			ports = append(ports, host.PortRequirement{App: info.ID, Title: info.Name + " " + input.Prompt, Port: port,
				Input: input.Key, Protocols: input.HostPortProtocols, Automatic: !inputWasSet(opts.Sets, info.ID, input.Key)})
		}
	}
	checks = append(checks, host.Onboarding(s.Deps, p.Root, len(apps), ports, reserved...)...)
	if p.Exists() {
		return checks
	}
	settings := s.detectSettings(ctx, p.Owner, opts.Exposure)
	applyInstallSettings(settings, opts)
	if check := accessCheck(settings); check.Level == host.Warn && settings["EXPOSURE"] != ExposureLocal {
		checks = append(checks, check)
	}
	if err := normalizeMediaSettings(settings); err != nil {
		return append(checks, host.Check{ID: "media-storage", Title: "Media storage", Level: host.Fail, Detail: err.Error()})
	}
	if err := s.ensureMediaIdentities(ctx, settings); err != nil {
		return append(checks, host.Check{ID: "media-users", Title: "Media file owners", Level: host.Fail,
			Detail: err.Error(), Hint: "Review separate Linux file owners in Where; existing files have not been changed."})
	}
	return append(checks, s.mediaHardlinkChecks(ctx, settings, apps)...)
}

func plannedPort(value string, fallback int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%q is not a port", value)
	}
	return port, nil
}
