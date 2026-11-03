package nzbget

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

func nzbgetDownloadManual(reason string) error {
	return &helpers.ManualError{Hint: "Review NZBGet > Settings > Categories, Paths and Extension Scripts: " + reason + ". Preserve existing categories, scripts, history, files and credentials. Choose an existing exact category and review its completed folder explicitly, then retry the consuming App's Setup. This Link creates no category, changes no global setting and does not migrate storage."}
}

func CompletedDownloadPath(ctx context.Context, h *helpers.H, category string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if category == "" || category == "." || category == ".." || strings.TrimSpace(category) != category || strings.ContainsAny(category, "/\\*?\r\n\x00") {
		return "", nzbgetDownloadManual("choose an explicit isolated category without path separators or wildcard characters")
	}
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return "", &helpers.ManualError{Hint: "Restore NZBGet's existing Admin login through the Project's private credential boundary and run self-host setup nzbget, then retry the consuming App's Setup. Do not rotate credentials or use Homarr's restricted login."}
	}
	h.Log.Secret(h.AdminPassword())
	if ok, err := adminWorks(ctx, h); err != nil || !ok {
		return "", &helpers.ManualError{Hint: "Run self-host setup nzbget to verify that its existing Admin login is accepted and anonymous RPC is rejected on the supported NZBGet version, then retry the consuming App's Setup. Preserve authentication and credentials; do not use an add-only or Homarr login."}
	}
	var entries []struct {
		Name  string
		Value *string
	}
	if err := callRPC(ctx, h, h.AdminUser(), h.AdminPassword(), "config", &entries); err != nil {
		return "", &helpers.ManualError{Hint: "NZBGet's active configuration could not be read with its accepted Admin login. Run self-host setup nzbget and review RPC access before retrying the consuming App's Setup. Preserve existing credentials; a version-only or add-only login is insufficient."}
	}
	if entries == nil {
		return "", errors.New("NZBGet did not return an explicit active configuration; retry self-host setup nzbget")
	}
	config := make(map[string]string, len(entries))
	for _, entry := range entries {
		if _, duplicate := config[entry.Name]; duplicate || entry.Name == "" || entry.Value == nil {
			return "", errors.New("NZBGet returned incomplete or duplicate active configuration; retry self-host setup nzbget")
		}
		config[entry.Name] = *entry.Value
	}
	if config["ControlUsername"] != h.AdminUser() || config["ControlPassword"] != h.AdminPassword() || config["ControlUsername"] == "***" || config["ControlPassword"] == "***" {
		return "", &helpers.ManualError{Hint: "NZBGet's active control credentials do not identify the accepted full Admin login. Restore the existing control login through the Project's private credential boundary and run self-host setup nzbget, then retry the consuming App. Do not substitute restricted, Homarr or add-only access and do not rotate credentials."}
	}
	keepHistory, err := strconv.ParseUint(config["KeepHistory"], 10, 32)
	if err != nil || keepHistory == 0 || keepHistory > 25000 || strings.Trim(config["KeepHistory"], "0123456789") != "" {
		return "", nzbgetDownloadManual("KeepHistory must be an existing positive number of days no greater than 25000 for Arr to see completed downloads")
	}
	extensions, present := config["Extensions"]
	if !present || extensions != "" || config["DefScript"] != "" {
		return "", nzbgetDownloadManual("global extension scripts are enabled or unreadable, so the final download destination cannot be verified without changing the person's choices")
	}
	selected := ""
	names := map[string]bool{}
	for index := 1; index < 100; index++ {
		prefix := "Category" + strconv.Itoa(index) + "."
		name, present := config[prefix+"Name"]
		if !present {
			break
		}
		key := strings.ToLower(name)
		if name == "" || names[key] || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\r\n\x00") {
			return "", nzbgetDownloadManual("the active category sequence has an empty or ambiguous name")
		}
		names[key] = true
		if name == category {
			selected = prefix
		}
	}
	if selected == "" {
		return "", nzbgetDownloadManual("the Arr category is not present with its exact case in NZBGet's contiguous category sequence; add or select that category explicitly and apply its settings")
	}
	if config[selected+"Extensions"] != "" || config[selected+"DefScript"] != "" {
		return "", nzbgetDownloadManual("the selected category has extension scripts, so its final download destination needs explicit review")
	}
	completed := config[selected+"DestDir"]
	if completed == "" {
		completed, err = nzbgetCompletedFolder(config["DestDir"])
		if err != nil {
			return "", err
		}
		switch config["AppendCategoryDir"] {
		case "yes":
			stem, _, _ := strings.Cut(category, ".")
			stem = strings.ToUpper(stem)
			reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9'
			if reserved || strings.HasSuffix(category, ".") || strings.ContainsAny(category, `":<>|`) || strings.ContainsFunc(category, func(char rune) bool { return char < 32 }) {
				return "", nzbgetDownloadManual("NZBGet sanitizes the selected category's appended directory name, so its actual completed path needs explicit review")
			}
			completed = path.Join(completed, category)
		case "no":
		default:
			return "", nzbgetDownloadManual("AppendCategoryDir is missing or unsupported")
		}
	}
	return nzbgetCompletedFolder(completed)
}

func nzbgetCompletedFolder(directory string) (string, error) {
	directory = strings.TrimRight(directory, "/")
	if directory == "" || !path.IsAbs(directory) || path.Clean(directory) != directory || strings.TrimSpace(directory) != directory || strings.ContainsAny(directory, "\r\n\x00\\") || strings.Contains(directory, "${") {
		return "", nzbgetDownloadManual("the selected completed folder is not an unambiguous resolved absolute directory; review its active path without rewriting queued or historical jobs")
	}
	return directory, nil
}
