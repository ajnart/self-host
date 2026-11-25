package service

import (
	"context"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/host"
)

// removeDesktopShortcut runs after the App leaves the Project, even when a
// later App or Link in the same Action still needs attention.
func (ss *session) removeDesktopShortcut(ctx context.Context, id string) {
	if !host.HasOmarchyAdapter(ss.s.Deps) {
		return
	}
	name := id
	if a, found := ss.s.Catalog.App(id); found && ss.state.Apps[id] != nil {
		name = a.Info().Name
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()
	result, err := host.RunOmarchy(ctx, ss.s.Deps, "shortcut-cleanup", id, ss.p.Root, ss.p.ComposeName, name)
	if err != nil || result.ExitCode != 0 {
		ss.s.Log.Emit(events.Event{Event: events.StepWarn, App: id, Step: "desktop-shortcut",
			Msg: "Desktop shortcut could not be removed", Hint: "Review the App shortcut in your desktop applications folder."})
	}
}
