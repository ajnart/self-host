package homarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/status"
)

type tandoorHoldSecrets struct {
	values map[string]string
	writes int
}

func (s *tandoorHoldSecrets) Get(key string) string { return s.values[key] }
func (s *tandoorHoldSecrets) Set(key, value string) error {
	s.writes++
	s.values[key] = value
	return nil
}

func TestTandoorExactSavedTokenHoldPrecedesEveryMutationPath(t *testing.T) {
	for _, savedToken := range []string{"tda_local_key_A", "tda_same_owner_key_B"} {
		for _, address := range []string{"http://tandoor:80", "http://old-tandoor:8080"} {
			for _, matchingFingerprint := range []bool{true, false} {
				for _, owned := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s/%s/fingerprint=%t/owned=%t", savedToken, address, matchingFingerprint, owned), func(t *testing.T) {
						var calls atomic.Int32
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							json.NewEncoder(w).Encode(map[string]any{"id": "saved-id", "kind": "tandoor", "url": address, "token": savedToken, "accountId": 17, "scope": "write", "expires": "2035-01-01T00:00:00Z"})
						}))
						t.Cleanup(server.Close)
						fromSecrets := &tandoorHoldSecrets{values: map[string]string{"HOMARR_API_KEY": "fixture-key"}}
						toSecrets := &tandoorHoldSecrets{values: map[string]string{"TANDOOR_HOMARR_API_KEY": "tda_local_key_A", "TANDOOR_HOMARR_TOKEN_OWNERSHIP": "preserved-owner-and-Space-record"}}
						steps := []helpers.StepResult{}
						from := helpers.New(helpers.Config{App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Secrets: fromSecrets, Log: events.Discard(), Record: func(r helpers.StepResult) { steps = append(steps, r) }})
						to := helpers.New(helpers.Config{App: "tandoor", Service: "tandoor", ContainerPort: 80, BaseURL: server.URL, HTTP: server.Client(), Secrets: toSecrets, Log: events.Discard()})
						descriptor := tandoorTarget()
						if owned {
							fromSecrets.values["HOMARR_LINKED_TANDOOR"] = "saved-id"
						}
						fingerprint := integrationFingerprint(descriptor, from, to)
						if !matchingFingerprint {
							fingerprint = "different"
						}
						fromSecrets.values[integrationFingerprintKey(from, "saved-id")] = fingerprint
						beforeFrom, beforeTo := maps.Clone(fromSecrets.values), maps.Clone(toSecrets.values)
						for _, run := range []func() error{
							func() error { return descriptor.preflight(t.Context(), from, to) },
							func() error { _, err := descriptor.linked(t.Context(), from, to); return err },
							func() error { return descriptor.add(t.Context(), from, to) },
							func() error { return descriptor.link().Apply(t.Context(), from, to) },
							func() error { return descriptor.verifyData(t.Context(), from, to, "saved-id") },
						} {
							err := run()
							var manual *helpers.ManualError
							if !errors.As(err, &manual) || manual.Hint != tandoorNativeHint {
								t.Fatalf("native admission did not retain its fixed hold: %v", err)
							}
						}
						if calls.Load() != 0 || fromSecrets.writes != 0 || toSecrets.writes != 0 || !maps.Equal(beforeFrom, fromSecrets.values) || !maps.Equal(beforeTo, toSecrets.values) {
							t.Fatal("token/Address/fingerprint drift caused a request or secret mutation")
						}
						if len(steps) != 1 || steps[0].Status != status.StepManual || steps[0].Hint != tandoorNativeHint {
							t.Fatal("Step Check/Do did not retain Manual before mutation")
						}
					})
				}
			}
		}
	}
}

func TestTandoorCountPipelineIsolatedFromNativeAdmission(t *testing.T) {
	for _, advance := range []bool{true, false} {
		fixture := newStatsFixture()
		fixture.values = map[string]any{"users": float64(1), "recipes": float64(0), "keywords": float64(0)}
		fixture.metrics = []any{}
		for _, key := range []string{"users", "recipes", "keywords"} {
			fixture.metrics = append(fixture.metrics, map[string]any{"key": key, "unit": "count"})
		}
		fixture.advance = advance
		h := fixture.helpers(t)
		h.Now = func() time.Time { return time.UnixMilli(1000) }
		descriptor := tandoorTarget()
		semantic := target{widgets: descriptor.widgets, stats: descriptor.stats, verify: func(context.Context, *helpers.H, *helpers.H, string) error { return nil }}
		err := semantic.verifyData(t.Context(), h, h, "counts-only")
		if (err == nil) != advance || fixture.catalogs != 1 || fixture.snapshots != 1 || fixture.refreshes != 1 || !fixture.force {
			t.Fatalf("count pipeline did not use one advancing sample: %v", err)
		}
	}
}

func TestTandoorStatsSemanticsUseReviewedCountHelpers(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*statsSample)
		valid  bool
	}{
		{"zero recipes and keywords", func(*statsSample) {}, true},
		{"no users", func(s *statsSample) { s.values["users"] = float64(0) }, false},
		{"negative recipes", func(s *statsSample) { s.values["recipes"] = float64(-1) }, false},
		{"fractional keywords", func(s *statsSample) { s.values["keywords"] = 0.5 }, false},
		{"nonfinite", func(s *statsSample) { s.values["keywords"] = math.Inf(1) }, false},
		{"missing", func(s *statsSample) { delete(s.values, "recipes") }, false},
		{"wrong unit", func(s *statsSample) { s.units["users"] = "bytes" }, false},
		{"old timestamp", func(s *statsSample) { s.updatedAt = 1 }, false},
		{"future timestamp", func(s *statsSample) { s.updatedAt = s.completedAt + 60_001 }, false},
		{"deferred", func(s *statsSample) { s.retryAt = float64(100) }, false},
		{"malformed retry", func(s *statsSample) { s.retryAt = "0" }, false},
		{"zero retry", func(s *statsSample) { s.retryAt = float64(0) }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sample := statsSample{values: map[string]any{"users": float64(1), "recipes": float64(0), "keywords": float64(0)}, units: map[string]string{"users": "count", "recipes": "count", "keywords": "count"}, startedAt: 100_000, completedAt: 100_010, updatedAt: 100_005}
			test.change(&sample)
			if err := verifyTandoorStats(sample); (err == nil) != test.valid {
				t.Fatalf("unexpected semantics: %v", err)
			}
		})
	}
}
