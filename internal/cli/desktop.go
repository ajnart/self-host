package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/service"
)

func (a *program) desktopCmd() *cobra.Command {
	desktop := &cobra.Command{Use: "desktop", Hidden: true, Short: "Interface for the packaged desktop adapter"}
	status := &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a.document = true
			status, err := a.service().Status(cmd.Context())
			if errors.Is(err, service.ErrNoProject) {
				return writeJSON(a.deps.Out, nil)
			}
			if err != nil {
				return a.fail(err)
			}
			return writeJSON(a.deps.Out, status.ForDesktop())
		},
	}
	docker := &cobra.Command{
		Use:  "docker",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeJSON(a.deps.Out, host.DesktopDocker(cmd.Context(), a.deps))
		},
	}
	var yes bool
	action := &cobra.Command{
		Use:  "action <verb> <app> <observed-state>",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return a.fail(fmt.Errorf("review the Action first and pass --yes to apply it"))
			}
			return a.fail(a.service().ApplyDesktopAction(cmd.Context(), service.DesktopAction{
				Verb: args[0], AppID: args[1], ObservedState: args[2],
			}))
		},
	}
	action.Flags().BoolVar(&yes, "yes", false, "apply the reviewed Action")
	desktop.AddCommand(status, docker, action)
	return desktop
}
