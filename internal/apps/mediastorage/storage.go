// Package mediastorage keeps cooperating media Apps on the same container paths.
// An absent layout preserves Projects created before the shared data tree.
package mediastorage

import "strings"

func Shared(settings map[string]string) bool { return settings["MEDIA_LAYOUT"] == "shared" }

func Media(settings map[string]string) string {
	if Shared(settings) {
		return "/data/media"
	}
	return "/media"
}

func Torrents(settings map[string]string) string {
	if Shared(settings) {
		return "/data/torrents"
	}
	return "/downloads"
}

// Identity selects only image-supported media identities. Other Apps retain
// their own image contract and the shared Host settings.
func Identity(id string, settings map[string]string) (uid, mask string) {
	if settings["MEDIA_SEPARATE_USERS"] == "on" {
		return "${" + strings.ToUpper(id) + "_PUID}", "${MEDIA_UMASK}"
	}
	return "${PUID}", "${UMASK}"
}
