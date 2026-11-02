package deluge

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

type downloadLabelOptions struct {
	ApplyMoveCompleted *bool   `json:"apply_move_completed"`
	MoveCompleted      *bool   `json:"move_completed"`
	MoveCompletedPath  *string `json:"move_completed_path"`
}

func downloadClientManual(reason string) error {
	return &helpers.ManualError{Hint: "Review Deluge's existing Web password, Connection Manager, Label plugin and download folders: " + reason + ". Preserve the chosen daemon, labels, torrents, credentials and paths; do not install plugins or enable remote daemon access. Run self-host setup deluge after explicit recovery, then retry the consuming App's Setup."}
}

func selectedDownloadDaemon(ctx context.Context, s *helpers.Session) error {
	connected, err := daemonConnected(ctx, s)
	if err != nil || !connected {
		return downloadClientManual("the intended local daemon must already be connected; select it explicitly in Connection Manager")
	}
	response, err := rpc(ctx, s, "web.get_config")
	if err != nil {
		return downloadClientManual("the saved daemon selection could not be read")
	}
	var config struct {
		DefaultDaemon *string `json:"default_daemon"`
	}
	if json.Unmarshal(response.Result, &config) != nil || config.DefaultDaemon == nil {
		return downloadClientManual("the saved daemon selection is unreadable")
	}
	response, err = rpc(ctx, s, "web.get_hosts")
	if err != nil {
		return downloadClientManual("the existing daemon identities could not be read")
	}
	var hosts [][]json.RawMessage
	if json.Unmarshal(response.Result, &hosts) != nil || hosts == nil {
		return downloadClientManual("the existing daemon identities are unreadable")
	}
	selected := ""
	ids := map[string]bool{}
	for _, host := range hosts {
		var id, address, username string
		var port int
		if len(host) != 4 || json.Unmarshal(host[0], &id) != nil || id == "" || ids[id] || json.Unmarshal(host[1], &address) != nil || json.Unmarshal(host[2], &port) != nil || json.Unmarshal(host[3], &username) != nil {
			return downloadClientManual("the existing daemon identities are invalid or ambiguous")
		}
		ids[id] = true
		if port != 58846 || !slices.Contains([]string{"127.0.0.1", "localhost", "::1"}, address) || *config.DefaultDaemon != "" && id != *config.DefaultDaemon {
			continue
		}
		if selected != "" {
			return downloadClientManual("more than one local daemon matches the saved selection")
		}
		selected = id
	}
	if selected == "" {
		return downloadClientManual("the saved selection does not identify the intended local daemon")
	}
	response, err = rpc(ctx, s, "web.get_host_status", selected)
	if err != nil {
		return downloadClientManual("the selected local daemon could not be verified")
	}
	var status []string
	if json.Unmarshal(response.Result, &status) != nil || len(status) != 3 || status[0] != selected || status[1] != "Connected" || strings.TrimSpace(status[2]) == "" {
		return downloadClientManual("the Web session is not verified as connected to the selected local daemon")
	}
	return nil
}

func DownloadPaths(ctx context.Context, h *helpers.H, category, importedCategory, downloadDirectory, completedDirectory string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if category == "" {
		return nil, downloadClientManual("choose an explicit isolated Arr category")
	}
	for _, label := range []string{category, importedCategory} {
		if strings.Trim(label, "-abcdefghijklmnopqrstuvwxyz0123456789") != "" {
			return nil, downloadClientManual("Arr labels may contain only lowercase a-z, 0-9 and hyphens")
		}
	}
	s, err := webSession(ctx, h)
	if err != nil {
		return nil, downloadClientManual("the saved Web password was not accepted; restore that secret through the Project's private credential boundary without rotating it")
	}
	defer rpc(ctx, s, "auth.delete_session")
	if err := selectedDownloadDaemon(ctx, s); err != nil {
		return nil, err
	}
	response, err := rpc(ctx, s, "core.get_config")
	if err != nil {
		return nil, downloadClientManual("the existing daemon download settings could not be read")
	}
	var config struct {
		DownloadLocation  *string  `json:"download_location"`
		MoveCompleted     *bool    `json:"move_completed"`
		MoveCompletedPath *string  `json:"move_completed_path"`
		EnabledPlugins    []string `json:"enabled_plugins"`
	}
	if json.Unmarshal(response.Result, &config) != nil || config.DownloadLocation == nil || config.MoveCompleted == nil || config.MoveCompletedPath == nil || config.EnabledPlugins == nil {
		return nil, downloadClientManual("the existing download folders and plugin state are unreadable")
	}
	if !slices.Contains(config.EnabledPlugins, "Label") {
		return nil, downloadClientManual("enable the built-in Label plugin explicitly in Preferences > Plugins before Arr tests; Setup will not enable it")
	}
	response, err = rpc(ctx, s, "system.listMethods")
	if err != nil {
		return nil, downloadClientManual("the Label plugin capabilities could not be read")
	}
	var methods []string
	if json.Unmarshal(response.Result, &methods) != nil {
		return nil, downloadClientManual("the Label plugin capabilities are unreadable")
	}
	for _, method := range []string{"label.get_labels", "label.get_options", "label.set_torrent"} {
		if !slices.Contains(methods, method) {
			return nil, downloadClientManual("the Label plugin is not ready; verify its activation explicitly before retrying")
		}
	}
	response, err = rpc(ctx, s, "label.get_labels")
	if err != nil {
		return nil, downloadClientManual("the existing Label categories could not be read")
	}
	var labels []string
	if json.Unmarshal(response.Result, &labels) != nil || labels == nil {
		return nil, downloadClientManual("the existing Label categories are unreadable")
	}
	options := map[string]downloadLabelOptions{}
	for _, label := range []string{category, importedCategory} {
		if label == "" {
			continue
		}
		if !slices.Contains(labels, label) {
			return nil, downloadClientManual("create the selected Arr label " + label + " explicitly before retrying; Arr's test would otherwise create it")
		}
		if _, ok := options[label]; ok {
			continue
		}
		response, err = rpc(ctx, s, "label.get_options", label)
		if err != nil {
			return nil, downloadClientManual("the selected Label download options could not be read")
		}
		var selected downloadLabelOptions
		if json.Unmarshal(response.Result, &selected) != nil || selected.ApplyMoveCompleted == nil || selected.MoveCompleted == nil || selected.MoveCompletedPath == nil {
			return nil, downloadClientManual("the selected Label download options are unreadable")
		}
		options[label] = selected
	}
	if err := verifyQueue(ctx, s); err != nil {
		return nil, downloadClientManual("the authenticated daemon did not return its connected download queue and explicit transfer rates")
	}
	if err := selectedDownloadDaemon(ctx, s); err != nil {
		return nil, err
	}
	download := *config.DownloadLocation
	if downloadDirectory != "" {
		download = downloadDirectory
	}
	move, completed := *config.MoveCompleted, *config.MoveCompletedPath
	if completedDirectory != "" {
		move, completed = true, completedDirectory
	}
	label := options[category]
	if *label.ApplyMoveCompleted {
		move, completed = *label.MoveCompleted, *label.MoveCompletedPath
	}
	paths := []string{download}
	if move {
		paths = append(paths, completed)
	}
	reported := *config.DownloadLocation
	if completedDirectory != "" {
		reported = completedDirectory
	} else if downloadDirectory != "" {
		reported = downloadDirectory
	} else if *label.ApplyMoveCompleted && *label.MoveCompleted {
		reported = *label.MoveCompletedPath
	} else if *config.MoveCompleted {
		reported = *config.MoveCompletedPath
	}
	paths = append(paths, reported)
	if importedCategory != "" {
		imported := options[importedCategory]
		if *imported.ApplyMoveCompleted && *imported.MoveCompleted {
			paths = append(paths, *imported.MoveCompletedPath)
		}
	}
	unique := make([]string, 0, len(paths))
	for _, directory := range paths {
		if !slices.Contains(unique, directory) {
			unique = append(unique, directory)
		}
	}
	return unique, nil
}
