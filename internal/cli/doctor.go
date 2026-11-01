package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/service"
)

func (a *program) doctorCmd() *cobra.Command {
	var opts service.InstallOptions
	cmd := &cobra.Command{
		Use:   "doctor [app|recipe...]",
		Short: "Check Docker, Compose, App bindings, firewalls and LAN reachability",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && (len(opts.Sets) > 0 || opts.EasyNames) {
				return fmt.Errorf("name the planned Apps or Recipes when checking selected ports")
			}
			log := a.logger()
			log.Emit(events.Event{Event: events.RunStart, Msg: "doctor"})
			checks := a.service().Doctor(cmd.Context())
			if len(args) > 0 {
				opts.Targets = args
				checks = append(checks, a.service().DoctorPlan(cmd.Context(), opts)...)
			}
			emitChecks(log, checks)
			worst := host.Worst(checks)
			log.Emit(events.Event{Event: events.RunEnd, Msg: "doctor: " + string(worst)})
			if worst == host.Fail {
				return exitError{ExitError}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&opts.Sets, "set", nil, "planned non-secret input as app.key=value (repeatable); app.port=N picks a port")
	cmd.Flags().BoolVar(&opts.EasyNames, "easy-names", false, "check port 80 for planned Easy names at home")
	return cmd
}

func emitChecks(log *events.Logger, checks []host.Check) {
	for _, c := range checks {
		event := events.Event{App: c.App, Step: c.ID, Msg: c.Title + ": " + c.Detail, Hint: c.Hint, Docs: c.Docs}
		switch c.Level {
		case host.OK:
			event.Event = events.StepOK
		case host.Skipped:
			event.Event = events.StepSkip
		case host.Warn:
			event.Event = events.StepWarn
		default:
			event.Event = events.StepFail
		}
		log.Emit(event)
	}
}
