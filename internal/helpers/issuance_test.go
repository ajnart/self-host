package helpers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
)

var issuanceJournal = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_ownership", PendingName: "homarr_pending",
}

type issuanceWrite struct{ key, value string }

type issuanceStore struct {
	values  map[string]string
	disk    map[string]string
	writes  []issuanceWrite
	failAt  int
	failure string
}

func newIssuanceStore() *issuanceStore {
	return &issuanceStore{values: map[string]string{}, disk: map[string]string{}}
}

func (s *issuanceStore) Get(key string) string { return s.values[key] }

func (s *issuanceStore) Set(key, value string) error {
	s.writes = append(s.writes, issuanceWrite{key, value})
	fail := s.failAt == len(s.writes)
	if !fail || s.failure != "before-memory" {
		s.values[key] = value
	}
	if !fail || s.failure == "after-disk" {
		s.disk = maps.Clone(s.values)
	}
	if fail {
		return errors.New("private-store-canary: " + value)
	}
	return nil
}

func issuanceH(s *issuanceStore) *helpers.H {
	return helpers.New(helpers.Config{App: "demo", Secrets: s, Log: events.Discard()})
}

func TestCredentialIssuanceCommitsOnlyAfterCapturedEvidence(t *testing.T) {
	s := newIssuanceStore()
	h := issuanceH(s)
	intent := json.RawMessage(`{"account":"selected"}`)
	if err := issuanceJournal.Begin(h, intent); err != nil {
		t.Fatal(err)
	}
	if err := issuanceJournal.Begin(h, intent); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("pending intent permitted another issuance: %v", err)
	}
	if err := issuanceJournal.Commit(h, json.RawMessage(`{"id":7}`)); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("uncaptured intent committed: %v", err)
	}
	evidence := helpers.CredentialEvidence{Secret: "issued-key", Data: json.RawMessage(`{"id":7,"status":201}`)}
	if err := issuanceJournal.Capture(h, evidence); err != nil {
		t.Fatal(err)
	}
	before := len(s.writes)
	pending, exists, err := issuanceJournal.Pending(h)
	if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != "issued-key" || string(pending.Intent) != string(intent) || len(s.writes) != before {
		t.Fatalf("pending read did not retain evidence without writing: exists=%v err=%v", exists, err)
	}
	if err := issuanceJournal.Commit(h, json.RawMessage(`{ "id": 7 }`)); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, write := range s.writes {
		keys = append(keys, write.key)
	}
	want := []string{"DEMO_HOMARR_PENDING", "DEMO_HOMARR_PENDING", "DEMO_HOMARR_PENDING", "DEMO_HOMARR_OWNERSHIP", "DEMO_HOMARR_API_KEY", "DEMO_HOMARR_PENDING"}
	if !reflect.DeepEqual(keys, want) || s.disk["DEMO_HOMARR_API_KEY"] != "issued-key" || s.disk["DEMO_HOMARR_OWNERSHIP"] != `{"id":7}` || s.disk["DEMO_HOMARR_PENDING"] != "" {
		t.Fatalf("commit did not checkpoint then save ownership, secret and clear pending: %v", keys)
	}
	if err := issuanceJournal.Begin(h, intent); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("saved credential permitted replacement: %v", err)
	}
}

func TestCredentialIssuanceWriteFailuresNeverAuthorizeReissuance(t *testing.T) {
	for _, mode := range []string{"before-memory", "after-memory", "after-disk"} {
		for failAt := 1; failAt <= 6; failAt++ {
			t.Run(fmt.Sprintf("%s/write-%d", mode, failAt), func(t *testing.T) {
				s := newIssuanceStore()
				s.failAt, s.failure = failAt, mode
				h := issuanceH(s)
				intent := json.RawMessage(`{"account":"selected"}`)
				issued := helpers.CredentialEvidence{Secret: "one-time-key", Data: json.RawMessage(`{"status":201,"id":7}`)}
				ownership := json.RawMessage(`{"id":7}`)
				posts := 0
				err := issuanceJournal.Begin(h, intent)
				if err == nil {
					posts++
					err = issuanceJournal.Capture(h, issued)
				}
				if err == nil {
					err = issuanceJournal.Commit(h, ownership)
				}
				if err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "one-time-key") || errors.Unwrap(err) != nil {
					t.Fatalf("failure must be fixed and retain no unsafe cause: %v", err)
				}
				if failAt == 1 && posts != 0 || failAt > 1 && posts != 1 || len(s.writes) != failAt {
					t.Fatalf("writes/posts continued after failure: writes=%d posts=%d", len(s.writes), posts)
				}
				if s.values["DEMO_HOMARR_PENDING"] != "" || s.values["DEMO_HOMARR_API_KEY"] != "" || s.values["DEMO_HOMARR_OWNERSHIP"] != "" {
					if err := issuanceJournal.Begin(h, intent); !errors.Is(err, helpers.ErrManual) {
						t.Fatalf("unacknowledged memory value authorized issue: %v", err)
					}
				}
				s.values = maps.Clone(s.disk)
				s.failAt = 0
				h = issuanceH(s)
				pending, exists, err := issuanceJournal.Pending(h)
				if err != nil {
					t.Fatal(err)
				}
				if failAt == 1 && !exists {
					return
				}
				if err := issuanceJournal.Begin(h, intent); !errors.Is(err, helpers.ErrManual) {
					t.Fatalf("restart authorized another issuance: %v", err)
				}
				if !exists {
					if s.disk["DEMO_HOMARR_API_KEY"] != issued.Secret || s.disk["DEMO_HOMARR_OWNERSHIP"] != string(ownership) {
						t.Fatal("missing journal without the exact completed tuple")
					}
					return
				}
				if pending.Issued == nil {
					if err := issuanceJournal.Commit(h, ownership); !errors.Is(err, helpers.ErrManual) {
						t.Fatalf("intent-only restart completed without a captured secret: %v", err)
					}
					return
				}
				before := len(s.writes)
				if err := issuanceJournal.Commit(h, ownership); err != nil {
					t.Fatal(err)
				}
				if len(s.writes) != before+4 || s.writes[before].key != "DEMO_HOMARR_PENDING" || s.disk["DEMO_HOMARR_API_KEY"] != issued.Secret || s.disk["DEMO_HOMARR_PENDING"] != "" {
					t.Fatal("resumption did not re-checkpoint and complete the original tuple")
				}
			})
		}
	}
}

func TestCredentialIssuancePreservesMalformedAndConflictingRecords(t *testing.T) {
	for _, raw := range []string{
		`not-json`, `null`, `{}`, `{"version":2,"intent":{},"issued":null}`,
		`{"version":1,"intent":null,"issued":null}`,
		`{"version":1,"intent":{},"issued":null,"extra":true}`,
		`{"version":1,"version":1,"intent":{},"issued":null}`,
		`{"version":1,"intent":{},"issued":{"secret":null,"data":{}}}`,
		`{"version":1,"intent":{},"issued":{"secret":"first","secret":"second","data":{}}}`,
		`{"version":1,"intent":{},"issued":{"secret":"key","data":[]}}`,
		`{"version":1,"intent":{},"issued":null} {}`,
		`{"version":2,"intent":{"name":"A&B"},"issued":{"secret":"synthetic<&>","data":{"name":"<unknown>"}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			s := newIssuanceStore()
			s.values["DEMO_HOMARR_PENDING"] = raw
			h := issuanceH(s)
			if _, _, err := issuanceJournal.Pending(h); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("malformed journal admitted: %v", err)
			}
			if err := issuanceJournal.Begin(h, json.RawMessage(`{}`)); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("malformed journal authorized issue: %v", err)
			}
			if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: "synthetic<&>", Data: json.RawMessage(`{"name":"<unknown>"}`)}); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("malformed journal allowed capture: %v", err)
			}
			if err := issuanceJournal.Commit(h, json.RawMessage(`{"name":"<unknown>"}`)); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("malformed journal allowed completion: %v", err)
			}
			if len(s.writes) != 0 || s.values["DEMO_HOMARR_PENDING"] != raw {
				t.Fatal("malformed journal changed")
			}
		})
	}
	for _, name := range []string{"DEMO_HOMARR_API_KEY", "DEMO_HOMARR_OWNERSHIP"} {
		s := newIssuanceStore()
		h := issuanceH(s)
		if err := issuanceJournal.Begin(h, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: "issued", Data: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		s.values[name] = "user-owned-conflict"
		before := len(s.writes)
		if err := issuanceJournal.Commit(h, json.RawMessage(`{"id":7}`)); !errors.Is(err, helpers.ErrManual) || len(s.writes) != before || s.values[name] != "user-owned-conflict" {
			t.Fatalf("conflict overwritten: %v", err)
		}
	}
}

func TestCredentialIssuanceCaptureIsImmutableAndKeepsPartialEvidence(t *testing.T) {
	s := newIssuanceStore()
	h := issuanceH(s)
	if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: "unknown"}); !errors.Is(err, helpers.ErrManual) {
		t.Fatalf("capture without intent admitted: %v", err)
	}
	if err := issuanceJournal.Begin(h, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	partial := helpers.CredentialEvidence{Data: json.RawMessage(`{"status":500}`)}
	if err := issuanceJournal.Capture(h, partial); err != nil {
		t.Fatal(err)
	}
	if err := issuanceJournal.Capture(h, partial); err != nil {
		t.Fatal(err)
	}
	before := len(s.writes)
	if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: "replacement"}); !errors.Is(err, helpers.ErrManual) || len(s.writes) != before {
		t.Fatalf("different capture replaced evidence: %v", err)
	}
	if err := issuanceJournal.Commit(h, json.RawMessage(`{}`)); !errors.Is(err, helpers.ErrManual) || len(s.writes) != before {
		t.Fatalf("missing one-time secret completed: %v", err)
	}
}

func TestCredentialIssuanceDoesNotTrustFailedCaptureInMemory(t *testing.T) {
	s := newIssuanceStore()
	h := issuanceH(s)
	if err := issuanceJournal.Begin(h, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	s.failAt, s.failure = 2, "after-memory"
	if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: "unacknowledged", Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("capture write failure was hidden")
	}
	s.failAt = 3
	if err := issuanceJournal.Commit(h, json.RawMessage(`{"id":7}`)); err == nil || len(s.writes) != 3 || s.values["DEMO_HOMARR_API_KEY"] != "" || s.values["DEMO_HOMARR_OWNERSHIP"] != "" {
		t.Fatalf("memory read bypassed the failed checkpoint: %v", err)
	}
}

func TestCredentialIssuanceMasksRawSecretAndRejectsInvalidNames(t *testing.T) {
	for _, names := range []helpers.CredentialIssuance{
		{}, {SecretName: "API_KEY", OwnershipName: "owner", PendingName: "pending"},
		{SecretName: "api_key", OwnershipName: "api_key", PendingName: "pending"},
		{SecretName: "api_key", OwnershipName: "owner", PendingName: "../pending"},
	} {
		s := newIssuanceStore()
		if err := names.Begin(issuanceH(s), json.RawMessage(`{}`)); !errors.Is(err, helpers.ErrManual) || len(s.writes) != 0 {
			t.Fatalf("invalid App-local names admitted: %v", err)
		}
	}
	s := newIssuanceStore()
	h := issuanceH(s)
	if err := issuanceJournal.Begin(h, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	secret := "one\"time\\private"
	if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: secret, Data: json.RawMessage(`{"status":500}`)}); err != nil {
		t.Fatal(err)
	}
	if h.Log.Redact(secret) != "[redacted]" {
		t.Fatal("only escaped JSON was registered for masking")
	}
	h = issuanceH(s)
	if _, _, err := issuanceJournal.Pending(h); err != nil || h.Log.Redact(secret) != "[redacted]" {
		t.Fatalf("read did not register captured raw secret: %v", err)
	}
}

func TestCredentialIssuanceReplaysIdenticalEvidenceWithPersistenceEscaping(t *testing.T) {
	for _, scenario := range []struct {
		name, data, persisted string
	}{
		{"ampersand", `{"name":"A&B"}`, `{"name":"A\u0026B"}`},
		{"angle brackets", `{"name":"<A&B>"}`, `{"name":"\u003cA\u0026B\u003e"}`},
		{"unicode and newline", "{\"name\":\"雪\u2028line\u2029end\",\"escaped\":\"\\u96ea\",\"line\":\"first\\nsecond\"}", `{"name":"雪\u2028line\u2029end","escaped":"\u96ea","line":"first\nsecond"}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := newIssuanceStore()
			h := issuanceH(s)
			if err := issuanceJournal.Begin(h, json.RawMessage(`{"account":"A&B"}`)); err != nil {
				t.Fatal(err)
			}
			issued := helpers.CredentialEvidence{Secret: "synthetic<&>\"\\雪\nsecret", Data: json.RawMessage(scenario.data)}
			if err := issuanceJournal.Capture(h, issued); err != nil {
				t.Fatal(err)
			}
			h = issuanceH(s)
			before := len(s.writes)
			pending, exists, err := issuanceJournal.Pending(h)
			if err != nil || !exists || pending.Issued == nil || string(pending.Issued.Data) != scenario.persisted || pending.Issued.Secret != issued.Secret || len(s.writes) != before {
				t.Fatalf("persisted evidence did not round-trip read-only: exists=%v err=%v", exists, err)
			}
			if h.Log.Redact(issued.Secret) != "[redacted]" {
				t.Fatal("read masked only the JSON-escaped secret")
			}
			if err := issuanceJournal.Capture(h, issued); err != nil {
				t.Fatalf("identical original evidence was rejected after persistence: %v", err)
			}
			if len(s.writes) != before+1 || s.writes[before].key != "DEMO_HOMARR_PENDING" {
				t.Fatal("identical replay did not repeat its write barrier")
			}
			if err := issuanceJournal.Commit(h, json.RawMessage(`{"name":"A&B<owner>"}`)); err != nil {
				t.Fatal(err)
			}
			if s.disk["DEMO_HOMARR_API_KEY"] != issued.Secret || s.disk["DEMO_HOMARR_PENDING"] != "" {
				t.Fatal("escaping changed the captured secret or completion ordering")
			}
		})
	}
}

func TestCredentialIssuanceEscapedReplayRepeatsAmbiguousWriteBarrier(t *testing.T) {
	for _, mode := range []string{"after-memory", "after-disk"} {
		t.Run(mode, func(t *testing.T) {
			s := newIssuanceStore()
			h := issuanceH(s)
			if err := issuanceJournal.Begin(h, json.RawMessage(`{"account":"A&B"}`)); err != nil {
				t.Fatal(err)
			}
			issued := helpers.CredentialEvidence{Secret: "synthetic<&>\"\\雪\nsecret", Data: json.RawMessage(`{"name":"<A&B>","unicode":"\u96ea","line":"first\nsecond"}`)}
			s.failAt, s.failure = 2, mode
			if err := issuanceJournal.Capture(h, issued); err == nil || errors.Is(err, helpers.ErrManual) || strings.Contains(err.Error(), "canary") || errors.Unwrap(err) != nil {
				t.Fatalf("ambiguous capture did not return a fixed persistence failure: %v", err)
			}
			if mode == "after-disk" {
				s.values = maps.Clone(s.disk)
				h = issuanceH(s)
			}
			if err := issuanceJournal.Begin(h, json.RawMessage(`{"account":"A&B"}`)); !errors.Is(err, helpers.ErrManual) {
				t.Fatalf("ambiguous capture permitted another issuance: %v", err)
			}
			s.failAt = 3
			if err := issuanceJournal.Capture(h, issued); err == nil || errors.Is(err, helpers.ErrManual) || errors.Unwrap(err) != nil || len(s.writes) != 3 {
				t.Fatalf("same capture skipped the fresh write barrier: %v", err)
			}
			s.failAt = 0
			if err := issuanceJournal.Capture(h, issued); err != nil || len(s.writes) != 4 {
				t.Fatalf("same capture could not resume its write: %v", err)
			}
			before := len(s.writes)
			if err := issuanceJournal.Commit(h, json.RawMessage(`{"name":"A&B<owner>"}`)); err != nil {
				t.Fatal(err)
			}
			keys := []string{}
			for _, write := range s.writes[before:] {
				keys = append(keys, write.key)
			}
			want := []string{"DEMO_HOMARR_PENDING", "DEMO_HOMARR_OWNERSHIP", "DEMO_HOMARR_API_KEY", "DEMO_HOMARR_PENDING"}
			if !reflect.DeepEqual(keys, want) || s.disk["DEMO_HOMARR_API_KEY"] != issued.Secret || s.disk["DEMO_HOMARR_PENDING"] != "" || h.Log.Redact(issued.Secret) != "[redacted]" {
				t.Fatal("escaped recovery changed ordering, raw-secret masking or the final credential")
			}
		})
	}
}

func TestCredentialIssuanceEncodingNormalizationDoesNotEquateChangedEvidence(t *testing.T) {
	for _, data := range []string{
		`{"name":"A&B!","unicode":"\u0041","count":1}`,
		`{"name":"A&B","unicode":"A","count":1}`,
		`{"name":"A&B","unicode":"\u0041","count":1.0}`,
		`{"count":1,"unicode":"\u0041","name":"A&B"}`,
	} {
		s := newIssuanceStore()
		h := issuanceH(s)
		if err := issuanceJournal.Begin(h, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		issued := helpers.CredentialEvidence{Secret: "synthetic<&>\nsecret", Data: json.RawMessage(`{"name":"A&B","unicode":"\u0041","count":1}`)}
		if err := issuanceJournal.Capture(h, issued); err != nil {
			t.Fatal(err)
		}
		before, stored := len(s.writes), s.disk["DEMO_HOMARR_PENDING"]
		if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: issued.Secret, Data: json.RawMessage(data)}); !errors.Is(err, helpers.ErrManual) || len(s.writes) != before || s.disk["DEMO_HOMARR_PENDING"] != stored || s.values["DEMO_HOMARR_PENDING"] != stored {
			t.Fatalf("changed evidence or provider-level JSON equivalence bypassed the hold: %v", err)
		}
		if err := issuanceJournal.Capture(h, helpers.CredentialEvidence{Secret: "different<&>\nsecret", Data: issued.Data}); !errors.Is(err, helpers.ErrManual) || len(s.writes) != before {
			t.Fatalf("different raw credential bypassed the hold: %v", err)
		}
	}
}
