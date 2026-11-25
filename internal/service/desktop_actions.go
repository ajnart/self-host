package service

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/ajnart/self-host/internal/compose"
)

// DesktopStatus is the Project observation consumed by desktop launchers.
// It contains only the fields needed to present Apps and their shortcuts.
type DesktopStatus struct {
	Home       string             `json:"home"`
	LANIPStale bool               `json:"lan_ip_stale,omitempty"`
	Apps       []DesktopAppStatus `json:"apps"`
}

type DesktopAppStatus struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Level   string   `json:"level"`
	Address string   `json:"address,omitempty"`
	Actions []string `json:"actions,omitempty"`
}

var desktopAddress = regexp.MustCompile(`^https?://(\[[0-9a-fA-F:]+\]|[a-zA-Z0-9.-]+)(:[0-9]+)?(/[^[:space:][:cntrl:]\\?#]*)?$`)

func (s Status) ForDesktop() DesktopStatus {
	desktop := DesktopStatus{Home: s.Home, LANIPStale: s.LANIPStale, Apps: make([]DesktopAppStatus, 0, len(s.Apps))}
	for _, as := range s.Apps {
		item := DesktopAppStatus{ID: as.ID, Name: as.Name, State: as.State, Level: Level(as)}
		if !s.LANIPStale && (as.State == "running" || as.State == "unknown") && desktopAddress.MatchString(as.URL) {
			item.Address = as.URL
		}
		if as.Managed {
			item.Actions = desktopActions(as.State)
		}
		desktop.Apps = append(desktop.Apps, item)
	}
	return desktop
}

func desktopActions(state string) []string {
	switch state {
	case "unknown":
		// The desktop can offer these as conditional Actions after authorization.
		return []string{"start", "stop", "restart"}
	case "running", "degraded":
		return []string{"stop", "restart"}
	default:
		if desktopStopped(state) {
			return []string{"start"}
		}
		return nil
	}
}

func desktopStopped(state string) bool {
	switch state {
	case "exited", "stopped", "created", "missing":
		return true
	default:
		return false
	}
}

// DesktopAction is a review for one named App. An unknown observation permits
// the action only when a fresh Docker observation authorizes that transition.
type DesktopAction struct {
	Verb          string
	AppID         string
	ObservedState string
}

func (s *Service) ApplyDesktopAction(ctx context.Context, review DesktopAction) error {
	if !slices.Contains(desktopActions(review.ObservedState), review.Verb) {
		return fmt.Errorf("invalid desktop Action; review it again")
	}
	c := lifecycleChange(review.Verb, []string{review.AppID})
	c.onlyTargets = true
	c.after = afterNone
	c.namesStart = nil
	c.guard = review.check
	c.complete = func(ss *session) error { return review.verify(ctx, ss) }
	return s.run(ctx, c)
}

func (review DesktopAction) check(ctx context.Context, ss *session) error {
	id := review.AppID
	if _, err := ss.targets([]string{id}); err != nil {
		return err
	}
	if ss.p.IsExtra(id) {
		return fmt.Errorf("the App is no longer managed in this Project; review the Action again")
	}
	if err := ss.p.ValidateServiceOwnership(nil); err != nil {
		return err
	}
	if id == cloudflareConnector && review.Verb != "stop" {
		suspended, err := ss.cloudflareSuspended()
		if err != nil {
			return err
		}
		if suspended {
			return fmt.Errorf("the Cloudflare Tunnel is suspended; review Public access in the Dashboard")
		}
	}
	status, _, err := review.observe(ctx, ss)
	if err != nil {
		return err
	}
	if status.State == "unknown" || !slices.Contains(desktopActions(status.State), review.Verb) ||
		(review.ObservedState != "unknown" && review.ObservedState != status.State) {
		return fmt.Errorf("cannot %s %s with current container state %q; review the Action again", review.Verb, status.Name, status.State)
	}
	return nil
}

func (review DesktopAction) verify(ctx context.Context, ss *session) error {
	status, containers, err := review.observe(ctx, ss)
	if err != nil {
		return fmt.Errorf("could not verify %s after %s: %w", review.AppID, review.Verb, err)
	}
	if review.Verb == "stop" {
		for _, c := range containers {
			if slices.Contains(status.Services, c.Service) && !desktopStopped(c.State) {
				return fmt.Errorf("could not verify %s stopped: service %s is %q", status.Name, c.Service, c.State)
			}
		}
		return nil
	}
	if status.State != "running" {
		return fmt.Errorf("could not verify %s running after %s: current container state %q", status.Name, review.Verb, status.State)
	}
	return nil
}

func (review DesktopAction) observe(ctx context.Context, ss *session) (AppStatus, []compose.Container, error) {
	id := review.AppID
	a, found := ss.s.Catalog.App(id)
	if !found || ss.state.Apps[id] == nil {
		return AppStatus{}, nil, fmt.Errorf("the App is no longer managed in this Project; review the Action again")
	}
	services, err := ss.p.FragmentServices(id)
	if err != nil {
		return AppStatus{}, nil, err
	}
	if !slices.Contains(services, a.Info().Service) {
		return AppStatus{}, nil, fmt.Errorf("the main service for %s is no longer managed by this App", a.Info().Name)
	}
	containers, err := ss.compose.Ps(ctx)
	if err != nil {
		return AppStatus{}, nil, fmt.Errorf("live App Status could not be checked: %w", err)
	}
	status := AppStatus{ID: id, Name: a.Info().Name, Managed: true, State: "missing", Services: services}
	ss.applyContainerStatus(&status, containersByService(containers), a.Info().Service)
	return status, containers, nil
}
