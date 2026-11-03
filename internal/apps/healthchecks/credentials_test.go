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
	"github.com/ajnart/self-host/internal/events"
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
	failure   error
}

func (s *healthchecksFaultSecrets) Get(key string) string { return s.stored[key] }

func (s *healthchecksFaultSecrets) Set(key, value string) error {
	if key == s.name {
		s.writes++
		if s.writes == s.failWrite {
			if s.failure != nil {
				return s.failure
			}
			return errors.New("synthetic private storage failure")
		}
	}
	s.stored[key] = value
	return nil
}

func TestHealthchecksExistingKeyOwnershipStorageFailureDiscardsPrivateCause(t *testing.T) {
	fake := newFake(t)
	h, stored, records := harness(t, fake)
	delete(stored, "HEALTHCHECKS_HOMARR_KEY_OWNERSHIP")
	before := maps.Clone(stored)
	private := "synthetic-unregistered-private-storage-detail"
	storageErr := fmt.Errorf("%s: %s: %s", private, apiKey, healthchecksOwnership(apiKey))
	h.Config.Secrets = &healthchecksFaultSecrets{stored: stored, name: "HEALTHCHECKS_HOMARR_KEY_OWNERSHIP", failWrite: 1, failure: storageErr}
	var emitted []events.Event
	h.Log.AddSink(events.SinkFunc(func(event events.Event) { emitted = append(emitted, event) }))
	err := (healthchecks.App{}).Setup(t.Context(), h)
	const failure = "Healthchecks verified credential ownership could not be saved; repair private storage and retry Setup without replacing the key"
	if !errors.Is(err, helpers.ErrStepFailed) || errors.Is(err, storageErr) || !strings.Contains(err.Error(), failure) {
		t.Fatal("ownership persistence did not return a fixed stage error without its private cause")
	}
	var inspect func(error)
	inspect = func(cause error) {
		if cause == nil {
			return
		}
		for _, secret := range []string{private, apiKey, healthchecksOwnership(apiKey)} {
			if strings.Contains(cause.Error(), secret) {
				t.Fatal("private storage details survived in the returned error chain")
			}
		}
		switch cause := cause.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range cause.Unwrap() {
				inspect(child)
			}
		case interface{ Unwrap() error }:
			inspect(cause.Unwrap())
		}
	}
	inspect(err)
	if len(*records) == 0 || (*records)[len(*records)-1].Error != failure {
		t.Fatal("Step did not record the fixed ownership persistence error")
	}
	for _, secret := range []string{private, apiKey, healthchecksOwnership(apiKey)} {
		if strings.Contains(fmt.Sprint(*records, emitted), secret) {
			t.Fatal("private storage details escaped through Step output")
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !maps.Equal(stored, before) || fake.issuances != 0 {
		t.Fatal("failed existing-key ownership persistence changed private records or issued a key")
	}
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
		{"unrelated inputs outside selected modal", http.StatusInternalServerError, `<input readonly value="unrelated-before">` + healthchecksCreatedModal(healthchecksIssuedKey) + `<div><input readonly value="unrelated-after"></div>`, 1, healthchecksIssuedKey},
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
				Candidates             json.RawMessage
			}
			if json.Unmarshal(pending.Issued.Data, &evidence) != nil || evidence.Status != scenario.status || evidence.CandidateCount != scenario.candidates {
				t.Fatal("provider outcome evidence did not survive")
			}
			if len(evidence.Candidates) != 0 {
				t.Fatal("unselected candidate secrets were persisted")
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

func TestHealthchecksAmbiguousCandidatesAreOnlyRedactedInMemory(t *testing.T) {
	for _, body := range []string{
		healthchecksCreatedModal("first-one-time-secret") + healthchecksCreatedModal("second-one-time-secret"),
		strings.Replace(healthchecksCreatedModal("first-one-time-secret"), ` readonly />`, ` readonly /><input readonly value="second-one-time-secret">`, 1),
	} {
		fake := newFake(t)
		fake.issuanceBody = body
		h, stored, records := harness(t, fake)
		healthchecksFreshCredential(fake, stored)
		for range 2 {
			if err := (healthchecks.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("ambiguous issuance did not remain Manual")
			}
		}
		journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
		pending, exists, err := journal.Pending(h)
		if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != "" || string(pending.Issued.Data) != `{"status":200,"transportFailed":false,"candidateCount":2}` {
			t.Fatal("ambiguity did not persist only the nonsecret outcome projection")
		}
		for _, candidate := range []string{"first-one-time-secret", "second-one-time-secret"} {
			if h.Log.Redact(candidate) != "[redacted]" || strings.Contains(fmt.Sprint(*records), candidate) {
				t.Fatal("an ambiguous candidate was not registered for in-memory redaction")
			}
			for _, value := range stored {
				if strings.Contains(value, candidate) {
					t.Fatal("an unselected candidate escaped into private persistence")
				}
			}
		}
		fake.mu.Lock()
		issued := fake.issuances
		fake.mu.Unlock()
		if issued != 1 || stored["HEALTHCHECKS_HOMARR_API_KEY"] != "" || stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] != "" {
			t.Fatal("ambiguous evidence was committed or reissued")
		}
	}
}

func TestHealthchecksTruncatedModalCapturesSelectedKeyWithoutCommitOrReissue(t *testing.T) {
	modal := healthchecksCreatedModal(healthchecksIssuedKey)
	inputEnd := strings.Index(modal, " />") + len(" />")
	for _, suffix := range []string{"", "</div>", "</div></div>", `</div><div class="modal-footer"`, `</div><div class="modal-footer">Keep this key</div></div></div>`} {
		t.Run(fmt.Sprint(len(suffix)), func(t *testing.T) {
			fake := newFake(t)
			fake.issuanceBody = modal[:inputEnd] + suffix
			h, stored, records := harness(t, fake)
			healthchecksFreshCredential(fake, stored)
			if err := (healthchecks.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("truncated selected modal did not remain Manual")
			}
			journal := helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
			pending, exists, err := journal.Pending(h)
			if err != nil || !exists || pending.Issued == nil || pending.Issued.Secret != healthchecksIssuedKey || string(pending.Issued.Data) != `{"status":200,"transportFailed":false,"candidateCount":1,"incomplete":true}` {
				t.Fatal("the complete selected input was not captured with incomplete outcome metadata")
			}
			before := maps.Clone(stored)
			if err := (healthchecks.App{}).Setup(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("incomplete captured outcome was accepted on retry")
			}
			if err := (healthchecks.App{}).Check(t.Context(), h); !errors.Is(err, helpers.ErrManual) {
				t.Fatal("read-only Check accepted unfinished issuance")
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if !maps.Equal(before, stored) || fake.issuances != 1 || stored["HEALTHCHECKS_HOMARR_API_KEY"] != "" || stored["HEALTHCHECKS_HOMARR_KEY_OWNERSHIP"] != "" || strings.Contains(fmt.Sprint(*records), healthchecksIssuedKey) {
				t.Fatal("incomplete outcome was committed, changed, leaked or reissued")
			}
			for _, request := range fake.requests {
				if strings.Contains(request, "/api/v3/") {
					t.Fatal("incomplete outcome reached credential acceptance")
				}
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
