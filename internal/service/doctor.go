package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/compose"
	"github.com/ajnart/self-host/internal/host"
)

// Doctor runs host checks, then per-App container, binding and LAN
// reachability checks when a Project exists.
func (s *Service) Doctor(ctx context.Context) []host.Check {
	checks := host.Docker(ctx, s.Deps)
	home := ""
	if p, err := s.Project(); err == nil {
		home = p.Root
	}
	checks = append(checks, host.Unraid(s.Deps, home)...)
	if host.Worst(checks) == host.Fail {
		return checks
	}
	ss, err := s.open(ctx)
	if err != nil {
		return checks
	}
	if err := ss.p.LockError(); err != nil {
		checks = append(checks, host.Check{ID: "project-lock", Title: "Project lock", Level: host.Fail, Detail: err.Error()})
	}
	checks = append(checks, ss.mediaHardlinkChecks(ctx, nil)...)
	if rootless, err := host.Rootless(ctx, s.Deps); err == nil && rootless && (ss.settings["PUID"] != "0" || ss.settings["PGID"] != "0") {
		checks = append(checks, host.Check{ID: "project-userns", Title: "Project container IDs", Level: host.Warn,
			Detail: fmt.Sprintf("rootless Docker maps host-user-owned folders to container 0:0, but this Project uses PUID=%s PGID=%s; Apps using these IDs may be unable to write bind mounts", ss.settings["PUID"], ss.settings["PGID"]),
			Hint:   "Review existing bind-mount ownership first; for host-user-owned folders use: self-host config set PUID=0 PGID=0. Images with fixed nonzero users and their backups need separate ownership/read access."})
	}
	status, err := s.Status(ctx)
	if err != nil {
		return append(checks, host.Check{ID: "project", Title: "Project", Level: host.Fail, Detail: err.Error()})
	}
	containers, _ := ss.compose.Ps(ctx)
	byService := map[string]compose.Container{}
	for _, c := range containers {
		byService[c.Service] = c
	}
	if status.Names != nil {
		check := host.Check{ID: "easy-names", Title: "Easy names at home", Level: host.OK, Detail: "Names resolve, checked from this computer, not your phone or TV."}
		if !status.Names.Working {
			check.Level, check.Detail, check.Hint = host.Warn, status.Names.Message, "Use the addresses with numbers; see self-host status --json for Technical details."
		}
		checks = append(checks, check)
	}

	lan := status.Exposure != ExposureLocal
	checks = append(checks, accessCheck(ss.settings))
	if lan {
		checks = append(checks, s.lanCheck(ss.settings))
	}
	var ports []string
	if ss.namesOn() {
		ports = append(ports, "80")
	}
	for _, app := range status.Apps {
		if app.Port != 0 {
			ports = append(ports, strconv.Itoa(app.Port))
		}
	}
	if lan {
		checks = append(checks, host.Firewall(ctx, s.Deps, strings.Join(ports, ",")))
	}

	for _, app := range status.Apps {
		checks = append(checks, s.appChecks(ctx, app, byService, lan)...)
	}
	checks = append(checks, ss.tailscaleChecks(ctx, byService)...)
	return checks
}

func (s *Service) DoctorPlan(ctx context.Context, opts InstallOptions) []host.Check {
	p, err := s.Project()
	failed := func(err error) []host.Check {
		return []host.Check{{ID: "plan", Title: "Planned ports", Level: host.Fail, Detail: err.Error()}}
	}
	if err != nil {
		return failed(err)
	}
	if !p.Exists() {
		return s.onboardingChecks(opts, nil)
	}
	apps, err := s.Resolve(opts.Targets)
	if err != nil {
		return failed(err)
	}
	installed, err := p.AppIDs()
	if err != nil {
		return failed(err)
	}
	state, err := p.LoadState()
	if err != nil {
		return failed(err)
	}
	settings, err := p.LoadSettings()
	if err != nil {
		return failed(err)
	}
	reserved, err := s.reservedPorts(p, state, settings, "")
	if err != nil {
		return failed(err)
	}
	if settings[settingNames] == namesOn {
		opts.EasyNames = false
	}
	opts.Targets = nil
	for _, a := range apps {
		if !slices.Contains(installed, a.Info().ID) {
			opts.Targets = append(opts.Targets, a.Info().ID)
		}
	}
	if len(opts.Targets) == 0 {
		return []host.Check{{ID: "plan", Title: "Planned Apps", Level: host.Warn,
			Detail: "all selected Apps are already installed", Hint: "Use plain Doctor for their current ports; planned checks inspect additions only."}}
	}
	return s.onboardingChecks(opts, reserved)
}

func (s *Service) appChecks(ctx context.Context, app AppStatus, byService map[string]compose.Container, lan bool) []host.Check {
	id := app.ID
	if app.State != "running" {
		return []host.Check{{ID: "running", App: id, Title: "Container", Level: host.Fail,
			Detail: "state " + app.State, Hint: "Start it with: self-host start " + id + " (logs: self-host logs " + id + ")"}}
	}
	checks := []host.Check{{ID: "running", App: id, Title: "Container", Level: host.OK, Detail: "running " + app.Health}}
	if app.Port == 0 {
		return checks
	}

	bind := ""
	for _, name := range append([]string{mainService(app)}, app.Services...) {
		for _, pub := range byService[name].Publishers {
			if pub.PublishedPort == app.Port || app.Port == 0 {
				bind = pub.URL
				break
			}
		}
		if bind != "" {
			break
		}
	}
	loopback := strings.HasPrefix(bind, "127.") || bind == "::1"
	switch {
	case bind == "":
		checks = append(checks, host.Check{ID: "binding", App: id, Title: "Port", Level: host.Warn,
			Detail: "no published port found", Hint: "The App is not reachable from outside Docker; check " + id + "'s ports in compose.yml."})
	case lan && loopback:
		checks = append(checks, host.Check{ID: "binding", App: id, Title: "Port", Level: host.Fail,
			Detail: fmt.Sprintf("port %d is bound to %s, so other devices cannot connect", app.Port, bind),
			Hint:   "Recreate it with the current Access: self-host start " + id})
	case !lan && !loopback:
		checks = append(checks, host.Check{ID: "binding", App: id, Title: "Port", Level: host.Warn,
			Detail: fmt.Sprintf("port %d is bound to %s although Access is this machine only", app.Port, bind),
			Hint:   "Recreate it with the current Access: self-host start " + id})
	default:
		checks = append(checks, host.Check{ID: "binding", App: id, Title: "Port", Level: host.OK,
			Detail: fmt.Sprintf("port %d bound to %s", app.Port, bind)})
	}

	url := app.observationURL()
	if url != "" {
		if err := host.Reachable(ctx, s.Deps.HTTP, url); err != nil {
			checks = append(checks, host.Check{ID: "reachable", App: id, Title: "Reachable", Level: host.Fail,
				Detail: err.Error(), Hint: "Check the App's logs (self-host logs " + id + ") and the firewall check above."})
		} else {
			checks = append(checks, host.Check{ID: "reachable", App: id, Title: "Reachable", Level: host.OK, Detail: "answers on " + url})
		}
	}
	return checks
}

func mainService(app AppStatus) string {
	if len(app.Services) == 1 {
		return app.Services[0]
	}
	return app.ID
}
