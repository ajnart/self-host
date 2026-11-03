package cli

import (
	"fmt"

	"github.com/ajnart/self-host/internal/service"
	"github.com/spf13/cobra"
)

func (a *program) connectionsCmd() *cobra.Command {
	root := &cobra.Command{Use: "connections", Short: "Configure explicit external Homarr connections without managing their targets"}
	var add, update service.ConnectionOptions
	addCmd := &cobra.Command{
		Use: "add <kind>", Short: "Save an approved private connection and run Homarr Setup", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := a.service().AddConnection(cmd.Context(), args[0], add)
			return a.fail(err)
		},
	}
	updateCmd := &cobra.Command{
		Use: "update <id>", Short: "Replace private credentials or selection, keeping the endpoint and kind", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.fail(a.service().UpdateConnection(cmd.Context(), args[0], update))
		},
	}
	for cmd, opts := range map[*cobra.Command]*service.ConnectionOptions{addCmd: &add, updateCmd: &update} {
		cmd.Flags().StringVar(&opts.File, "file", "", "owner-only private JSON input, or - for bounded stdin; never pass endpoint or credential values as arguments")
		cmd.Flags().BoolVar(&opts.Approved, "yes", false, "approve the exact privately reviewed endpoint, credentials/read scopes and existing-data selection")
	}
	statusCmd := &cobra.Command{
		Use: "status [id]", Short: "Show stored connection Setup evidence without endpoints, credentials or provider requests", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a.document = true
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			statuses, err := a.service().Connections(cmd.Context(), id)
			if err != nil {
				return a.fail(err)
			}
			if a.json {
				return writeJSON(a.deps.Out, statuses)
			}
			for _, item := range statuses {
				fmt.Fprintf(a.deps.Out, "%s  %s  %s\n", item.ID, item.Kind, item.Setup)
				if item.Hint != "" {
					fmt.Fprintln(a.deps.Out, "  "+item.Hint)
				}
			}
			return nil
		},
	}
	var removeApproved bool
	removeCmd := &cobra.Command{
		Use: "remove <id>", Short: "Disconnect only recorded owned Homarr bindings; never change the external target", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.fail(a.service().RemoveConnection(cmd.Context(), args[0], removeApproved))
		},
	}
	removeCmd.Flags().BoolVar(&removeApproved, "yes", false, "approve disconnecting only this connection's recorded Homarr resources")
	root.AddCommand(addCmd, updateCmd, statusCmd, removeCmd)
	return root
}
