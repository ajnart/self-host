package service

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/system"
)

func TestPlannedAndInstalledPortsReserveStoppedComposePublications(t *testing.T) {
	for _, tc := range []struct {
		name, ports  string
		extra, local bool
		want         int
	}{
		{name: "manual short TCP", ports: "      - '127.0.0.1:9001:80'\n", want: 9002},
		{name: "Unmanaged app", ports: "      - '${BIND_IP}:9001:80'\n", extra: true, want: 9002},
		{name: "UDP does not reserve TCP", ports: "      - '9001:80/udp'\n", want: 9001},
		{name: "long range", ports: "      - target: 80\n        published: '9001-9002'\n        host_ip: 0.0.0.0\n        protocol: tcp\n", want: 9003},
		{name: "IPv6 binding", ports: "      - '[::1]:9001:80'\n", want: 9002},
		{name: "distinct local binding", ports: "      - '127.0.0.2:9001:80'\n", local: true, want: 9001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, &testApp{id: "one", done: map[string]bool{}}, &testApp{id: "new", done: map[string]bool{}})
			if err := h.install("one"); err != nil {
				t.Fatal(err)
			}
			p, err := h.svc.Project()
			if err != nil {
				t.Fatal(err)
			}
			if tc.local {
				settings, err := p.LoadSettings()
				if err != nil {
					t.Fatal(err)
				}
				settings["EXPOSURE"], settings["BIND_IP"] = ExposureLocal, BindIP(ExposureLocal)
				if err := p.SaveSettings(settings); err != nil {
					t.Fatal(err)
				}
			}
			definition := "  manual:\n    image: example/manual\n    ports:\n" + tc.ports
			if tc.extra {
				h.writeExtra("manual", "services:\n"+definition)
			} else {
				root, err := p.ReadCompose()
				if err != nil {
					t.Fatal(err)
				}
				if err := p.WriteCompose([]byte(strings.Replace(string(root), "services:\n", "services:\n"+definition, 1))); err != nil {
					t.Fatal(err)
				}
			}
			h.deps.DiskUsage = func(string) (system.DiskUsage, error) {
				return system.DiskUsage{AvailableBytes: 20 << 30}, nil
			}
			before, err := p.ReadCompose()
			if err != nil {
				t.Fatal(err)
			}
			calls := len(h.runner.Calls())
			checks := h.svc.DoctorPlan(context.Background(), InstallOptions{Targets: []string{"new"}})
			planned := 0
			for _, check := range checks {
				if check.ID == "port" && check.App == "new" {
					planned = check.Port
				}
			}
			after, err := p.ReadCompose()
			if err != nil || string(before) != string(after) || len(h.runner.Calls()) != calls {
				t.Fatalf("planning rewrote Compose or ran external commands: %v", err)
			}
			if planned != tc.want || host.Worst(checks) == host.Fail {
				t.Fatalf("planned port = %d, want %d: %+v", planned, tc.want, checks)
			}
			if err := h.install("new"); err != nil {
				t.Fatal(err)
			}
			state, err := p.LoadState()
			if err != nil || state.Apps["new"] == nil || state.Apps["new"].Port != planned {
				t.Fatalf("allocator differs from plan: %+v, %v", state, err)
			}
		})
	}
}

func TestPlannedAndInstalledSecondaryPortsReserveUnmanagedPublications(t *testing.T) {
	h := newHarness(t, &testApp{id: "one", done: map[string]bool{}}, &portInputApp{testApp{id: "paired", done: map[string]bool{}}})
	if err := h.install("one"); err != nil {
		t.Fatal(err)
	}
	h.writeExtra("manual", "services:\n  manual:\n    image: example/manual\n    ports:\n      - '6881:6881/udp'\n")
	h.deps.DiskUsage = func(string) (system.DiskUsage, error) {
		return system.DiskUsage{AvailableBytes: 20 << 30}, nil
	}
	checks := h.svc.DoctorPlan(context.Background(), InstallOptions{Targets: []string{"paired"}})
	planned := 0
	for _, check := range checks {
		if check.App == "paired" && check.Input == "peer-port" {
			planned = check.Port
		}
	}
	if planned != 6882 {
		t.Fatalf("secondary reservation was ignored: %+v", checks)
	}
	if err := h.install("paired"); err != nil {
		t.Fatal(err)
	}
	p, err := h.svc.Project()
	if err != nil {
		t.Fatal(err)
	}
	state, err := p.LoadState()
	if err != nil || state.Apps["paired"] == nil || state.Apps["paired"].Inputs["peer-port"] != strconv.Itoa(planned) {
		t.Fatalf("secondary allocator differs from plan: %+v, %v", state, err)
	}
}

func TestDoctorPortPlanDoesNotInspectDockerBeforeProjectCreation(t *testing.T) {
	h := newHarness(t, &testApp{id: "new", done: map[string]bool{}})
	h.deps.DiskUsage = func(string) (system.DiskUsage, error) {
		return system.DiskUsage{AvailableBytes: 20 << 30}, nil
	}
	checks := h.svc.DoctorPlan(context.Background(), InstallOptions{Targets: []string{"new"}})
	if host.Worst(checks) == host.Fail || len(h.runner.Calls()) != 0 {
		t.Fatalf("port planning reached Docker: %+v, %v", checks, h.runner.Calls())
	}
}
