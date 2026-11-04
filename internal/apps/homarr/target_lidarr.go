package homarr

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/apps/lidarr"
	"github.com/ajnart/self-host/internal/helpers"
)

func lidarrTarget() target {
	return target{
		id: "lidarr", name: "Lidarr", kind: "lidarr",
		internal: fixed("http://lidarr:8686"), secrets: apiKeySecret,
		widgets:   []widget{plain("calendar"), statWidget("artists", "monitored", "upcoming")},
		preflight: verifyLidarrReady,
		verify:    verifyLidarr,
		stats:     verifyLidarrStats,
	}
}

func verifyLidarrReady(ctx context.Context, from, to *helpers.H) error {
	if err := (lidarr.App{}).Check(ctx, to); err != nil {
		if errors.Is(err, helpers.ErrManual) {
			from.Info(err.Error())
			return err
		}
		return errors.New("Lidarr's saved API key or Admin login is not ready; run self-host setup lidarr, then self-host setup homarr")
	}
	return nil
}

func verifyLidarr(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := verifyLidarrReady(ctx, from, to); err != nil {
		return err
	}
	libraryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	resp, err := to.HTTP(libraryCtx, helpers.Request{
		Path: "/api/v1/artist", NoRedirect: true,
		Header: map[string]string{"X-Api-Key": to.Secret(to.Key("api_key"))},
	})
	cancel()
	if err != nil {
		return err
	}
	var artists []struct{ Monitored *bool }
	if resp.Status != 200 || resp.Decode(&artists) != nil || artists == nil {
		return errors.New("Lidarr did not return its authenticated artist library; retry self-host setup lidarr")
	}
	for _, artist := range artists {
		if artist.Monitored == nil {
			return errors.New("Lidarr returned incomplete artist monitoring data; retry self-host setup lidarr")
		}
	}
	events, err := readCalendarEvents(ctx, from, integrationID, "lidarr")
	if err != nil {
		return err
	}
	if !calendarSubtitles(events) {
		return errors.New("Homarr returned invalid Lidarr artist data; retry self-host setup homarr")
	}
	return nil
}

func verifyLidarrStats(sample statsSample) error {
	return verifyLibraryCounts(sample, "Lidarr", "artists")
}

func verifyLibraryCounts(sample statsSample, name, library string) error {
	for _, key := range []string{library, "monitored", "upcoming"} {
		count, ok := sample.values[key].(float64)
		if sample.units[key] != "count" || !ok || count < 0 || math.IsNaN(count) || math.IsInf(count, 0) || count != math.Trunc(count) {
			return errors.New("Homarr returned invalid " + name + " count statistics; retry self-host setup homarr")
		}
	}
	if sample.values["monitored"].(float64) > sample.values[library].(float64) {
		return errors.New("Homarr returned inconsistent " + name + " " + strings.TrimSuffix(library, "s") + " statistics; retry self-host setup homarr")
	}
	return nil
}
