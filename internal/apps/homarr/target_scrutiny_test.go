package homarr

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/systemtest"
)

func scrutinyTestConnection() helpers.ExternalConnection {
	return helpers.ExternalConnection{ID: "0123456789abcdef0123456789abcdef", Kind: "scrutiny", Endpoint: "http://private-scrutiny.example.invalid:8080/base"}
}

func TestScrutinyExternalInputRequiresTheNoSecretContract(t *testing.T) {
	c := scrutinyTestConnection()
	got, err := externalConnectionDescriptor(c)
	if err != nil || got.kind != "scrutiny" || got.id != "external-"+c.ID || !got.integrationOnly || len(got.credentials(nil, nil)) != 0 || got.internal(nil) != c.Endpoint || len(got.widgets) != 1 || len(got.widgets[0].metrics) != 3 {
		t.Fatalf("descriptor: %v", err)
	}
	for _, change := range []func(*helpers.ExternalConnection){
		func(c *helpers.ExternalConnection) { c.Endpoint = "ftp://private-scrutiny.example.invalid" },
		func(c *helpers.ExternalConnection) {
			c.Endpoint = "http://user:private@private-scrutiny.example.invalid"
		},
		func(c *helpers.ExternalConnection) { c.Endpoint += "?token=private" },
		func(c *helpers.ExternalConnection) { c.Endpoint += "#" },
		func(c *helpers.ExternalConnection) {
			c.Credentials = []helpers.ExternalCredential{{Kind: "apiKey", Value: "private"}}
		},
		func(c *helpers.ExternalConnection) { c.Selection = map[string]string{"node": "private"} },
	} {
		input := scrutinyTestConnection()
		change(&input)
		if err := (App{}).ValidateExternalConnection(input); err == nil {
			t.Fatal("invalid Scrutiny connection was accepted")
		}
	}
	encoded, _ := json.Marshal(c)
	if strings.Contains(string(encoded), "private-scrutiny") {
		t.Fatal("Scrutiny private input escaped the connection DTO")
	}
}

func scrutinyFixture(now time.Time, threshold int, statuses ...int) (scrutinySettings, scrutinySummary) {
	var settings scrutinySettings
	settings.Success = true
	settings.Settings.Metrics.StatusThreshold = &threshold
	var summary scrutinySummary
	summary.Success = true
	summary.Data.Summary = map[string]struct {
		Device struct {
			UUID         string `json:"scrutiny_uuid"`
			WWN          string `json:"wwn"`
			Archived     bool   `json:"archived"`
			DeviceStatus *int   `json:"device_status"`
		} `json:"device"`
		Smart *struct {
			CollectorDate string `json:"collector_date"`
		} `json:"smart"`
	}{}
	for i, status := range statuses {
		s := status
		key := "wwn-" + string(rune('a'+i))
		row := summary.Data.Summary[key]
		row.Device.WWN = key
		row.Device.DeviceStatus = &s
		row.Smart = &struct {
			CollectorDate string `json:"collector_date"`
		}{CollectorDate: now.Add(-time.Hour).Format(time.RFC3339)}
		summary.Data.Summary[key] = row
	}
	return settings, summary
}

func TestScrutinyCountsFollowThresholdAndIdentity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		threshold int
		statuses  []int
		want      scrutinyTally
	}{
		{3, []int{0, 1, 2, 3, 7, -1}, scrutinyTally{1, 3, 2}},
		{1, []int{0, 1, 2, 3}, scrutinyTally{2, 2, 0}},
		{2, []int{0, 1, 2, 3}, scrutinyTally{2, 2, 0}},
	} {
		settings, summary := scrutinyFixture(now, tc.threshold, tc.statuses...)
		got, err := scrutinyCounts(settings, summary, now)
		if err != nil || got != tc.want {
			t.Fatalf("threshold %d: %+v %v", tc.threshold, got, err)
		}
	}
	settings, summary := scrutinyFixture(now, 3, 0, 0)
	for key, row := range summary.Data.Summary {
		if key == "wwn-b" {
			row.Device.Archived = true
			row.Smart = nil
			summary.Data.Summary[key] = row
		}
	}
	if got, err := scrutinyCounts(settings, summary, now); err != nil || got.passed != 1 {
		t.Fatal("archived device without a sample was not excluded")
	}
	for name, mutate := range map[string]func(*scrutinySettings, *scrutinySummary){
		"unsuccessful": func(s *scrutinySettings, _ *scrutinySummary) { s.Success = false },
		"threshold":    func(s *scrutinySettings, _ *scrutinySummary) { v := 4; s.Settings.Metrics.StatusThreshold = &v },
		"empty":        func(_ *scrutinySettings, m *scrutinySummary) { clear(m.Data.Summary) },
		"unsampled": func(_ *scrutinySettings, m *scrutinySummary) {
			row := m.Data.Summary["wwn-a"]
			row.Smart = nil
			m.Data.Summary["wwn-a"] = row
		},
		"stale": func(_ *scrutinySettings, m *scrutinySummary) {
			row := m.Data.Summary["wwn-a"]
			row.Smart.CollectorDate = now.Add(-49 * time.Hour).Format(time.RFC3339)
			m.Data.Summary["wwn-a"] = row
		},
		"future": func(_ *scrutinySettings, m *scrutinySummary) {
			row := m.Data.Summary["wwn-a"]
			row.Smart.CollectorDate = now.Add(time.Hour).Format(time.RFC3339)
			m.Data.Summary["wwn-a"] = row
		},
		"identity": func(_ *scrutinySettings, m *scrutinySummary) {
			row := m.Data.Summary["wwn-a"]
			row.Device.WWN = "other"
			m.Data.Summary["wwn-a"] = row
		},
		"missing status": func(_ *scrutinySettings, m *scrutinySummary) {
			row := m.Data.Summary["wwn-a"]
			row.Device.DeviceStatus = nil
			m.Data.Summary["wwn-a"] = row
		},
	} {
		settings, summary := scrutinyFixture(now, 3, 0, 0)
		mutate(&settings, &summary)
		if _, err := scrutinyCounts(settings, summary, now); !errors.Is(err, helpers.ErrManual) {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestScrutinyPreflightReadsOnlyTheEndpointAndRejectsRedirects(t *testing.T) {
	now := systemtest.NewDeps(t).Now()
	var paths []string
	redirect := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if redirect {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		switch r.URL.Path {
		case "/base/api/settings":
			w.Write([]byte(`{"success":true,"settings":{"metrics":{"status_threshold":3}}}`))
		case "/base/api/summary":
			w.Write([]byte(`{"success":true,"data":{"summary":{"w1":{"device":{"wwn":"w1","device_status":0},"smart":{"collector_date":"` + now.Add(-time.Minute).Format(time.RFC3339) + `"}}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	deps := systemtest.NewDeps(t)
	h := helpers.New(helpers.Config{App: "homarr", Service: "homarr", BaseURL: srv.URL, HTTP: srv.Client(), Log: events.Discard(), Now: deps.Now, Sleep: deps.Sleep})
	if err := scrutinyPreflight(t.Context(), h, srv.URL+"/base"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "GET /base/api/") {
			t.Fatalf("unexpected request %s", path)
		}
	}
	redirect = true
	if err := scrutinyPreflight(t.Context(), h, srv.URL+"/base"); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("redirecting endpoint was followed or accepted")
	}
	srv.Close()
	if err := scrutinyPreflight(t.Context(), h, srv.URL+"/base"); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("unreachable endpoint was not Manual")
	}
}

func TestScrutinyStatsRetainCountUnitsClockAndRetrySemantics(t *testing.T) {
	valid := func() statsSample {
		return statsSample{
			values:  map[string]any{"passed": float64(2), "failed": float64(0), "unknown": float64(0)},
			units:   map[string]string{"passed": "count", "failed": "count", "unknown": "count"},
			retryAt: float64(0), startedAt: 10000, completedAt: 10010, updatedAt: 10005,
		}
	}
	if err := scrutinyValidateStats(valid()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*statsSample){
		func(s *statsSample) { s.values["passed"] = float64(0) },
		func(s *statsSample) { s.values["failed"] = float64(-1) },
		func(s *statsSample) { s.values["unknown"] = float64(1.5) },
		func(s *statsSample) { s.values["passed"] = nil },
		func(s *statsSample) { delete(s.values, "failed") },
		func(s *statsSample) { s.values["passed"] = math.NaN() },
		func(s *statsSample) { s.units["failed"] = "percent" },
		func(s *statsSample) { s.retryAt = nil },
		func(s *statsSample) { s.updatedAt = 20000 },
	} {
		sample := valid()
		change(&sample)
		if err := scrutinyValidateStats(sample); !errors.Is(err, helpers.ErrManual) {
			t.Fatal("invalid Scrutiny Stats semantics were accepted")
		}
	}
}
