package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
)

type onboardingOmarchyBackend struct {
	*fakeBackend
	req Request
}

func (*onboardingOmarchyBackend) HasOmarchyBar() bool { return true }
func (*onboardingOmarchyBackend) OmarchyLogo() string { return "██▀" }
func (*onboardingOmarchyBackend) OmarchyBar(context.Context, string) (bool, error) {
	return false, nil
}
func (b *onboardingOmarchyBackend) Run(ctx context.Context, req Request, log *events.Logger) error {
	b.req = req
	return b.fakeBackend.Run(ctx, req, log)
}

func TestOnboardingOmarchyShortcutIsOptIn(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		m, fake := freshHost(t)
		desktop := &onboardingOmarchyBackend{fakeBackend: fake}
		m.backend = desktop
		m.onboarding = newOnboarding(m.ctx())
		m.onboarding.intro = nil
		m = atStep(t, m, "Look")
		if out := screen(m); !strings.Contains(out, "Show self-host in the Omarchy top bar") {
			t.Fatalf("omitted the Omarchy opt-in from Look: %s", out)
		}
		if optIn {
			for m.onboarding.look < len(glyphChoices) {
				m = drive(t, m, press("down"))
			}
			m = drive(t, m, press("space"))
		}
		m = drive(t, m, press("enter"))
		if got := strings.Contains(screen(m), "Show the top bar shortcut after Setup completes"); got != optIn {
			t.Fatalf("Review shows shortcut = %v, want %v", got, optIn)
		}
		m = drive(t, m, press("enter"))
		if desktop.req.Action != ActionInstall || desktop.req.ShowOmarchyBar != optIn {
			t.Fatalf("approved request = %+v, want install with opt-in %v", desktop.req, optIn)
		}
	}
}
