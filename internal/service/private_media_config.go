package service

import (
	"context"
	"errors"
)

// These fixed paths belong to images whose separate file owners we support.
// Reading as the App user keeps configuration private without requiring the
// Project owner to retain sudo for later Setup or API key recovery.
func (ss *session) privateMediaConfigReader(id, service string) func(context.Context, string) ([]byte, error) {
	if ss.settings["MEDIA_SEPARATE_USERS"] != "on" {
		return nil
	}
	rel, path := "", ""
	switch id {
	case "sonarr", "radarr", "lidarr":
		rel, path = "config/config.xml", "/config/config.xml"
	case "sabnzbd":
		rel, path = "config/sabnzbd.ini", "/config/sabnzbd.ini"
	case "bazarr":
		rel, path = "config/config/config.yaml", "/config/config/config.yaml"
	default:
		return nil
	}
	return func(ctx context.Context, requested string) ([]byte, error) {
		if requested != rel {
			return nil, errors.New("unsupported private App configuration path")
		}
		result, err := ss.compose.Exec(ctx, service, nil, "s6-setuidgid", "abc", "cat", path)
		if err != nil || result.ExitCode != 0 {
			// Config output and stderr may contain credentials.
			return nil, errors.New("private App configuration is not readable in its container yet")
		}
		return result.Stdout, nil
	}
}
