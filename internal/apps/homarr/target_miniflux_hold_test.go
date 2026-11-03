package homarr

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/miniflux"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

type minifluxRoundTripFunc func(*http.Request) (*http.Response, error)

func (f minifluxRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func minifluxAssertSafeNativeHold(t *testing.T, from *helpers.H, err, private error, canary string) {
	t.Helper()
	if err == nil {
		t.Fatal("native Miniflux acceptance lacked exact saved-key proof")
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), canary) {
			t.Fatal("untrusted native material entered the error chain")
		}
	}
	if private != nil && errors.Is(err, private) {
		t.Fatal("the private native cause was retained")
	}
	var manual *helpers.ManualError
	if !errors.Is(err, helpers.ErrManual) || !errors.As(err, &manual) || !strings.Contains(manual.Hint, "exact saved Miniflux key") {
		t.Fatal("missing actionable Miniflux-specific exact-key Manual boundary")
	}
	from.Log.Secret(canary)
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.Contains(cause.Error(), canary) {
			t.Fatal("later redaction registration cannot repair an already retained private cause")
		}
	}
}

func TestMinifluxNativeUntrustedErrorsCannotEscapeOrRetainCauses(t *testing.T) {
	const canary = "arbitrary-native-value-not-registered-for-redaction"
	for _, mode := range []string{"trpc message", "http body", "decode", "transport"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			private := errors.New(canary)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch mode {
				case "trpc message":
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"json": map[string]any{"message": canary, "code": "INTERNAL_SERVER_ERROR"}}})
				case "http body":
					w.WriteHeader(http.StatusServiceUnavailable)
					json.NewEncoder(w).Encode(map[string]string{"private": canary})
				default:
					w.Write([]byte(`{"result":{"data":{"json":"` + canary))
				}
			}))
			t.Cleanup(server.Close)
			var records []helpers.StepResult
			var emitted []events.Event
			from := helpers.New(helpers.Config{
				App: "homarr", BaseURL: server.URL, HTTP: server.Client(),
				Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key"},
				Log:     events.New(events.SinkFunc(func(event events.Event) { emitted = append(emitted, event) })),
				Record:  func(result helpers.StepResult) { records = append(records, result) },
			})
			if mode == "transport" {
				from.Config.HTTP = &http.Client{Transport: minifluxRoundTripFunc(func(*http.Request) (*http.Response, error) {
					requests++
					return nil, private
				})}
			}
			to := minifluxPeerFixture(t, "")
			err := minifluxTarget().verifyData(t.Context(), from, to, "miniflux-owned")
			minifluxAssertSafeNativeHold(t, from, err, private, canary)
			err = minifluxTarget().link().Apply(t.Context(), from, to)
			minifluxAssertSafeNativeHold(t, from, err, private, canary)
			raw, encodeErr := json.Marshal([]any{records, emitted})
			if encodeErr != nil || strings.Contains(string(raw), canary) || !strings.Contains(string(raw), "exact saved Miniflux key") || requests != 0 {
				t.Fatal("exact-key hold leaked native content or contacted Homarr before admission")
			}
		})
	}
}

func TestMinifluxSameAccountTokenSubstitutionStaysManualBeforeMutationOrStats(t *testing.T) {
	to := minifluxPeerFixture(t, "")
	if err := miniflux.VerifyHomarr(t.Context(), to); err != nil {
		t.Fatalf("local expected token A should remain valid: %v", err)
	}
	const savedToken = "miniflux-other-valid-key"
	response, err := to.HTTP(t.Context(), helpers.Request{Path: "/v1/me", Header: map[string]string{"X-Auth-Token": savedToken}})
	if err != nil || response.Status != http.StatusOK {
		t.Fatal("the substitute token B must also be valid")
	}
	var sameReader map[string]any
	if json.Unmarshal(response.Body, &sameReader) != nil || sameReader["id"] != float64(27) || sameReader["username"] != "admin" || sameReader["is_admin"] != true {
		t.Fatal("the substitution must preserve the actual account identity and authorization")
	}
	stats := newStatsFixture()
	stats.values = map[string]any{"read": 0, "unread": 0}
	stats.metrics = []any{map[string]any{"key": "read", "unit": "count"}, map[string]any{"key": "unread", "unit": "count"}}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/trpc/integration.request" {
			stats.ServeHTTP(w, r)
			return
		}
		actual, err := to.HTTP(r.Context(), helpers.Request{Path: "/v1/me", Header: map[string]string{"X-Auth-Token": savedToken}})
		if err != nil {
			t.Error("the saved integration's valid token B could not authenticate")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var data map[string]any
		if json.Unmarshal(actual.Body, &data) != nil {
			t.Error("the saved integration's actual identity was not readable")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"data": map[string]any{"json": map[string]any{
			"ok": true, "status": actual.Status, "integration": map[string]any{"id": "miniflux-owned", "kind": "miniflux"}, "data": data,
		}}}})
	}))
	t.Cleanup(server.Close)
	from := helpers.New(helpers.Config{
		App: "homarr", BaseURL: server.URL, HTTP: server.Client(), Log: events.Discard(),
		Secrets: statsSecrets{"HOMARR_API_KEY": "fixture-key", "HOMARR_LINKED_MINIFLUX": "miniflux-owned"},
	})
	localBefore := maps.Clone(to.Secrets.(statsSecrets))
	ownedBefore := maps.Clone(from.Secrets.(statsSecrets))
	err = minifluxTarget().verifyData(t.Context(), from, to, "miniflux-owned")
	minifluxAssertSafeNativeHold(t, from, err, nil, "not-a-real-secret")
	if _, err := minifluxTarget().linked(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("Check admitted an unproven saved token")
	}
	if err := minifluxTarget().add(t.Context(), from, to); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("apply admitted an unproven saved token")
	}
	if requests != 0 || stats.catalogs != 0 || stats.snapshots != 0 || stats.refreshes != 0 || !reflect.DeepEqual(localBefore, to.Secrets) || !reflect.DeepEqual(ownedBefore, from.Secrets) {
		t.Fatal("same-account substitution changed credentials/ownership or reached Homarr/Stats")
	}
}
