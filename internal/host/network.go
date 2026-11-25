package host

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/system"
)

// Firewall reports a host firewall that may block other devices.
func Firewall(ctx context.Context, deps *system.Deps, ports string) Check {
	check := Check{ID: "firewall", Title: "Firewall", Level: OK, Detail: "no active firewall identified by these checks; reachability from other devices is unverified"}
	run := func(name string, args ...string) string {
		result, err := deps.Runner.Run(ctx, system.Cmd{Name: name, Args: args})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(result.Stdout))
	}
	if deps.GOOS == "darwin" {
		if strings.Contains(run("/usr/libexec/ApplicationFirewall/socketfilterfw", "--getglobalstate"), "enabled") {
			check.Level, check.Detail = Warn, "the macOS application firewall is on"
			check.Hint = "If other devices cannot connect, allow incoming connections for OrbStack (or Docker Desktop) in System Settings › Network › Firewall › Options."
		}
		return check
	}
	if run("systemctl", "is-active", "ufw") == "active" {
		check.Level, check.Detail = Warn, "ufw is active"
		check.Hint = "Docker port access depends on forwarding rules and helpers such as ufw-docker; a plain ufw allow rule may not cover it. Review the rules for App ports " + ports + " and your actual LAN, then check from another device. This local check does not verify reachability."
		return check
	}
	if run("systemctl", "is-active", "firewalld") == "active" {
		check.Level, check.Detail = Warn, "firewalld is active"
		check.Hint = "If a device cannot connect, open the ports: sudo firewall-cmd --permanent --add-port={" + ports + "}/tcp && sudo firewall-cmd --reload"
	}
	return check
}

// Reachable asks url for any HTTP response.
func Reachable(ctx context.Context, client *http.Client, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("no answer from %s: %w", url, err)
	}
	resp.Body.Close()
	return nil
}
