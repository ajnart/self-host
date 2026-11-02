package homarr

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/apps/sportarr"
	"github.com/ajnart/self-host/internal/helpers"
)

func sportarrTarget() target {
	return target{
		id: "sportarr", name: "Sportarr", kind: "sportarr",
		internal: (*helpers.H).InternalURL, secrets: apiKeySecret,
		widgets:   []widget{plain("calendar"), plain("mediaMissing")},
		preflight: verifySportarrReady, verify: verifySportarr,
	}
}

func verifySportarrReady(ctx context.Context, from, to *helpers.H) error {
	if err := (sportarr.App{}).Check(ctx, to); err != nil {
		if errors.Is(err, helpers.ErrManual) {
			from.Info(err.Error())
			return err
		}
		return errors.New("Sportarr's native identity or existing Admin login could not be verified; run self-host setup sportarr, then self-host setup homarr")
	}
	return nil
}

func verifySportarr(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := verifySportarrReady(ctx, from, to); err != nil {
		return err
	}
	address, err := browserAddress(ctx, from, to)
	if err != nil {
		return errors.New("Sportarr's browser Address could not be resolved; retry self-host setup homarr")
	}
	base, err := url.Parse(address)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || base.Scheme != "http" && base.Scheme != "https" {
		return errors.New("Sportarr's browser Address is invalid; review Access before retrying self-host setup homarr")
	}
	mediaCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	sample, err := readMediaOrganizer(mediaCtx, from, integrationID)
	if err != nil {
		return err
	}
	if sample.missingCount > 0 && len(sample.missing) == 0 || sample.queuedCount > 0 && len(sample.queued) == 0 {
		return errors.New("Homarr returned Sportarr media totals without the first page of events; retry self-host setup homarr")
	}
	for _, section := range []struct {
		items  []any
		queued bool
	}{{items: sample.missing}, {items: sample.queued, queued: true}} {
		seen := map[float64]bool{}
		for _, value := range section.items {
			item, ok := value.(map[string]any)
			id, validID := countValue(item["id"])
			if !ok || !validID || id == 0 || id > 2147483647 || seen[id] || !sportarrMediaItem(item, section.queued, base) {
				return errors.New("Homarr returned incomplete Sportarr event or queue data; verify the native integration and retry self-host setup homarr")
			}
			seen[id] = true
		}
	}
	return nil
}

func sportarrMediaItem(item map[string]any, queued bool, base *url.URL) bool {
	if strings.TrimSpace(stringField(item, "title")) == "" || stringField(item, "type") != "episode" || !sportarrMediaLink(stringField(item, "link"), base) {
		return false
	}
	for _, field := range []string{"year", "seasonNumber", "episodeNumber"} {
		if item[field] == nil && (queued || field == "year") {
			continue
		}
		if _, ok := countValue(item[field]); !ok {
			return false
		}
	}
	for _, field := range []string{"seriesTitle", "imageUrl"} {
		if item[field] != nil {
			if _, ok := item[field].(string); !ok {
				return false
			}
		}
	}
	if !queued {
		return true
	}
	switch stringField(item, "status") {
	case "queued", "downloading", "paused", "warning", "completed", "failed":
	default:
		return false
	}
	progress, ok := countValue(item["percentComplete"])
	if !ok || progress > 100 {
		return false
	}
	remaining, present := item["timeLeft"]
	_, text := remaining.(string)
	return present && (remaining == nil || text)
}

func sportarrMediaLink(address string, base *url.URL) bool {
	link, err := url.Parse(address)
	if err != nil || link.Scheme != base.Scheme || !strings.EqualFold(link.Hostname(), base.Hostname()) || link.User != nil || link.RawQuery != "" || link.ForceQuery || link.Fragment != "" {
		return false
	}
	port := func(value *url.URL) string {
		if explicit := value.Port(); explicit != "" {
			return explicit
		}
		if value.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	if port(link) != port(base) {
		return false
	}
	prefix := strings.TrimRight(base.Path, "/")
	if link.Path == prefix+"/activity" || link.Path == prefix+"/leagues" {
		return true
	}
	id, found := strings.CutPrefix(link.Path, prefix+"/leagues/")
	if !found || id == "" || strings.Trim(id, "0123456789") != "" {
		return false
	}
	value, err := strconv.ParseUint(id, 10, 31)
	return err == nil && value > 0
}
