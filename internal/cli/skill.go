package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ajnart/self-host/internal/skill"
)

func (a *program) skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill [topic]",
		Short: "Print the agent guide that matches this binary",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			a.document = true
			guide := skill.Guide(a.deps.Version)
			if len(args) > 0 {
				var err error
				guide, err = skill.Topic(a.deps.Version, args[0])
				if err != nil {
					return a.fail(err)
				}
			}
			_, err := fmt.Fprint(a.deps.Out, guide)
			return err
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Make the self-host skill discoverable by the AI agents installed on this computer",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return a.skillFiles(skill.Install) },
		},
		&cobra.Command{
			Use:   "remove",
			Short: "Remove the skill directories that skill install wrote",
			Args:  cobra.NoArgs,
			RunE:  func(*cobra.Command, []string) error { return a.skillFiles(skill.Remove) },
		},
	)
	return cmd
}

func (a *program) skillFiles(run func(home string) ([]skill.Result, error)) error {
	a.document = true
	owner, err := a.deps.Owner()
	if err != nil {
		return a.fail(err)
	}
	home := owner.Home
	if override := a.deps.Getenv("HOME"); override != "" && a.deps.Getenv("SUDO_USER") == "" {
		home = override
	}
	results, err := run(home)
	if a.json {
		if werr := writeJSON(a.deps.Out, map[string]any{"paths": results}); werr != nil {
			return a.fail(werr)
		}
	} else {
		printSkillResults(a.deps.Out, results)
	}
	return a.fail(err)
}

func printSkillResults(w io.Writer, results []skill.Result) {
	for _, r := range results {
		fmt.Fprintf(w, "%-30s %s\n", r.Status, r.Path)
	}
}
