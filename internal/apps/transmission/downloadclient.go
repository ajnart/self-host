package transmission

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

var downloadClientCategory = regexp.MustCompile(`^\.?[-a-zA-Z]+$`)

func downloadClientManual(reason string) error {
	return &helpers.ManualError{Hint: "Review Transmission's existing RPC login, download folders and the consuming App's Transmission category/directory: " + reason + ". Preserve credentials, RPC protection, active torrents, files and mounts. Recover the accepted Project login with self-host setup transmission, then retry the consuming App's Setup. This Link does not change daemon folders, add torrents or migrate storage."}
}

func DownloadClientPaths(ctx context.Context, h *helpers.H, category, directory string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !downloadClientCategory.MatchString(category) {
		return nil, downloadClientManual("choose an explicit isolated category supported by the Arr provider, using letters and hyphens with an optional leading dot")
	}
	if h.AdminUser() == "" || h.AdminPassword() == "" {
		return nil, downloadClientManual("the Project's existing Admin login is missing")
	}
	h.Log.Secret(h.AdminPassword())
	ok, err := rpcSession(ctx, h, h.AdminUser(), h.AdminPassword())
	if err != nil || !ok {
		return nil, downloadClientManual("the saved authentication policy, authenticated RPC version/session or explicit queue data could not be verified; keep authentication enabled and review IP/host policy without bypassing it")
	}
	config, err := rpcRead(ctx, h, h.AdminUser(), h.AdminPassword(), "session-get", map[string]any{
		"fields": []string{"download-dir", "incomplete-dir-enabled", "incomplete-dir"},
	})
	if err != nil {
		return nil, downloadClientManual("the authenticated download-folder configuration could not be read")
	}
	root, rooted := config["download-dir"].(string)
	incompleteEnabled, enabled := config["incomplete-dir-enabled"].(bool)
	if !rooted || root == "" || !enabled {
		return nil, downloadClientManual("the daemon did not return an explicit download folder and incomplete-folder policy")
	}
	paths := []string{strings.TrimRight(directory, "/")}
	if directory == "" {
		root = strings.TrimRight(root, "/")
		paths = []string{root, root + "/" + category}
	}
	if incompleteEnabled {
		incomplete, ok := config["incomplete-dir"].(string)
		if !ok || incomplete == "" {
			return nil, downloadClientManual("the enabled incomplete folder is missing or unreadable")
		}
		paths = append(paths, strings.TrimRight(incomplete, "/"))
	}
	return paths, nil
}
