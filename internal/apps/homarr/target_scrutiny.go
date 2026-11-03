package homarr

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const scrutinyMaxSampleAge = 48 * time.Hour
const scrutinyFutureTolerance = 5 * time.Minute
const scrutinyNilUUID = "00000000-0000-0000-0000-000000000000"

const scrutinyRecovery = "Scrutiny's existing web/collector data is not verified. Preserve this connection, then check that the approved endpoint is reachable without authentication or a redirect, that every non-archived device has a genuine stored SMART sample from an existing collector no older than 48 hours, and that its device-status threshold is 1, 2 or 3. Retry self-host setup homarr afterwards. Do not mount devices, grant privileges, trigger SMART tests, install collectors, disable authentication or bypass TLS; a missing sample is not a passed disk."
const scrutinyStatsRecovery = "Scrutiny Stats require three measured non-negative whole device counts (passed, failed, unknown), count units, a positive total and one successful genuinely advancing refresh. Preserve this connection and retry Homarr Setup; do not invent counts or use cached/no-op data as acceptance."

type scrutinyEnvelope struct {
	Success bool `json:"success"`
}

type scrutinySettings struct {
	scrutinyEnvelope
	Settings struct {
		Metrics struct {
			StatusThreshold *int `json:"status_threshold"`
		} `json:"metrics"`
	} `json:"settings"`
}

type scrutinySummary struct {
	scrutinyEnvelope
	Data struct {
		Summary map[string]struct {
			Device struct {
				UUID         string `json:"scrutiny_uuid"`
				WWN          string `json:"wwn"`
				Archived     bool   `json:"archived"`
				DeviceStatus *int   `json:"device_status"`
			} `json:"device"`
			Smart *struct {
				CollectorDate string `json:"collector_date"`
			} `json:"smart"`
		} `json:"summary"`
	} `json:"data"`
}

func scrutinyExternalTarget(c helpers.ExternalConnection) (target, error) {
	if c.Kind != "scrutiny" {
		return target{}, errors.New("the Scrutiny external adapter requires the exact scrutiny kind")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || strings.ContainsAny(c.Endpoint, "\r\n\t\\ #") {
		return target{}, errors.New("Scrutiny requires an approved HTTP(S) endpoint without embedded credentials, query or fragment")
	}
	if len(c.Credentials) != 0 || len(c.Selection) != 0 {
		return target{}, errors.New("Scrutiny takes no credentials or selection; its endpoint must be reachable without authentication")
	}
	return target{
		name: "Scrutiny", kind: "scrutiny",
		widgets: []widget{statWidget("passed", "failed", "unknown")},
		preflight: func(ctx context.Context, from, _ *helpers.H) error {
			return scrutinyPreflight(ctx, from, c.Endpoint)
		},
		stats: scrutinyValidateStats,
	}, nil
}

func scrutinyGet(ctx context.Context, h *helpers.H, endpoint, path string, out any) error {
	resp, err := h.HTTP(ctx, helpers.Request{BaseURL: endpoint, Path: path, NoRedirect: true})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil || resp.Status != http.StatusOK || resp.Decode(out) != nil {
		return &helpers.ManualError{Hint: scrutinyRecovery}
	}
	return nil
}

func scrutinyPreflight(ctx context.Context, h *helpers.H, endpoint string) error {
	var settings scrutinySettings
	if err := scrutinyGet(ctx, h, endpoint, "/api/settings", &settings); err != nil {
		return err
	}
	var summary scrutinySummary
	if err := scrutinyGet(ctx, h, endpoint, "/api/summary", &summary); err != nil {
		return err
	}
	_, err := scrutinyCounts(settings, summary, h.Now())
	return err
}

type scrutinyTally struct{ passed, failed, unknown int }

func scrutinyCounts(settings scrutinySettings, summary scrutinySummary, now time.Time) (scrutinyTally, error) {
	invalid := func() (scrutinyTally, error) { return scrutinyTally{}, &helpers.ManualError{Hint: scrutinyRecovery} }
	threshold := settings.Settings.Metrics.StatusThreshold
	if !settings.Success || !summary.Success || threshold == nil || *threshold < 1 || *threshold > 3 {
		return invalid()
	}
	var tally scrutinyTally
	identities := map[string]bool{}
	for key, row := range summary.Data.Summary {
		if row.Device.Archived {
			continue
		}
		identity := row.Device.UUID
		if identity == scrutinyNilUUID {
			identity = ""
		}
		if identity == "" {
			identity = row.Device.WWN
		}
		if key == "" || identity != key || identities[identity] || row.Device.DeviceStatus == nil || row.Smart == nil {
			return invalid()
		}
		identities[identity] = true
		collected, err := time.Parse(time.RFC3339, row.Smart.CollectorDate)
		if err != nil || collected.IsZero() || collected.Before(now.Add(-scrutinyMaxSampleAge)) || collected.After(now.Add(scrutinyFutureTolerance)) {
			return invalid()
		}
		status := *row.Device.DeviceStatus
		switch {
		case status < 0 || status > 3:
			tally.unknown++
		case status == 0:
			tally.passed++
		case *threshold == 3 && status >= 1, *threshold < 3 && (status == *threshold || status == 3):
			tally.failed++
		default:
			tally.passed++
		}
	}
	if len(identities) == 0 {
		return invalid()
	}
	return tally, nil
}

func scrutinyValidateStats(sample statsSample) error {
	invalid := &helpers.ManualError{Hint: scrutinyStatsRecovery}
	total := 0.0
	for _, key := range []string{"passed", "failed", "unknown"} {
		count, ok := measuredNumber(sample.values[key], true)
		if !ok || sample.units[key] != "count" {
			return invalid
		}
		total += count
	}
	retry, retryOK := sample.retryAt.(float64)
	if total == 0 || !retryOK || retry != 0 || math.IsNaN(total) || !validStatsTimestamp(sample.updatedAt) || sample.updatedAt > sample.completedAt+5000 || sample.updatedAt < sample.startedAt-60000 {
		return invalid
	}
	return nil
}
