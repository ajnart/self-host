package host

import (
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/ajnart/self-host/internal/system"
)

// PortRequirement is one non-secret host port in an approved Onboarding plan.
type PortRequirement struct {
	App, Title string
	Input      string
	Port       int
	Protocols  []string
	Automatic  bool
	BindIP     string
}

// Onboarding checks the machine against an unapproved plan without changing it.
func Onboarding(deps *system.Deps, projectPath string, appCount int, ports []PortRequirement, existing ...PortRequirement) []Check {
	checks := []Check{onboardingDisk(deps, projectPath, appCount)}
	reserved := slices.Clone(existing)
	for _, requirement := range ports {
		if len(requirement.Protocols) == 0 {
			requirement.Protocols = []string{"tcp"}
		}
		check := onboardingPort(deps, requirement, reserved)
		checks = append(checks, check)
		if check.Port > 0 {
			requirement.Port = check.Port
			reserved = append(reserved, requirement)
		}
	}
	return checks
}

func onboardingDisk(deps *system.Deps, path string, apps int) Check {
	check := Check{ID: "disk", Title: "Disk space", Level: OK}
	available, required, err := diskSpace(deps, path, apps)
	if err != nil {
		check.Level, check.Detail, check.Hint = Fail, "free space could not be read: "+err.Error(), "Fix access to the Home folder, then press r to recheck."
		return check
	}
	check.Detail = fmt.Sprintf("%d GiB free in the Home folder; %d GiB recommended", available>>30, required>>30)
	if available < required {
		check.Level = Warn
		check.Hint = "Free some space before installing large Apps or storing media."
	}
	return check
}

func onboardingPort(deps *system.Deps, requirement PortRequirement, reserved []PortRequirement) Check {
	protocols := slices.Clone(requirement.Protocols)
	if len(protocols) == 0 {
		protocols = []string{"tcp"}
	}
	requirement.Protocols = protocols
	label := protocolLabel(protocols)
	check := Check{ID: "port", App: requirement.App, Input: requirement.Input, Title: requirement.Title, Level: OK,
		Detail: fmt.Sprintf("port %d %s is free", requirement.Port, label)}
	if portUsable(deps, requirement, reserved) {
		check.Port = requirement.Port
		return check
	}
	if !requirement.Automatic {
		check.Level = Fail
		check.Detail = fmt.Sprintf("selected port %d %s is already in use", requirement.Port, label)
		check.Hint = "Choose another port in What, then press r to recheck."
		return check
	}
	for port := requirement.Port + 1; port <= 65535; port++ {
		candidate := requirement
		candidate.Port = port
		if portUsable(deps, candidate, reserved) {
			check.Port = port
			check.Level = Warn
			check.Detail = fmt.Sprintf("port %d %s is in use; install will use %d instead", requirement.Port, label, port)
			return check
		}
	}
	check.Level = Fail
	check.Detail = fmt.Sprintf("no free %s port from %d", label, requirement.Port)
	check.Hint = "Free a port, then press r to recheck."
	return check
}

func portUsable(deps *system.Deps, requirement PortRequirement, reserved []PortRequirement) bool {
	if PortReserved(requirement, reserved) {
		return false
	}
	for _, protocol := range requirement.Protocols {
		var probes []PortProbe
		switch protocol {
		case "tcp":
			probes = ProbePort(deps, requirement.Port)
		case "udp":
			probes = ProbeUDPPort(deps, requirement.Port)
		default:
			return false
		}
		if !PortAvailable(probes) {
			return false
		}
	}
	return true
}

func PortReserved(candidate PortRequirement, reserved []PortRequirement) bool {
	for _, existing := range reserved {
		if candidate.Port == 0 || candidate.Port != existing.Port || !portBindingsOverlap(candidate.BindIP, existing.BindIP) {
			continue
		}
		for _, protocol := range candidate.Protocols {
			if slices.Contains(existing.Protocols, protocol) {
				return true
			}
		}
	}
	return false
}

func portBindingsOverlap(left, right string) bool {
	a, b := net.ParseIP(left), net.ParseIP(right)
	if left == "" || right == "" || a == nil || b == nil || a.IsUnspecified() || b.IsUnspecified() {
		return true
	}
	return a.Equal(b)
}

func protocolLabel(protocols []string) string {
	labels := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		labels = append(labels, strings.ToUpper(protocol))
	}
	return strings.Join(labels, " and ")
}
