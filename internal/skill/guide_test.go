package skill_test

import (
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/skill"
)

func TestPlanningTopicIncludesMediaConfiguration(t *testing.T) {
	planning, err := skill.Topic("v1.2.3", "planning")
	if err != nil {
		t.Fatal(err)
	}
	_, media, found := strings.Cut(skill.Guide("v1.2.3"), "\n## Media configuration\n")
	if !found {
		t.Fatal("full guide lost Media configuration")
	}
	media, _, _ = strings.Cut(media, "\n## ")
	if !strings.Contains(planning, "\n## Media configuration\n"+media) {
		t.Fatal("planning omitted the existing media preservation and profile selection guidance")
	}
	for _, flag := range []string{"self-host doctor <app|recipe>... --set app.port=N --json", "`--easy-names` also checks port 80"} {
		if !strings.Contains(planning, flag) {
			t.Errorf("planning lost Doctor guidance %q", flag)
		}
	}
}

func TestEveryTopicPreservesCoreSafety(t *testing.T) {
	for _, topic := range []string{"core", "planning", "repair", "access", "backup", "unmanaged", "reference"} {
		t.Run(topic, func(t *testing.T) {
			focused, err := skill.Topic("v1.2.3", topic)
			if err != nil {
				t.Fatal(err)
			}
			for _, heading := range []string{"Reading this guide", "Safety boundary", "Every conversation", "Project folder", "Secrets"} {
				_, section, found := strings.Cut(skill.Guide("v1.2.3"), "\n## "+heading+"\n")
				if !found {
					t.Fatalf("full guide lost %s", heading)
				}
				section, _, _ = strings.Cut(section, "\n## ")
				if !strings.Contains(focused, "\n## "+heading+"\n"+section) {
					t.Errorf("focused guide omitted core section %s", heading)
				}
			}
		})
	}
}
