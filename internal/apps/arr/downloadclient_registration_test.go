package arr_test

import (
	"slices"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/apps/arr"
	"github.com/ajnart/self-host/internal/apps/lidarr"
)

func TestProductionConsumersRegisterEachDownloadClientOnce(t *testing.T) {
	want := []string{"qbittorrent", "sabnzbd", "nzbget", "deluge", "transmission"}
	for _, consumer := range []app.App{arr.Sonarr(), arr.Radarr(), lidarr.App{}} {
		t.Run(consumer.Info().ID, func(t *testing.T) {
			links := consumer.Links()
			targets := make([]string, 0, len(links))
			for _, link := range links {
				targets = append(targets, link.Target)
				if link.ID != "link-"+link.Target || link.Apply == nil || link.Remove == nil {
					t.Fatalf("incomplete downloader Link %q for %q", link.ID, link.Target)
				}
			}
			if !slices.Equal(targets, want) {
				t.Fatalf("downloader targets %v, want %v", targets, want)
			}
		})
	}
}
