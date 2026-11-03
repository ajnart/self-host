package arr

import (
	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/servarr"
)

var applicationImplementations = map[string]string{
	"sonarr": "Sonarr",
	"radarr": "Radarr",
	"lidarr": "Lidarr",
}

func downloadClientLinks(categoryField, category string) []app.Link {
	return servarr.DownloadClientLinks(apiV3, categoryField, category)
}

func applicationLink(target string) app.Link {
	return servarr.ApplicationLink(target, applicationImplementations[target])
}
