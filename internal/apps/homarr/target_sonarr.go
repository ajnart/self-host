package homarr

import (
	"context"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/apps/servarr"
	"github.com/ajnart/self-host/internal/helpers"
)

func sonarrTarget() target {
	return target{
		id: "sonarr", name: "Sonarr", kind: "sonarr", internal: (*helpers.H).InternalURL, secrets: apiKeySecret,
		widgets: []widget{plain("calendar"), plain("mediaMissing"), statWidget("shows", "downloaded", "missing", "queued")},
		verify:  verifySonarrNative, stats: verifySonarrStats,
	}
}

func verifySonarrNative(ctx context.Context, from, to *helpers.H, integrationID string) error {
	const failure = "Sonarr native identity or Media Missing data did not verify; check its saved credentials with self-host setup sonarr, then retry self-host setup homarr"
	work, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := to.HTTP(work, servarr.Request(to, servarr.V3, helpers.Request{Path: "/system/status", NoRedirect: true}))
	if err != nil || response.Status != 200 {
		return errors.New(failure)
	}
	var identity struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if response.Decode(&identity) != nil || identity.AppName != "Sonarr" || strings.TrimSpace(identity.Version) == "" {
		return errors.New(failure)
	}
	sample, err := readMediaOrganizer(work, from, integrationID)
	if err != nil {
		return err
	}
	browser, err := browserAddress(work, from, to)
	if err != nil {
		return errors.New(failure)
	}
	for _, raw := range sample.missing {
		item, ok := raw.(map[string]any)
		if !ok || !sonarrMediaItem(item, browser, "/wanted/missing") {
			return errors.New(failure)
		}
	}
	for _, raw := range sample.queued {
		item, ok := raw.(map[string]any)
		if !ok || !sonarrMediaItem(item, browser, "/activity/queue") || strings.TrimSpace(stringField(item, "status")) == "" {
			return errors.New(failure)
		}
		progress, ok := item["percentComplete"].(float64)
		if !ok || math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 || progress > 100 {
			return errors.New(failure)
		}
		remaining, present := item["timeLeft"]
		_, text := remaining.(string)
		if !present || remaining != nil && !text {
			return errors.New(failure)
		}
	}
	return nil
}

func sonarrMediaItem(item map[string]any, browser, fallback string) bool {
	id, ok := item["id"].(float64)
	if !ok || math.IsNaN(id) || math.IsInf(id, 0) || math.Trunc(id) != id || stringField(item, "type") != "episode" || strings.TrimSpace(stringField(item, "title")) == "" {
		return false
	}
	base, err := url.Parse(browser)
	if err != nil || base.Host == "" || base.Scheme != "http" && base.Scheme != "https" {
		return false
	}
	link := stringField(item, "link")
	destination, err := url.Parse(link)
	if err != nil || destination.User != nil || destination.Scheme != base.Scheme || destination.Host != base.Host {
		return false
	}
	root := strings.TrimRight(browser, "/")
	if link == root+fallback {
		return true
	}
	prefix := root + "/series/"
	if !strings.HasPrefix(link, prefix) {
		return false
	}
	slug, err := url.PathUnescape(strings.TrimPrefix(link, prefix))
	return err == nil && slug != "" && slug != "." && slug != ".." && !strings.ContainsAny(slug, "/?#")
}

func verifySonarrStats(sample statsSample) error {
	if !validCountStats(sample, "shows", "downloaded", "missing", "queued") {
		return errors.New("Homarr returned invalid Sonarr count statistics; retry self-host setup homarr")
	}
	return nil
}
