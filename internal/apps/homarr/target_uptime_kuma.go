package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/apps/uptimekuma"
	"github.com/ajnart/self-host/internal/helpers"
)

func uptimeKumaTarget() target {
	descriptor := uptimeKumaTargetForFamily(uptimeKumaStatusPages)
	descriptor.prepare = prepareUptimeKuma
	return descriptor
}

func uptimeKumaTargetForFamily(family uptimeKumaNativeFamily) target {
	options := map[string]any{
		"showAverageUptime": true, "showUptimeRing": true, "showTotalMonitors": true,
		"showUpCount": true, "showDownCount": true, "showPausedCount": true,
	}
	if family == uptimeKumaStatusPages {
		options = map[string]any{
			"summaryOnly": false, "showSummary": true, "showIncidents": true,
			"groupByStatusPageGroup": true, "showOnlyProblems": false, "sortBy": "status",
			"showHeartbeatBar": true, "showUptime": true, "showResponseTime": true, "showCertificateExpiry": true,
		}
	}
	descriptor := target{
		id: "uptime-kuma", name: "Uptime Kuma", kind: "uptimeKuma", internal: fixed("http://uptime-kuma:3001"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "slug", Value: uptimekuma.HomarrSlug(to)}}
		},
		widgets: []widget{{kind: "uptimeKuma", options: options, managedOptions: []string{
			"showAverageUptime", "showUptimeRing", "showTotalMonitors", "showUpCount", "showDownCount", "showPausedCount",
			"statusPageSlug", "summaryOnly", "showSummary", "showIncidents", "groupByStatusPageGroup", "showOnlyProblems",
			"sortBy", "showHeartbeatBar", "showUptime", "showResponseTime", "showCertificateExpiry",
		}}, statWidget("totalMonitors", "upCount", "downCount", "averageUptimePercent")},
		verify: func(ctx context.Context, from, to *helpers.H, integrationID string) error {
			return verifyUptimeKuma(ctx, from, to, integrationID, family)
		},
		stats: verifyUptimeKumaStats,
	}
	if family == uptimeKumaStatusPages {
		descriptor.nullableStats = []string{"averageUptimePercent"}
		descriptor.stats = func(sample statsSample) error { return uptimeKumaStats(sample, true) }
	}
	return descriptor
}

func prepareUptimeKuma(ctx context.Context, from, to *helpers.H) (target, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := uptimekuma.HomarrMonitors(ctx, to); err != nil {
		return target{}, &helpers.ManualError{Hint: "Run self-host setup uptime-kuma to verify its explicitly selected existing public status page, then retry self-host setup homarr. Preserve the page, monitors and Access; do not publish monitors or add a metrics key."}
	}
	if err := validateOwnership(from); err != nil {
		return target{}, err
	}
	family, err := uptimeKumaCapability(ctx, from, uptimekuma.HomarrSlug(to))
	if err != nil {
		return target{}, err
	}
	descriptor := uptimeKumaTargetForFamily(family)
	if family == uptimeKumaStatusPages {
		descriptor.widgets[0].options["statusPageSlug"] = uptimekuma.HomarrSlug(to)
	}
	if err := descriptor.compatible(ctx, from, to); err != nil {
		return target{}, uptimeKumaCapabilityManual()
	}
	id := from.Secret(from.Key("linked_uptime-kuma"))
	if id == "" || from.Secret(from.Key("widgets_"+id)) == "" || from.Secret(integrationFingerprintKey(from, id)) != integrationFingerprint(descriptor, from, to) {
		return target{}, &helpers.ManualError{Hint: "Uptime Kuma native verification needs a recorded owned Homarr integration with its matching selected-page credential and explicit widget ownership before any change. A first install or missing ownership cannot safely prove the populated native contract read-only. Preserve all resources and request a reviewed adapter or ownership recovery before retrying self-host setup homarr; do not adopt a user integration or create a duplicate."}
	}
	integrations, err := listIntegrations(ctx, from)
	if err != nil {
		return target{}, uptimeKumaCapabilityManual()
	}
	matches := 0
	for _, integration := range integrations {
		if stringField(integration, "id") == id {
			if stringField(integration, "kind") != descriptor.kind || stringField(integration, "url") != descriptor.internal(to) {
				return target{}, uptimeKumaCapabilityManual()
			}
			matches++
		}
	}
	if matches != 1 {
		return target{}, uptimeKumaCapabilityManual()
	}
	if err := descriptor.verify(ctx, from, to, id); err != nil {
		return target{}, &helpers.ManualError{Hint: "The recorded Uptime Kuma integration did not verify the explicitly selected page and native monitor contract before mutation. Preserve its integration and widgets, review the selected page and resolved Homarr image contract, then retry self-host setup homarr. Do not publish monitors, replace credentials or fall back to another native API after a data failure."}
	}
	current, err := board(ctx, from)
	if err != nil {
		return target{}, uptimeKumaCapabilityManual()
	}
	if _, err := descriptor.reconcileOwnedOptions(ctx, from, current, id, false); err != nil {
		return target{}, err
	}
	return descriptor, nil
}

func verifyUptimeKuma(ctx context.Context, from, to *helpers.H, integrationID string, family uptimeKumaNativeFamily) error {
	monitors, err := uptimekuma.HomarrMonitors(ctx, to)
	if err != nil {
		return &helpers.ManualError{Hint: "Run self-host setup uptime-kuma to verify the explicitly selected existing public status page, then retry self-host setup homarr. Preserve its monitors and Access; do not publish a page or add a metrics key."}
	}
	input := map[string]any{"integrationIds": []string{integrationID}}
	if family == uptimeKumaStatusPages {
		input["statusPageSlug"] = uptimekuma.HomarrSlug(to)
	}
	value, err := apiKeyProcedure(ctx, from, family.procedure(), input)
	if err != nil {
		return errors.New("Homarr could not verify the selected Uptime Kuma native data; review its authentication and resolved API contract, then retry Setup")
	}
	if family == uptimeKumaStatusPages {
		return verifyUptimeKumaStatusPage(from, to, integrationID, value, monitors)
	}
	return verifyUptimeKumaDashboard(from, integrationID, value, monitors)
}

func verifyUptimeKumaDashboard(from *helpers.H, integrationID string, value any, monitors []uptimekuma.HomarrMonitor) error {
	var results []struct {
		IntegrationID  string `json:"integrationId"`
		IntegrationURL string `json:"integrationUrl"`
		Error          any    `json:"error"`
		Dashboard      *struct {
			Total    *int64   `json:"totalMonitors"`
			Up       *int64   `json:"upCount"`
			Down     *int64   `json:"downCount"`
			Paused   *int64   `json:"pausedCount"`
			Average  *float64 `json:"averageUptimePercent"`
			Monitors *[]struct {
				ID     *int64          `json:"id"`
				Name   *string         `json:"name"`
				Status *string         `json:"status"`
				Uptime json.RawMessage `json:"uptimePercent24h"`
			} `json:"monitors"`
		} `json:"dashboard"`
	}
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &results) != nil || len(results) != 1 || results[0].IntegrationID != integrationID || results[0].IntegrationURL != "http://uptime-kuma:3001" || results[0].Error != nil {
		return errors.New("Homarr did not return the owned Uptime Kuma dashboard; retry self-host setup homarr")
	}
	dashboard := results[0].Dashboard
	if dashboard == nil || dashboard.Total == nil || dashboard.Up == nil || dashboard.Down == nil || dashboard.Paused == nil || dashboard.Average == nil || dashboard.Monitors == nil || *dashboard.Total != int64(len(monitors)) || len(*dashboard.Monitors) != len(monitors) {
		return errors.New("Homarr returned incomplete Uptime Kuma statistics; retry self-host setup homarr")
	}
	counts := map[string]int64{}
	remaining := map[int64]int{}
	names := map[int64]string{}
	for _, monitor := range monitors {
		if name, found := names[monitor.ID]; found && name != monitor.Name {
			return errors.New("Uptime Kuma returned conflicting selected monitor identities; review the selected page before retrying Setup")
		}
		names[monitor.ID] = monitor.Name
		remaining[monitor.ID]++
	}
	seen := map[int64]string{}
	uptimeSum := 0.0
	uptimeCount := 0.0
	for _, monitor := range *dashboard.Monitors {
		if monitor.ID == nil || monitor.Name == nil || monitor.Status == nil || !slices.Contains([]string{"up", "down", "paused"}, *monitor.Status) || len(monitor.Uptime) == 0 {
			return errors.New("Homarr returned invalid Uptime Kuma monitor statuses")
		}
		if remaining[*monitor.ID] == 0 || !slices.ContainsFunc(monitors, func(expected uptimekuma.HomarrMonitor) bool {
			return expected.ID == *monitor.ID && expected.Name == *monitor.Name
		}) {
			return errors.New("Homarr did not return the explicitly selected Uptime Kuma monitors; retry Setup")
		}
		var uptime *float64
		if json.Unmarshal(monitor.Uptime, &uptime) != nil || uptime != nil && (*uptime < 0 || *uptime > 100) {
			return errors.New("Homarr returned an invalid Uptime Kuma uptime percentage")
		}
		if uptime != nil {
			uptimeSum += *uptime
			uptimeCount++
		}
		encoded, err := json.Marshal(monitor)
		if err != nil {
			return errors.New("Homarr returned malformed Uptime Kuma monitor data")
		}
		if previous, found := seen[*monitor.ID]; found && previous != string(encoded) {
			return errors.New("Homarr returned conflicting duplicate Uptime Kuma monitors; review the selected page before retrying Setup")
		}
		seen[*monitor.ID] = string(encoded)
		remaining[*monitor.ID]--
		counts[*monitor.Status]++
	}
	if *dashboard.Up != counts["up"] || *dashboard.Down != counts["down"] || *dashboard.Paused != counts["paused"] || math.Abs(*dashboard.Average-uptimeSum/math.Max(uptimeCount, 1)) > 0.000001 {
		return errors.New("Homarr returned inconsistent Uptime Kuma status counts or uptime")
	}
	if len(monitors) == 0 {
		from.Info("The selected Uptime Kuma status page is valid but empty; no monitor health or rendered widget result has been verified.")
	}
	return nil
}

type uptimeKumaNativeHeartbeat struct {
	Status *string         `json:"status"`
	Time   *float64        `json:"time"`
	Ping   json.RawMessage `json:"ping"`
}

type uptimeKumaNativeMonitor struct {
	ID          *int64                        `json:"id"`
	Name        *string                       `json:"name"`
	Status      *string                       `json:"status"`
	Uptime      json.RawMessage               `json:"uptime24h"`
	LastCheck   json.RawMessage               `json:"lastCheckAt"`
	StatusSince json.RawMessage               `json:"statusSince"`
	Ping        json.RawMessage               `json:"ping"`
	Certificate json.RawMessage               `json:"certificateDaysRemaining"`
	Type        json.RawMessage               `json:"type"`
	URL         json.RawMessage               `json:"url"`
	Tags        *[]map[string]json.RawMessage `json:"tags"`
	Heartbeats  *[]uptimeKumaNativeHeartbeat  `json:"heartbeats"`
}

func verifyUptimeKumaStatusPage(from, to *helpers.H, integrationID string, value any, expected []uptimekuma.HomarrMonitor) error {
	var results []struct {
		IntegrationID  string `json:"integrationId"`
		IntegrationURL string `json:"integrationUrl"`
		Error          any    `json:"error"`
		StatusPage     *struct {
			Title  *string `json:"title"`
			URL    *string `json:"url"`
			Groups *[]struct {
				ID       *int64                     `json:"id"`
				Name     *string                    `json:"name"`
				Monitors *[]uptimeKumaNativeMonitor `json:"monitors"`
			} `json:"groups"`
			Incidents    *[]json.RawMessage `json:"incidents"`
			Maintenances *[]json.RawMessage `json:"maintenances"`
		} `json:"statusPage"`
	}
	invalid := errors.New("Homarr did not return the explicitly selected Uptime Kuma page and complete native monitor data; review the page and resolved API contract, then retry Setup")
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &results) != nil || len(results) != 1 || results[0].IntegrationID != integrationID || results[0].IntegrationURL != "http://uptime-kuma:3001" || results[0].Error != nil {
		return invalid
	}
	page := results[0].StatusPage
	if page == nil || page.Title == nil || strings.TrimSpace(*page.Title) == "" || page.URL == nil || page.Groups == nil || page.Incidents == nil || page.Maintenances == nil {
		return invalid
	}
	address, err := url.Parse(*page.URL)
	if err != nil || address.Host == "" || address.User != nil || !slices.Contains([]string{"http", "https"}, address.Scheme) || address.EscapedPath() != "/status/"+url.PathEscape(uptimekuma.HomarrSlug(to)) || address.RawQuery != "" || address.Fragment != "" {
		return invalid
	}
	remaining := map[int64]int{}
	names := map[int64]string{}
	for _, monitor := range expected {
		if name, found := names[monitor.ID]; found && name != monitor.Name {
			return invalid
		}
		names[monitor.ID] = monitor.Name
		remaining[monitor.ID]++
	}
	seen := map[int64]uptimeKumaNativeMonitor{}
	groups := map[int64]bool{}
	for _, group := range *page.Groups {
		if group.ID == nil || *group.ID <= 0 || groups[*group.ID] || group.Name == nil || group.Monitors == nil {
			return invalid
		}
		groups[*group.ID] = true
		for _, monitor := range *group.Monitors {
			if monitor.ID == nil || monitor.Name == nil || remaining[*monitor.ID] == 0 || names[*monitor.ID] != *monitor.Name || !uptimeKumaValidMonitor(monitor) {
				return invalid
			}
			if previous, found := seen[*monitor.ID]; found && !reflect.DeepEqual(previous, monitor) {
				return invalid
			}
			seen[*monitor.ID] = monitor
			remaining[*monitor.ID]--
		}
	}
	for _, count := range remaining {
		if count != 0 {
			return invalid
		}
	}
	if len(expected) == 0 {
		from.Info("The selected Uptime Kuma status page is valid but empty; no monitor health or rendered widget result has been verified.")
	}
	return nil
}

func uptimeKumaValidMonitor(monitor uptimeKumaNativeMonitor) bool {
	if monitor.Status == nil || !slices.Contains([]string{"up", "down", "pending", "maintenance", "stale", "unknown"}, *monitor.Status) || monitor.Tags == nil || monitor.Heartbeats == nil || len(*monitor.Heartbeats) > 50 {
		return false
	}
	uptime, hasUptime, valid := uptimeKumaNullableNumber(monitor.Uptime)
	if !valid || hasUptime && (uptime < 0 || uptime > 100) {
		return false
	}
	lastCheck, hasCheck, valid := uptimeKumaNullableNumber(monitor.LastCheck)
	if !valid || hasCheck && (lastCheck <= 0 || math.Trunc(lastCheck) != lastCheck) || hasUptime && !hasCheck {
		return false
	}
	since, hasSince, valid := uptimeKumaNullableNumber(monitor.StatusSince)
	if !valid || hasSince && (since <= 0 || math.Trunc(since) != since || !hasCheck || since > lastCheck) {
		return false
	}
	for _, field := range []json.RawMessage{monitor.Ping, monitor.Certificate} {
		if _, _, valid := uptimeKumaNullableNumber(field); !valid {
			return false
		}
	}
	for _, field := range []json.RawMessage{monitor.Type, monitor.URL} {
		if !uptimeKumaNullableString(field) {
			return false
		}
	}
	for _, tag := range *monitor.Tags {
		var name string
		if len(tag["name"]) == 0 || string(tag["name"]) == "null" || json.Unmarshal(tag["name"], &name) != nil || !uptimeKumaNullableString(tag["color"]) || !uptimeKumaNullableString(tag["value"]) {
			return false
		}
	}
	beats := *monitor.Heartbeats
	if len(beats) == 0 {
		return *monitor.Status == "unknown" && !hasCheck && !hasSince && !hasUptime
	}
	previous := 0.0
	for _, beat := range beats {
		if beat.Status == nil || !slices.Contains([]string{"up", "down", "pending", "maintenance", "unknown"}, *beat.Status) || beat.Time == nil || *beat.Time <= 0 || math.IsNaN(*beat.Time) || math.IsInf(*beat.Time, 0) || math.Trunc(*beat.Time) != *beat.Time || *beat.Time < previous {
			return false
		}
		if _, _, valid := uptimeKumaNullableNumber(beat.Ping); !valid {
			return false
		}
		previous = *beat.Time
	}
	latest := beats[len(beats)-1]
	if !hasCheck || lastCheck != *latest.Time || !reflect.DeepEqual(monitor.Ping, latest.Ping) {
		return false
	}
	if *monitor.Status == "stale" {
		return !hasSince
	}
	if *monitor.Status != *latest.Status {
		return false
	}
	for index := len(beats) - 2; index >= 0; index-- {
		if *beats[index].Status != *latest.Status {
			return hasSince && since == *beats[index+1].Time
		}
	}
	return !hasSince
}

func uptimeKumaNullableNumber(raw json.RawMessage) (float64, bool, bool) {
	if len(raw) == 0 {
		return 0, false, false
	}
	if string(raw) == "null" {
		return 0, false, true
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false, false
	}
	return value, true, true
}

func uptimeKumaNullableString(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value *string
	return json.Unmarshal(raw, &value) == nil
}

func verifyUptimeKumaStats(sample statsSample) error {
	return uptimeKumaStats(sample, false)
}

func uptimeKumaStats(sample statsSample, nullableAverage bool) error {
	for _, key := range []string{"totalMonitors", "upCount", "downCount"} {
		count, ok := sample.values[key].(float64)
		if sample.units[key] != "count" || !ok || math.IsNaN(count) || math.IsInf(count, 0) || count < 0 || math.Trunc(count) != count {
			return errors.New("Homarr returned invalid Uptime Kuma Stats counts or units; retry Setup")
		}
	}
	value, present := sample.values["averageUptimePercent"]
	average, numeric := value.(float64)
	if sample.units["averageUptimePercent"] != "percent" || !present || !(nullableAverage && value == nil) && (!numeric || math.IsNaN(average) || math.IsInf(average, 0) || average < 0 || average > 100) {
		return errors.New("Homarr returned an invalid Uptime Kuma Stats uptime percentage or unit; retry Setup")
	}
	total := sample.values["totalMonitors"].(float64)
	if sample.values["upCount"].(float64)+sample.values["downCount"].(float64) > total || total == 0 && (nullableAverage && value != nil || !nullableAverage && average != 0) {
		return errors.New("Homarr returned inconsistent Uptime Kuma Stats counts or uptime; retry Setup")
	}
	return nil
}
