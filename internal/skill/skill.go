// Package skill holds the agent guide embedded in the binary, so an agent
// always reads the guide that matches the commands it can run.
package skill

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
)

//go:embed guide.md
var guide string

// Guide returns the agent guide for the given binary version.
func Guide(version string) string {
	return strings.ReplaceAll(guide, "{{version}}", strings.TrimPrefix(version, "v"))
}

func Topic(version, topic string) (string, error) {
	topics := map[string][]string{
		"core":      {},
		"planning":  {"Setting up", "Doctor checks", "Shared media storage", "Media configuration", "Beszel", "Responsible use"},
		"repair":    {"JSON events", "Exit codes", "When a step fails", "Doctor checks"},
		"access":    {"Reachability and Access"},
		"backup":    {"Backups"},
		"unmanaged": {"Unmanaged apps", "Apps the Catalog lacks", "Responsible use"},
		"reference": {"Commands", "JSON events", "Exit codes", "Telemetry", "Apps the Catalog lacks", "Responsible use", "Feedback", "Updating the binary"},
	}
	selected, ok := topics[topic]
	if !ok {
		return "", fmt.Errorf("unknown skill topic %q; use core, planning, repair, access, backup, unmanaged or reference; omit the topic for the full guide", topic)
	}
	selected = append(slices.Clone(selected), "Reading this guide", "Safety boundary", "Every conversation", "Project folder", "Secrets")
	sections := strings.Split(Guide(version), "\n## ")
	var out strings.Builder
	out.WriteString(sections[0])
	for _, section := range sections[1:] {
		heading, _, _ := strings.Cut(section, "\n")
		if slices.Contains(selected, heading) {
			out.WriteString("\n## ")
			out.WriteString(section)
			selected = slices.DeleteFunc(selected, func(name string) bool { return name == heading })
		}
	}
	if len(selected) > 0 {
		return "", fmt.Errorf("skill topic %q references missing guide sections: %s", topic, strings.Join(selected, ", "))
	}
	return out.String(), nil
}
