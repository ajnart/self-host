package sabnzbd

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

func downloadClientManual(reason string) error {
	return &helpers.ManualError{Hint: "Review SABnzbd > Config > Categories and Folders: " + reason + ". Preserve existing category folders, scripts, jobs and media. Choose a dedicated category with job folders enabled and no sorting or moving script, then retry the consuming App's Setup. Setup does not change categories or migrate storage."}
}

func downloadClientConfig(ctx context.Context, h *helpers.H, section string) (map[string]any, error) {
	result, err := apiForm(ctx, h, map[string]string{"mode": "get_config", "section": section, "apikey": h.Secret(h.Key("api_key"))})
	if err != nil {
		return nil, errors.New("SABnzbd download-client configuration could not be read; retry self-host setup sabnzbd and the consuming App")
	}
	config, ok := result["config"].(map[string]any)
	if !ok {
		return nil, errors.New("SABnzbd returned invalid download-client configuration")
	}
	return config, nil
}

func CompletedDownloadPath(ctx context.Context, h *helpers.H, category string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := verifyAPIKey(ctx, h, h.Secret(h.Key("api_key"))); err != nil {
		return "", &helpers.ManualError{Hint: "Run self-host setup sabnzbd to recover and verify its existing full API key, then retry the consuming App's Setup. The NZB upload-only key is insufficient. Do not rotate credentials or disable authentication."}
	}
	if category == "" || category == "*" || strings.TrimSpace(category) != category || strings.ContainsAny(category, "\r\n\x00") {
		return "", downloadClientManual("choose an explicit isolated category, not the default catch-all")
	}
	config, err := downloadClientConfig(ctx, h, "categories")
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
	root, err := readSetting(ctx, h, h.Secret(h.Key("api_key")), "complete_dir")
	if err != nil {
		return "", errors.New("SABnzbd did not return its completed download folder")
	}
	if !path.IsAbs(root) {
		result, err := apiForm(ctx, h, map[string]string{"mode": "fullstatus", "skip_dashboard": "1", "apikey": h.Secret(h.Key("api_key"))})
		if err != nil {
			return "", errors.New("SABnzbd's resolved completed folder could not be read")
		}
		status, ok := result["status"].(map[string]any)
		if !ok {
			return "", errors.New("SABnzbd returned invalid completed-folder status")
		}
		root, ok = status["completedir"].(string)
		if !ok {
			return "", errors.New("SABnzbd did not identify its resolved completed folder")
		}
	}
	if directory != "" && strings.TrimRight(directory, "/") == "" {
		return "", downloadClientManual("the category folder cannot be the filesystem root")
	}
	root, directory = strings.TrimRight(root, "/"), strings.TrimRight(directory, "/")
	if !path.IsAbs(root) || root == "/" || path.Clean(root) != root || strings.ContainsAny(root, "\r\n\x00\\") {
		return "", downloadClientManual("the completed download folder is not a supported absolute path")
	}
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
	config, err := downloadClientConfig(ctx, h, "sorters")
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
