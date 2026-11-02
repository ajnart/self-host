package healthchecks_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/ajnart/self-host/internal/apps/healthchecks"
	"github.com/ajnart/self-host/internal/helpers"
)

const healthchecksIssuedKey = "hcr_0123456789012345678901234567"

func healthchecksCreatedModal(value string) string {
	return `<div id="key-created-modal" class="modal"><div class="modal-dialog"><div class="modal-content"><div class="modal-header"><h4>API Key Created!</h4></div><div class="modal-body"><input type="text" class="form-control" value="` + value + `" readonly /></div><div class="modal-footer">Keep this key</div></div></div></div>`
}

func healthchecksOwnership(key string) string {
	fingerprint := sha256.Sum256([]byte(key))
	return fmt.Sprintf(`{"version":1,"selection":{"owner":"admin@selfhost.local","projectId":"%s","badge":"%s"},"fingerprint":"%s"}`, projectID, badgeURL, hex.EncodeToString(fingerprint[:]))
}

func healthchecksFreshCredential(fake *fakeHealthchecks, stored secrets) {
	delete(stored, "HEALTHCHECKS_HOMARR_API_KEY")
	delete(stored, "HEALTHCHECKS_HOMARR_KEY_OWNERSHIP")
	fake.slotPopulated = false
}

func TestHealthchecksIssuesOneProjectReadOnlyKeyAndReusesIt(t *testing.T) {
	fake := newFake(t)
	h, stored, _ := harness(t, fake)
	healthchecksFreshCredential(fake, stored)
	for range 3 {
		if err := (healthchecks.App{}).Setup(t.Context(), h); err != nil {
			t.Fatal(err)
		}
		if err := (healthchecks.App{}).Check(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}
	fake.mu.Lock()
	issued := fake.issuances
	fake.mu.Unlock()
	if issued != 1 || stored["HEALTHCHECKS_HOMARR_API_KEY"] != healthchecksIssuedKey || stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] != healthchecksOwnership(healthchecksIssuedKey) || stored["HEALTHCHECKS_HOMARR_KEY_PENDING"] != "" {
		t.Fatal("issuance did not persist exact private ownership or repeated the provider action")
	}
}

func TestHealthchecksExistingExplicitKeyGetsVerifiedOwnershipWithoutIssuance(t *testing.T) {
	fake := newFake(t)
	h, stored, _ := harness(t, fake)
	delete(stored, "HEALTHCHECKS_HOMARR_KEY_OWNERSHIP")
	if err := (healthchecks.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
		t.Fatal("Check must not infer or persist missing credential ownership")
	}
	if err := (healthchecks.App{}).Setup(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	issued := fake.issuances
	fake.mu.Unlock()
	if issued != 0 || stored["HEALTHCHECKS_HOMARR_API_KEY"] != apiKey || stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] != healthchecksOwnership(apiKey) {
		t.Fatal("explicit known key was rotated or ownership did not bind its exact project")
	}
}

type healthchecksFaultSecrets struct {
	stored    secrets
	name      string
	failWrite int
	writes    int
}

func (s *healthchecksFaultSecrets) Get(key string) string { return s.stored[key] }

func (s *healthchecksFaultSecrets) Set(key, value string) error {
	if key == s.name {
		s.writes++
		if s.writes == s.failWrite {
			return errors.New("synthetic private storage failure")
		}
	}
	s.stored[key] = value
	return nil
}

func TestHealthchecksCapturedCredentialSurvivesCommitFailures(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		failWrite int
	}{
		{"HEALTHCHECKS_HOMARR_KEY_OWNERSHIP", 1},
		{"HEALTHCHECKS_HOMARR_API_KEY", 1},
		{"HEALTHCHECKS_HOMARR_KEY_PENDING", 4},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake := newFake(t)
			h, stored, _ := harness(t, fake)
			healthchecksFreshCredential(fake, stored)
			h.Config.Secrets = &healthchecksFaultSecrets{stored: stored, name: scenario.name, failWrite: scenario.failWrite}
			if err := (healthchecks.App{}).Setup(t.Context(), h); err == nil {
				t.Fatal("failed private persistence was accepted")
			}
			pending, exists, err := (helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}).Pending(h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != healthchecksIssuedKey {
				t.Fatal("selected one-time evidence was lost after commit failure")
			}
			if err := (healthchecks.App{}).Setup(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			issued := fake.issuances
			fake.mu.Unlock()
			if issued != 1 || stored["HEALTHCHECKS_HOMARR_KEY_PENDING"] != "" || stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] != healthchecksOwnership(healthchecksIssuedKey) {
				t.Fatal("captured recovery reissued or failed to complete exact ownership")
			}
		})
	}
}

func TestHealthchecksCapturePrecedesStatusAndCredentialSemantics(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		status     int
		body       string
		candidates int
		secret     string
	}{
		{"HTTP failure carrying key", http.StatusInternalServerError, healthchecksCreatedModal(healthchecksIssuedKey), 1, healthchecksIssuedKey},
		{"reordered readonly attribute", http.StatusInternalServerError, strings.ReplaceAll(healthchecksCreatedModal(healthchecksIssuedKey), `value="`+healthchecksIssuedKey+`" readonly`, `readonly value="`+healthchecksIssuedKey+`"`), 1, healthchecksIssuedKey},
		{"malformed returned key", http.StatusOK, healthchecksCreatedModal("one-time-unexpected-secret"), 1, "one-time-unexpected-secret"},
		{"ambiguous selected modal", http.StatusOK, healthchecksCreatedModal("first-one-time-secret") + healthchecksCreatedModal("second-one-time-secret"), 2, ""},
		{"missing selected modal", http.StatusOK, `<p>no one-time credential was returned</p>`, 0, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake := newFake(t)
			fake.issuanceStatus, fake.issuanceBody = scenario.status, scenario.body
			h, stored, records := harness(t, fake)
			healthchecksFreshCredential(fake, stored)
			for range 2 {
				if err := (healthchecks.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) || scenario.secret != "" && strings.Contains(err.Error(), scenario.secret) {
					t.Fatal("unaccepted issuance outcome did not remain private Manual recovery")
				}
			}
			journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
			pending, exists, err := journal.Pending(h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != scenario.secret {
				t.Fatal("selected evidence was not captured before acceptance")
			}
			var evidence struct {
				Status, CandidateCount int
				Candidates             []string
			}
			if json.Unmarshal(pending.Issued.Data, &evidence) != nil || evidence.Status != scenario.status || evidence.CandidateCount != scenario.candidates {
				t.Fatal("provider outcome evidence did not survive")
			}
			if scenario.candidates == 2 && len(evidence.Candidates) != 2 {
				t.Fatal("ambiguous one-time evidence was discarded")
			}
			fake.mu.Lock()
			issued := fake.issuances
			fake.mu.Unlock()
			if issued != 1 || stored["HEALTHCHECKS_HOMARR_API_KEY"] != "" || stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] != "" || scenario.secret != "" && strings.Contains(fmt.Sprint(*records), scenario.secret) {
				t.Fatal("unaccepted evidence was committed, leaked or reissued")
			}
		})
	}
}

func TestHealthchecksCaptureStorageLossAndSemanticFailureNeverReissue(t *testing.T) {
	for _, failCapture := range []bool{false, true} {
		t.Run(fmt.Sprint(failCapture), func(t *testing.T) {
			fake := newFake(t)
			h, stored, _ := harness(t, fake)
			healthchecksFreshCredential(fake, stored)
			if failCapture {
				h.Config.Secrets = &healthchecksFaultSecrets{stored: stored, name: "HEALTHCHECKS_HOMARR_KEY_PENDING", failWrite: 2}
			} else {
				fake.checks = `{"checks":null}`
			}
			if err := (healthchecks.App{}).Setup(t.Context(), h); err == nil {
				t.Fatal("uncertain or semantically invalid issuance was accepted")
			}
			fake.mu.Lock()
			fake.checks = `{"checks":[]}`
			fake.mu.Unlock()
			err := (healthchecks.App{}).Setup(t.Context(), h)
			if failCapture && !errors.Is(err, helpers.ErrManual) || !failCapture && err != nil {
				t.Fatal("capture uncertainty was retried or captured valid evidence could not recover")
			}
			fake.mu.Lock()
			issued := fake.issuances
			fake.mu.Unlock()
			if issued != 1 {
				t.Fatal("provider key issuance repeated after uncertainty")
			}
		})
	}
}

func TestHealthchecksCorruptionPendingSelectionAndSlotCollisionsStopBeforeIssuance(t *testing.T) {
	for _, scenario := range []string{"corrupt pending", "pending without evidence", "foreign pending project", "foreign captured owner", "corrupt captured metadata", "active key collision", "corrupt ownership", "foreign ownership", "populated slot", "slot changed after intent"} {
		t.Run(scenario, func(t *testing.T) {
			fake := newFake(t)
			h, stored, _ := harness(t, fake)
			healthchecksFreshCredential(fake, stored)
			journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
			switch scenario {
			case "corrupt pending":
				stored["HEALTHCHECKS_HOMARR_KEY_PENDING"] = `{"version":999,"intent":{}}`
			case "pending without evidence", "foreign pending project":
				id := projectID
				if scenario == "foreign pending project" {
					id = "37d76ba7-a070-4fe1-89ba-20c425a30579"
				}
				if err := journal.Begin(h, json.RawMessage(fmt.Sprintf(`{"owner":"admin@selfhost.local","projectId":"%s","badge":"%s"}`, id, badgeURL))); err != nil {
					t.Fatal(err)
				}
			case "corrupt ownership":
				stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] = `{bad`
			case "foreign ownership":
				stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] = strings.ReplaceAll(healthchecksOwnership(apiKey), projectID, "37d76ba7-a070-4fe1-89ba-20c425a30579")
			case "populated slot":
				fake.slotPopulated = true
			case "slot changed after intent":
				fake.populateAt = 3
			case "foreign captured owner", "corrupt captured metadata", "active key collision":
				owner := "admin@selfhost.local"
				if scenario == "foreign captured owner" {
					owner = "different@selfhost.local"
				}
				intent := json.RawMessage(fmt.Sprintf(`{"owner":"%s","projectId":"%s","badge":"%s"}`, owner, projectID, badgeURL))
				if err := journal.Begin(h, intent); err != nil {
					t.Fatal(err)
				}
				data := json.RawMessage(`{"status":200,"transportFailed":false,"candidateCount":1}`)
				if scenario == "corrupt captured metadata" {
					data = json.RawMessage(`{"status":200,"transportFailed":false,"candidateCount":"1"}`)
				}
				if err := journal.Capture(h, helpers.CredentialEvidence{Secret: healthchecksIssuedKey, Data: data}); err != nil {
					t.Fatal(err)
				}
				fake.key = healthchecksIssuedKey
				if scenario == "active key collision" {
					stored["HEALTHCHECKS_HOMARR_API_KEY"] = apiKey
					stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] = healthchecksOwnership(apiKey)
				}
			}
			before := maps.Clone(stored)
			for range 2 {
				if err := (healthchecks.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
					t.Fatal("uncertainty did not remain Manual")
				}
			}
			fake.mu.Lock()
			issued := fake.issuances
			fake.mu.Unlock()
			if issued != 0 {
				t.Fatal("uncertain ownership, pending state or slot collision issued a key")
			}
			if scenario != "slot changed after intent" && !maps.Equal(before, stored) {
				t.Fatal("Manual recovery rewrote existing private records")
			}
		})
	}
}

func TestHealthchecksCannotIssueBeforeIntentPersists(t *testing.T) {
	fake := newFake(t)
	h, stored, _ := harness(t, fake)
	healthchecksFreshCredential(fake, stored)
	h.Config.Secrets = &healthchecksFaultSecrets{stored: stored, name: "HEALTHCHECKS_HOMARR_KEY_PENDING", failWrite: 1}
	if err := (healthchecks.App{}).Setup(t.Context(), h); err == nil {
		t.Fatal("failed issuance intent was accepted")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.issuances != 0 || stored["HEALTHCHECKS_HOMARR_KEY_PENDING"] != "" {
		t.Fatal("provider action preceded persisted intent")
	}
}
