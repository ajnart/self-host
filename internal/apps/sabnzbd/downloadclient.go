package sabnzbd

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const downloadReadPrerequisiteProbe = `for directory in "$@"; do
test -d "$directory"
test -r "$directory"
test -w "$directory"
test -x "$directory"
done
printf 'existing-download-folders\n'`

func downloadClientManual(reason string) error {
	return &helpers.ManualError{Hint: "Review SABnzbd > Config > Categories and Folders: " + reason + ". Preserve existing category folders, scripts, jobs and media. Choose a dedicated category with job folders enabled and no sorting or moving script, then retry the consuming App's Setup. Setup does not change categories or migrate storage."}
}

func downloadClientConfig(ctx context.Context, h *helpers.H, section, keyword string) (map[string]any, error) {
	result, err := apiForm(ctx, h, map[string]string{"mode": "get_config", "section": section, "keyword": keyword, "apikey": h.Secret(h.Key("api_key"))})
	if err != nil {
		return nil, errors.New("SABnzbd download-client configuration could not be read; retry self-host setup sabnzbd and the consuming App")
	}
	config, ok := result["config"].(map[string]any)
	if !ok {
		return nil, errors.New("SABnzbd returned invalid download-client configuration")
	}
	return config, nil
}

func downloadReadPrerequisites(ctx context.Context, h *helpers.H) (string, error) {
	key := h.Secret(h.Key("api_key"))
	h.Log.Secret(key)
	folders := make([]string, 0, 2)
	for _, setting := range []string{"download_dir", "complete_dir"} {
		folder, err := readSetting(ctx, h, key, setting)
		if err != nil {
			return "", &helpers.ManualError{Hint: "SABnzbd's existing " + setting + " could not be read safely. Run self-host setup sabnzbd to recover its full API key, then retry the consuming App's Setup. Preserve credentials and folders; do not rotate keys or disable authentication."}
		}
		folder = strings.TrimRight(folder, "/")
		if !path.IsAbs(folder) || folder == "" || path.Clean(folder) != folder || strings.TrimSpace(folder) != folder || strings.ContainsAny(folder, "\r\n\x00\\") {
			return "", downloadClientManual("the existing " + setting + " is not a supported absolute folder; resolve and review it explicitly before retrying because resolving it through status can create directories")
		}
		folders = append(folders, folder)
	}
	config, err := downloadClientConfig(ctx, h, "categories", "*")
	if err != nil {
		return "", downloadClientManual("the existing default (*) category could not be read without initializing defaults; verify the full API key with self-host setup sabnzbd and review that category explicitly")
	}
	categories, ok := config["categories"].([]any)
	if !ok || len(categories) != 1 {
		return "", downloadClientManual("the default (*) category is missing or unreadable; initialize and review categories explicitly in SABnzbd before retrying")
	}
	category, ok := categories[0].(map[string]any)
	if !ok || category["name"] != "*" {
		return "", downloadClientManual("the existing default (*) category could not be verified without initializing defaults; review categories explicitly before retrying")
	}
	if h.Config.Exec == nil {
		return "", downloadClientManual("read-only App-user access checks are unavailable; verify the existing incomplete and completed folders in the running App before retrying")
	}
	result, err := h.Exec(ctx, nil, "s6-setuidgid", "abc", "sh", "-eu", "-c", downloadReadPrerequisiteProbe, "self-host-sabnzbd-read-prerequisites", folders[0], folders[1])
	if err != nil || result.ExitCode != 0 || len(result.Stderr) != 0 || string(result.Stdout) != "existing-download-folders\n" {
		return "", downloadClientManual("the existing incomplete and completed folders could not both be verified as accessible directories by the App user; restore or review the folders and mounts explicitly before retrying; this Link creates no directory")
	}
	return folders[1], nil
}

func CompletedDownloadPath(ctx context.Context, h *helpers.H, category string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if category == "" || category == "*" || strings.TrimSpace(category) != category || strings.ContainsAny(category, "\r\n\x00") {
		return "", downloadClientManual("choose an explicit isolated category, not the default catch-all")
	}
	root, err := downloadReadPrerequisites(ctx, h)
	if err != nil {
		return "", err
	}
	if _, err := verifyAPIKey(ctx, h, h.Secret(h.Key("api_key"))); err != nil {
		return "", &helpers.ManualError{Hint: "Run self-host setup sabnzbd to recover and verify its existing full API key, then retry the consuming App's Setup. The NZB upload-only key is insufficient. Do not rotate credentials or disable authentication."}
	}
	config, err := downloadClientConfig(ctx, h, "categories", "")
	if err != nil {
		return "", err
	}
	categories, ok := config["categories"].([]any)
	if !ok {
		return "", errors.New("SABnzbd did not return an explicit category list")
	}
	var selected map[string]any
	defaultScript := ""
	names := map[string]bool{}
	for _, raw := range categories {
		entry, ok := raw.(map[string]any)
		if !ok {
			return "", errors.New("SABnzbd returned a malformed category")
		}
		name, named := entry["name"].(string)
		_, directory := entry["dir"].(string)
		script, scripted := entry["script"].(string)
		if !named || name == "" || names[name] || !directory || !scripted {
			return "", errors.New("SABnzbd returned incomplete or duplicate categories")
		}
		names[name] = true
		if name == "*" {
			defaultScript = script
		}
		if name == category {
			selected = entry
		}
	}
	if selected == nil {
		return "", downloadClientManual("the Arr category " + category + " is missing; create that exact category explicitly and review its folder")
	}
	directory := selected["dir"].(string)
	if strings.ContainsAny(directory, "*\r\n\x00\\") {
		return "", downloadClientManual("the chosen category disables job folders or has an unsupported directory")
	}
	script := selected["script"].(string)
	if script == "Default" {
		script = defaultScript
	}
	if script != "None" {
		return "", downloadClientManual("the selected category's effective script is not verified as None")
	}
	if err := verifyDownloadClientSorting(ctx, h, category); err != nil {
		return "", err
	}
	if directory != "" && strings.TrimRight(directory, "/") == "" {
		return "", downloadClientManual("the category folder cannot be the filesystem root")
	}
	directory = strings.TrimRight(directory, "/")
	if directory == "" {
		return root, nil
	}
	if path.Clean(directory) != directory || directory == "." || directory == ".." || strings.HasPrefix(directory, "../") {
		return "", downloadClientManual("the category folder contains an ambiguous relative path")
	}
	if path.IsAbs(directory) {
		return directory, nil
	}
	return path.Join(root, directory), nil
}

func verifyDownloadClientSorting(ctx context.Context, h *helpers.H, category string) error {
	config, err := downloadClientConfig(ctx, h, "sorters", "")
	if err != nil {
		return err
	}
	if len(config) == 0 {
		return nil
	}
	sorters, ok := config["sorters"].([]any)
	if !ok {
		return errors.New("SABnzbd returned invalid sorting configuration")
	}
	for _, raw := range sorters {
		sorter, ok := raw.(map[string]any)
		if !ok {
			return errors.New("SABnzbd returned an invalid sorter")
		}
		active, ok := sorter["is_active"].(float64)
		categories, hasCategories := sorter["sort_cats"].([]any)
		if !ok || active != 0 && active != 1 || !hasCategories {
			return errors.New("SABnzbd returned incomplete sorter settings")
		}
		applies := len(categories) == 0
		for _, rawCategory := range categories {
			name, ok := rawCategory.(string)
			if !ok {
				return errors.New("SABnzbd returned an invalid sorting category")
			}
			applies = applies || name == category
		}
		if active == 1 && applies {
			return downloadClientManual("sorting is enabled for the selected category; review it explicitly instead of changing it automatically")
		}
	}
	return nil
}
