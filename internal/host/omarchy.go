package host

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/system"
)

const omarchyAdapter = "/usr/bin/self-host-omarchy"

func HasOmarchyAdapter(d *system.Deps) bool {
	return omarchyExecutable(d, omarchyAdapter)
}

func HasOmarchyBar(d *system.Deps) bool {
	return HasOmarchyAdapter(d) && omarchyExecutable(d, "/usr/bin/omarchy-shell")
}

func omarchyExecutable(d *system.Deps, path string) bool {
	if d.GOOS != "linux" || d.LookPath == nil {
		return false
	}
	found, err := d.LookPath(path)
	return err == nil && found == path
}

// OmarchyLogo reads the system's terminal artwork as plain block characters.
func OmarchyLogo(d *system.Deps) string {
	if !omarchyExecutable(d, "/usr/bin/omarchy") {
		return ""
	}
	data, err := os.ReadFile("/usr/share/omarchy/logo.txt")
	if err != nil || len(data) > 4096 {
		return ""
	}
	return strings.Trim(strings.Map(func(r rune) rune {
		if r == '\n' || r == ' ' || (r >= '▀' && r <= '▟') {
			return r
		}
		return -1
	}, string(data)), "\n")
}

// RunOmarchy runs the packaged adapter as the desktop user, even when Docker
// required the calling self-host process to run as root.
func RunOmarchy(ctx context.Context, d *system.Deps, args ...string) (system.Result, error) {
	cmd, err := d.DesktopCommand(system.Cmd{Name: omarchyAdapter, Args: args, WaitDelay: time.Second})
	if err != nil {
		return system.Result{}, err
	}
	return d.Runner.Run(ctx, cmd)
}
