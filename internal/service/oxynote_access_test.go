package service

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/oxynote"
	tsapp "github.com/ajnart/self-host/internal/apps/tailscale"
	"github.com/ajnart/self-host/internal/helpers"
)

type oxynoteAccessApp struct{ oxynote.App }

func (oxynoteAccessApp) Setup(context.Context, *helpers.H) error { return nil }
func (oxynoteAccessApp) Check(context.Context, *helpers.H) error { return nil }

func TestOxynoteKeepsOnlyItsAuthenticatedHomeAddress(t *testing.T) {
	for _, installTogether := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote added later", true: "install with remote"}[installTogether], func(t *testing.T) {
			dashboard := &inventoryProbeApp{testApp: &testApp{id: "homarr", done: map[string]bool{}}, inspect: true}
			h := newHarness(t, oxynoteAccessApp{}, tsapp.App{}, dashboard)
			net := &tailnet{running: []string{"oxynote", "homarr", "access-caddy"}, trackStartup: true}
			net.install(h)
			if installTogether {
				if err := h.installTailscale("oxynote", "homarr"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := h.install("oxynote", "homarr"); err != nil {
					t.Fatal(err)
				}
				if err := h.installTailscale(); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(h.readFile("apps", "tailscale", "compose.yml"), "tailscale-oxynote") {
				t.Fatal("Oxynote received unsupported private Access")
			}
			net.running = append(net.running, "tailscale-oxynote")
			for _, settings := range [][]string{{"EASY_NAMES=on"}, {"EASY_NAMES=off"}, {"EXPOSURE=local"}, {"EXPOSURE=lan", "LAN_IP=192.168.1.21"}} {
				if err := h.svc.ConfigSet(t.Context(), settings); err != nil {
					t.Fatal(err)
				}
				ss, err := h.svc.open(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				want := AppURL(ss.settings, 8085, "")
				a := appStatus(t, h, "oxynote")
				if a.URL != want || !slices.Equal(a.Addresses, []string{want}) {
					t.Fatalf("Oxynote advertises unauthenticated Addresses: %+v, want %s", a, want)
				}
				status, err := h.svc.Status(t.Context())
				if err != nil || !slices.ContainsFunc(status.AccessWarnings, func(w string) bool { return strings.Contains(w, "Oxynote") }) {
					t.Fatalf("Oxynote's Access limitation was hidden: %+v %v", status.AccessWarnings, err)
				}
				if err := h.svc.Setup(t.Context(), []string{"homarr"}); err != nil {
					t.Fatal(err)
				}
				for _, item := range dashboard.installed {
					if item.ID == "oxynote" && item.URL != want {
						t.Fatalf("Homarr advertises %s, want %s", item.URL, want)
					}
				}
				if ss.namesOn() {
					data, err := ss.p.ReadNamesCaddyfile()
					if err != nil || strings.Contains(string(data), "oxynote.") {
						t.Fatalf("Oxynote received an unsupported Easy name: %s %v", data, err)
					}
				}
			}
			plan, err := h.svc.CloudflarePlan(t.Context(), CloudflareOptions{Domain: "example.com", Emails: []string{"owner@example.com"}, Apps: []string{"oxynote"}})
			if err != nil || len(plan.Publish) != 0 || len(plan.Refused) != 1 {
				t.Fatalf("Cloudflare offered unsupported Access: %+v %v", plan, err)
			}
			export, err := h.svc.AccessExport(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(export.Apps, func(a ExportApp) bool { return a.ID == "oxynote" }) || !slices.ContainsFunc(export.Warnings, func(w ExportWarning) bool { return w.ID == "oxynote" }) {
				t.Fatalf("Bring your own Access lost Oxynote's restriction: %+v", export)
			}
		})
	}
}
