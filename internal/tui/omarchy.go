package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/ajnart/self-host/internal/host"
)

var omarchyKeys = []key.Binding{
	hint("enter", "toggle"), hint("r", "refresh"), hint("[/]", "sub-tab"), hint("esc", "back"),
}

// omarchyBarBackend is optional. Other installations and fake backends do not
// expose a desktop preference they cannot apply.
type omarchyBarBackend interface {
	HasOmarchyBar() bool
	OmarchyBar(context.Context, string) (bool, error)
}

func (b ServiceBackend) HasOmarchyBar() bool {
	return host.HasOmarchyBar(b.Deps)
}

func (b ServiceBackend) OmarchyLogo() string { return host.OmarchyLogo(b.Deps) }

func (b ServiceBackend) OmarchyBar(ctx context.Context, verb string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	result, err := host.RunOmarchy(ctx, b.Deps, "bar", verb)
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, string(result.Stderr)))
		if detail == "" {
			detail = "Could not reach the Omarchy bar."
		}
		return false, fmt.Errorf("%s", detail)
	}
	switch strings.TrimSpace(string(result.Stdout)) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, fmt.Errorf("unrecognized Omarchy bar status")
	}
}

type omarchyBarPane struct {
	backend omarchyBarBackend
	on      bool
	known   bool
	busy    bool
	err     error
}

type omarchyBarMsg struct {
	on  bool
	err error
}

func (p *omarchyBarPane) run(verb string) tea.Cmd {
	if p.busy {
		return nil
	}
	p.busy = true
	p.err = nil
	return func() tea.Msg {
		on, err := p.backend.OmarchyBar(context.Background(), verb)
		return omarchyBarMsg{on: on, err: err}
	}
}

func (p *omarchyBarPane) toggle() tea.Cmd {
	if !p.known {
		return p.run("status")
	}
	if p.on {
		return p.run("disable")
	}
	return p.run("enable")
}

func (p *omarchyBarPane) apply(msg omarchyBarMsg) {
	p.on = msg.on
	p.known = msg.err == nil
	p.busy = false
	p.err = msg.err
}

func (p *omarchyBarPane) box(ctx *viewContext, width int) string {
	t := ctx.theme
	value := t.dim().Render("Checking…")
	help := ""
	switch {
	case p.busy:
		value = t.dim().Render("Updating…")
	case p.err != nil:
		value = t.dim().Render("Unavailable")
		help = p.err.Error() + " Press r to refresh."
	case p.known && p.on:
		value = t.title().Render("On")
		help = "Press Enter to hide the shortcut."
	case p.known:
		value = t.dim().Render("Off")
		help = "Press Enter to show the shortcut."
	}
	lines := []string{
		"  " + t.body().Render("Top bar shortcut") + "    " + value,
		"",
		"  " + t.dim().Render("Click the Logo to see your Apps. Right-click for the Dashboard."),
		"  " + t.dim().Render("This setting applies to your Omarchy desktop."),
	}
	if help != "" {
		lines = append(lines, "", "  "+t.body().Render(help))
	}
	for i := range lines {
		lines[i] = truncate(lines[i], max(width-4, 12))
	}
	return t.box("Omarchy", "", strings.Join(lines, "\n"), width, len(lines)+2, true)
}
