package homarr

import (
	"reflect"
	"testing"
	"time"
)

func TestUptimeKumaReviewPreservesOwnedDisplayPreferences(t *testing.T) {
	f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	preferences := map[string]any{
		"statusPageSlug": "old-selection", "summaryOnly": true, "showSummary": false, "showIncidents": false,
		"groupByStatusPageGroup": false, "showOnlyProblems": true, "sortBy": "name", "showHeartbeatBar": false,
		"showUptime": false, "showResponseTime": false, "showCertificateExpiry": false, "custom": "keep",
	}
	f.items[0].(map[string]any)["options"] = preferences
	if err := uptimeKumaTarget().add(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	options := f.items[0].(map[string]any)["options"].(map[string]any)
	for key, value := range preferences {
		if key == "statusPageSlug" {
			value = "chosen-page"
		}
		if !reflect.DeepEqual(options[key], value) {
			t.Errorf("owned display preference %s changed from %v to %v", key, value, options[key])
		}
	}
}

func TestUptimeKumaReviewRequiresExactSourceGroups(t *testing.T) {
	for _, scenario := range []string{"moved monitor", "changed group id", "changed group name", "extra empty group", "missing empty group"} {
		t.Run(scenario, func(t *testing.T) {
			f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
			page := f.results[0].(map[string]any)["statusPage"].(map[string]any)
			groups := page["groups"].([]any)
			switch scenario {
			case "moved monitor":
				f.pageGroups = []any{
					map[string]any{"id": 7, "name": "Chosen group", "monitorList": f.pageMonitors[:1]},
					map[string]any{"id": 8, "name": "Second group", "monitorList": f.pageMonitors[1:]},
				}
				page["groups"] = append(groups, map[string]any{"id": 8, "name": "Second group", "monitors": []any{}})
			case "changed group id":
				groups[0].(map[string]any)["id"] = 8
			case "changed group name":
				groups[0].(map[string]any)["name"] = "Other source group"
			case "extra empty group":
				page["groups"] = append(groups, map[string]any{"id": 8, "name": "Extra", "monitors": []any{}})
			case "missing empty group":
				f.pageGroups = []any{map[string]any{"id": 7, "name": "Chosen group", "monitorList": f.pageMonitors}, map[string]any{"id": 8, "name": "Empty", "monitorList": []any{}}}
			}
			if _, err := uptimeKumaTarget().prepare(t.Context(), from, to); err == nil || f.mutations != 0 {
				t.Fatalf("source group mismatch admitted: err=%v writes=%d", err, f.mutations)
			}
		})
	}
}

func TestUptimeKumaReviewModernSourceUnknownKeepsLegacyStrict(t *testing.T) {
	for _, family := range []uptimeKumaNativeFamily{uptimeKumaStatusPages, uptimeKumaLegacyDashboard} {
		for _, scenario := range []string{"absent heartbeat", "expanded integer status"} {
			t.Run(family.procedure()+"/"+scenario, func(t *testing.T) {
				f, from, to := uptimeKumaNewContractFixture(t, family)
				beats := f.heartbeat["heartbeatList"].(map[string]any)
				if scenario == "absent heartbeat" {
					delete(beats, "2")
				} else {
					beats["2"] = []any{map[string]any{"status": 17, "time": "2026-11-02 11:59:00"}}
					if family == uptimeKumaStatusPages {
						monitor := uptimeKumaModernMonitors(f.results[0].(map[string]any)["statusPage"].(map[string]any))[1].(map[string]any)
						lastCheck := time.Date(2026, time.November, 2, 11, 59, 0, 0, time.UTC).UnixMilli()
						monitor["lastCheckAt"] = lastCheck
						monitor["heartbeats"] = []any{map[string]any{"status": "unknown", "time": lastCheck, "ping": nil}}
					}
				}
				_, err := uptimeKumaTarget().prepare(t.Context(), from, to)
				if (err == nil) != (family == uptimeKumaStatusPages) || f.mutations != 0 {
					t.Fatalf("modern unknown/legacy strictness: family=%v err=%v writes=%d", family, err, f.mutations)
				}
			})
		}
	}
}

func TestUptimeKumaReviewNative24HourUptimeBoundary(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		age    time.Duration
		uptime any
		valid  bool
	}{
		{"within window numeric", 24*time.Hour - time.Millisecond, 99, true},
		{"exact boundary numeric", 24 * time.Hour, 99, true},
		{"past boundary numeric", 24*time.Hour + time.Millisecond, 99, false},
		{"days old numeric", 72 * time.Hour, 99, false},
		{"days old null", 72 * time.Hour, nil, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
			monitor := uptimeKumaModernMonitors(f.results[0].(map[string]any)["statusPage"].(map[string]any))[0].(map[string]any)
			sampledAt := time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)
			lastCheck := sampledAt.Add(-scenario.age).UnixMilli()
			monitor["lastCheckAt"], monitor["uptime24h"], monitor["status"] = lastCheck, scenario.uptime, "stale"
			monitor["heartbeats"] = []any{map[string]any{"status": "up", "time": lastCheck - 60_000, "ping": 20}, map[string]any{"status": "up", "time": lastCheck, "ping": 10}}
			from.Now = func() time.Time { return sampledAt.Add(365 * 24 * time.Hour) }
			_, err := uptimeKumaTarget().prepare(t.Context(), from, to)
			if (err == nil) != scenario.valid || f.mutations != 0 {
				t.Fatalf("24h semantics valid=%v err=%v writes=%d", scenario.valid, err, f.mutations)
			}
		})
	}
}

func TestUptimeKumaReviewPreservesMatchingEmptyGroupIdentity(t *testing.T) {
	f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	f.pageGroups = []any{map[string]any{"id": 7, "name": "Chosen group", "monitorList": f.pageMonitors}, map[string]any{"id": 8, "name": "Empty", "monitorList": []any{}}}
	page := f.results[0].(map[string]any)["statusPage"].(map[string]any)
	page["groups"] = append(page["groups"].([]any), map[string]any{"id": 8, "name": "Empty", "monitors": []any{}})
	if _, err := uptimeKumaTarget().prepare(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
}

func TestUptimeKumaReviewGroupOrderAndExpectedCrossGroupDuplicate(t *testing.T) {
	f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	f.pageGroups = []any{map[string]any{"id": 7, "name": "Chosen group", "monitorList": f.pageMonitors}, map[string]any{"id": 8, "name": "Repeated", "monitorList": f.pageMonitors[:1]}}
	page := f.results[0].(map[string]any)["statusPage"].(map[string]any)
	page["groups"] = []any{map[string]any{"id": 8, "name": "Repeated", "monitors": uptimeKumaModernMonitors(page)[:1]}, page["groups"].([]any)[0]}
	if _, err := uptimeKumaTarget().prepare(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
}

func TestUptimeKumaReviewOnlyDefaultsMissingDisplayFields(t *testing.T) {
	f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
	f.items[0].(map[string]any)["options"] = map[string]any{"showUptime": false, "custom": "keep"}
	if err := uptimeKumaTarget().add(t.Context(), from, to); err != nil {
		t.Fatal(err)
	}
	options := f.items[0].(map[string]any)["options"].(map[string]any)
	if options["showUptime"] != false || options["showSummary"] != true || options["summaryOnly"] != false || options["sortBy"] != "status" || options["statusPageSlug"] != "chosen-page" || options["custom"] != "keep" {
		t.Fatalf("missing-only display defaults failed: %v", options)
	}
}

func TestUptimeKumaReviewInvalidDisplayChoiceRemainsManual(t *testing.T) {
	for _, options := range []map[string]any{{"showUptime": "false"}, {"sortBy": "unrecognized"}} {
		f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
		f.items[0].(map[string]any)["options"] = options
		if _, err := uptimeKumaTarget().prepare(t.Context(), from, to); err == nil || f.mutations != 0 {
			t.Fatalf("invalid manual display choice was reset: err=%v writes=%d", err, f.mutations)
		}
	}
}

func TestUptimeKumaReviewRequiresNativeSampleTimestamp(t *testing.T) {
	for _, timestamp := range []any{nil, "malformed", float64(1234)} {
		f, from, to := uptimeKumaNewContractFixture(t, uptimeKumaStatusPages)
		f.results[0].(map[string]any)["updatedAt"] = timestamp
		if _, err := uptimeKumaTarget().prepare(t.Context(), from, to); err == nil || f.mutations != 0 {
			t.Fatalf("invalid native sample timestamp was accepted: err=%v writes=%d", err, f.mutations)
		}
	}
}
