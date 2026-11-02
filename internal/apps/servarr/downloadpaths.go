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
stat -L -c '%d:%i' -- "$1"`

func downloadPathValid(value string) bool {
	return strings.HasPrefix(value, "/") && value != "/" && path.Clean(value) == value && !strings.ContainsAny(value, "\r\n\x00\\")
}

func pathWithin(parent, child string) bool {
	return parent == child || strings.HasPrefix(child, parent+"/")
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

func (p providerLink) mappedDownloadPath(ctx context.Context, from, to *helpers.H, completed string) (string, error) {
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
		if !p.pointsAtTarget(mapping.Host, to) {
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

func downloadDirectoryIdentity(ctx context.Context, h *helpers.H, directory string) (string, string, error) {
	if h.Config.Exec == nil || !downloadPathValid(directory) {
		return "", "", fmt.Errorf("directory access is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := h.Exec(ctx, nil, "s6-setuidgid", "abc", "sh", "-eu", "-c", downloadDirectoryProbe, "self-host-download-path", directory)
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 {
		return "", "", fmt.Errorf("directory access could not be verified as the App user")
	}
	lines := strings.Split(strings.TrimSpace(string(result.Stdout)), "\n")
	if len(lines) != 2 || !downloadPathValid(lines[0]) {
		return "", "", fmt.Errorf("directory identity is unavailable")
	}
	parts := strings.Split(lines[1], ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("directory identity is invalid")
	}
	for _, part := range parts {
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil || number == 0 || strconv.FormatUint(number, 10) != part {
			return "", "", fmt.Errorf("directory identity is invalid")
		}
	}
	return lines[0], lines[1], nil
}

func (p providerLink) verifyDownloadPath(ctx context.Context, from, to *helpers.H, completed string) error {
	if !downloadPathValid(completed) {
		return p.pathManual(from, "the downloader did not identify a supported absolute completed folder")
	}
	local, err := p.mappedDownloadPath(ctx, from, to, completed)
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
	_, remoteIdentity, err := downloadDirectoryIdentity(ctx, to, completed)
	if err != nil {
		return p.pathManual(from, "the downloader cannot verify access to its completed folder as its App user")
	}
	canonical, localIdentity, err := downloadDirectoryIdentity(ctx, from, local)
	if err != nil || remoteIdentity != localIdentity {
		return p.pathManual(from, "the completed folder is not verified as the same accessible directory in both Apps; legacy App-local folders are not shared storage")
	}
	ids := map[int64]bool{}
	for _, root := range roots {
		if root.ID <= 0 || ids[root.ID] || !downloadPathValid(root.Path) {
			return p.pathManual(from, "the configured library roots are invalid or ambiguous")
		}
		ids[root.ID] = true
		library, libraryIdentity, err := downloadDirectoryIdentity(ctx, from, root.Path)
		if err != nil {
			return p.pathManual(from, "a selected library root is not accessible as the consuming App user")
		}
		if libraryIdentity == localIdentity || pathWithin(canonical, library) || pathWithin(library, canonical) || pathWithin(local, root.Path) || pathWithin(root.Path, local) {
			return p.pathManual(from, "completed downloads and configured library roots overlap")
		}
	}
	return nil
}
