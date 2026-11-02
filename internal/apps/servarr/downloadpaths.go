package servarr

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const downloadDirectoryProbe = `test -d "$1"
test -r "$1"
test -w "$1"
test -x "$1"
readlink -f -- "$1"
stat -L -c '%d:%i' -- "$1"
cat /proc/self/mountinfo`

type downloadPathIdentity uint8

const (
	downloadPathS6ABC downloadPathIdentity = iota
	downloadPathSuExecUserUsers
)

type downloadDirectory struct {
	canonical, identity, mountDevice, backing string
}

func downloadPathValid(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value && !strings.ContainsAny(value, "\r\n\x00\\")
}

func pathWithin(parent, child string) bool {
	return parent == child || parent == "/" && strings.HasPrefix(child, "/") || strings.HasPrefix(child, parent+"/")
}

func (p providerLink) pathManual(h *helpers.H, reason string) error {
	return &helpers.ManualError{Hint: "Review " + h.App + " > Settings > Media Management and Download Clients, and " + p.name + " download folders: " + reason + ". Preserve existing files, libraries, profiles, category folders and Compose mounts. Use an explicitly reviewed shared folder or existing Remote Path Mapping, then run self-host setup " + h.App + ". Setup does not migrate storage or prove a hardlink import."}
}

func (p providerLink) readDownloadPaths(ctx context.Context, h *helpers.H, endpoint string, value any) error {
	resp, err := p.request(ctx, h, "GET", endpoint, nil)
	if err != nil || resp.Status != 200 || resp.Decode(value) != nil {
		return fmt.Errorf("the %s download-path configuration could not be read; retry self-host setup %s", h.App, h.App)
	}
	return nil
}

func (p providerLink) mappedDownloadPath(ctx context.Context, from *helpers.H, host, completed string) (string, error) {
	var mappings []struct {
		ID                          int64 `json:"id"`
		Host, RemotePath, LocalPath string
	}
	if err := p.readDownloadPaths(ctx, from, "/remotepathmapping", &mappings); err != nil {
		return "", err
	}
	if mappings == nil {
		return "", fmt.Errorf("%s did not return an explicit Remote Path Mapping list", from.App)
	}
	local, matched := completed, false
	ids := map[int64]bool{}
	for _, mapping := range mappings {
		if mapping.ID <= 0 || ids[mapping.ID] || strings.TrimSpace(mapping.Host) == "" {
			return "", p.pathManual(from, "the Remote Path Mapping identities are invalid or ambiguous")
		}
		ids[mapping.ID] = true
		if !strings.EqualFold(mapping.Host, host) {
			continue
		}
		remote, destination := strings.TrimRight(mapping.RemotePath, "/"), strings.TrimRight(mapping.LocalPath, "/")
		if !downloadPathValid(remote) || !downloadPathValid(destination) {
			return "", p.pathManual(from, "a mapping for the selected downloader has an unsupported path")
		}
		if !pathWithin(remote, completed) {
			continue
		}
		if matched {
			return "", p.pathManual(from, "more than one mapping applies to the completed download folder")
		}
		matched = true
		local = destination + strings.TrimPrefix(completed, remote)
	}
	return local, nil
}

func downloadDirectoryIdentity(ctx context.Context, h *helpers.H, directory string, identity downloadPathIdentity) (downloadDirectory, error) {
	if h.Config.Exec == nil || !downloadPathValid(directory) {
		return downloadDirectory{}, fmt.Errorf("directory access is unavailable")
	}
	command := []string{"s6-setuidgid", "abc"}
	probe := downloadDirectoryProbe
	switch identity {
	case downloadPathS6ABC:
	case downloadPathSuExecUserUsers:
		command = []string{"su-exec", "user:users"}
		probe = `test "$(id -u)" -ne 0
test "$(id -g)" -ne 0
id -un
id -gn
` + downloadDirectoryProbe
	default:
		return downloadDirectory{}, fmt.Errorf("directory App-user mechanism is unsupported")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command = append(command, "sh", "-eu", "-c", probe, "self-host-download-path", directory)
	result, err := h.Exec(ctx, nil, command...)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		return downloadDirectory{}, fmt.Errorf("directory access could not be verified as the App user")
	}
	lines := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
	if identity == downloadPathSuExecUserUsers {
		if len(lines) < 5 || lines[0] != "user" || lines[1] != "users" {
			return downloadDirectory{}, fmt.Errorf("directory App-user identity could not be verified")
		}
		lines = lines[2:]
	}
	if len(lines) < 3 || !downloadPathValid(lines[0]) {
		return downloadDirectory{}, fmt.Errorf("directory identity is unavailable")
	}
	parts := strings.Split(lines[1], ":")
	if len(parts) != 2 {
		return downloadDirectory{}, fmt.Errorf("directory identity is invalid")
	}
	for _, part := range parts {
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil || number == 0 || strconv.FormatUint(number, 10) != part {
			return downloadDirectory{}, fmt.Errorf("directory identity is invalid")
		}
	}
	observation := downloadDirectory{canonical: lines[0], identity: lines[1]}
	selected := ""
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\")
	for _, line := range lines[2:] {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			return downloadDirectory{}, fmt.Errorf("directory mount information is invalid")
		}
		separator := -1
		for index := 6; index < len(fields)-3; index++ {
			if fields[index] == "-" {
				separator = index
				break
			}
		}
		device := strings.Split(fields[2], ":")
		if separator < 0 || len(device) != 2 {
			return downloadDirectory{}, fmt.Errorf("directory mount information is invalid")
		}
		for _, part := range device {
			if number, err := strconv.ParseUint(part, 10, 32); err != nil || strconv.FormatUint(number, 10) != part {
				return downloadDirectory{}, fmt.Errorf("directory mount device is invalid")
			}
		}
		root, mount := unescape.Replace(fields[3]), unescape.Replace(fields[4])
		if root != "/" && !downloadPathValid(root) || mount != "/" && !downloadPathValid(mount) {
			return downloadDirectory{}, fmt.Errorf("directory mount information is unsupported")
		}
		if mount != observation.canonical && pathWithin(observation.canonical, mount) {
			return downloadDirectory{}, fmt.Errorf("nested storage needs explicit review")
		}
		if !pathWithin(mount, observation.canonical) || len(mount) < len(selected) {
			continue
		}
		if mount == selected {
			return downloadDirectory{}, fmt.Errorf("stacked storage needs explicit review")
		}
		selected = mount
		observation.mountDevice = fields[2]
		observation.backing = path.Join(root, strings.TrimPrefix(strings.TrimPrefix(observation.canonical, mount), "/"))
	}
	if selected == "" {
		return downloadDirectory{}, fmt.Errorf("directory backing storage is unavailable")
	}
	return observation, nil
}

func (p providerLink) verifyDownloadPath(ctx context.Context, from, to *helpers.H, host, completed string) error {
	return p.verifyDownloadPathAs(ctx, from, to, host, completed, downloadPathS6ABC)
}

func (p providerLink) verifyDownloadPathAs(ctx context.Context, from, to *helpers.H, host, completed string, targetIdentity downloadPathIdentity) error {
	if !downloadPathValid(completed) {
		return p.pathManual(from, "the downloader did not identify a supported absolute completed folder")
	}
	local, err := p.mappedDownloadPath(ctx, from, host, completed)
	if err != nil {
		return err
	}
	var roots []struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	if err := p.readDownloadPaths(ctx, from, "/rootfolder", &roots); err != nil {
		return err
	}
	if len(roots) == 0 {
		return p.pathManual(from, "choose the intended library root and any required profiles in the consuming App first")
	}
	remote, err := downloadDirectoryIdentity(ctx, to, completed, targetIdentity)
	if err != nil {
		return p.pathManual(from, "the downloader cannot verify access to its completed folder as its App user")
	}
	observed, err := downloadDirectoryIdentity(ctx, from, local, downloadPathS6ABC)
	if err != nil || remote.identity != observed.identity || remote.mountDevice != observed.mountDevice || remote.backing != observed.backing {
		return p.pathManual(from, "the completed folder is not verified as the same accessible directory in both Apps; legacy App-local folders are not shared storage")
	}
	ids := map[int64]bool{}
	for _, root := range roots {
		if root.ID <= 0 || ids[root.ID] || !downloadPathValid(root.Path) {
			return p.pathManual(from, "the configured library roots are invalid or ambiguous")
		}
		ids[root.ID] = true
		library, err := downloadDirectoryIdentity(ctx, from, root.Path, downloadPathS6ABC)
		if err != nil {
			return p.pathManual(from, "a selected library root is not accessible as the consuming App user")
		}
		backingOverlap := library.mountDevice == observed.mountDevice && (pathWithin(library.backing, observed.backing) || pathWithin(observed.backing, library.backing))
		if backingOverlap || library.identity == observed.identity || pathWithin(observed.canonical, library.canonical) || pathWithin(library.canonical, observed.canonical) || pathWithin(local, root.Path) || pathWithin(root.Path, local) {
			return p.pathManual(from, "completed downloads and configured library roots overlap")
		}
	}
	return nil
}
