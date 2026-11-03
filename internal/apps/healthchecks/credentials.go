package healthchecks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"

	"github.com/ajnart/self-host/internal/helpers"
)

type projectSelection struct {
	Owner     string `json:"owner"`
	ProjectID string `json:"projectId"`
	Badge     string `json:"badge"`
}

type keyOwnership struct {
	Version     int              `json:"version"`
	Selection   projectSelection `json:"selection"`
	Fingerprint string           `json:"fingerprint"`
}

type keyEvidence struct {
	Status          int  `json:"status"`
	TransportFailed bool `json:"transportFailed"`
	CandidateCount  int  `json:"candidateCount"`
	Incomplete      bool `json:"incomplete,omitempty"`
}

var credentialJournal = helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending"}
var settingsRows = regexp.MustCompile(`(?s)<tr\b[^>]*>(.*?)</tr>`)
var readOnlyLabel = regexp.MustCompile(`<td>\s*API key \(read-only\)\s*</td>`)
var emptyReadOnly = regexp.MustCompile(`data-create-key=["']api_key_readonly["']`)
var populatedReadOnly = regexp.MustCompile(`data-revoke-key=["']api_key_readonly["']`)
var createdModal = regexp.MustCompile(`^<div\b[^>]*\bid=["']key-created-modal["'][^>]*>`)
var createdTags = regexp.MustCompile(`<div\b[^>]*>|</div\s*>|<input\b[^>]*>`)
var createdValue = regexp.MustCompile(`\bvalue\s*=\s*["']([^"']*)["']`)
var createdReadOnly = regexp.MustCompile(`\sreadonly(?:\s|=|/?>)`)
var issuedKeyPattern = regexp.MustCompile(`^hcr_[a-zA-Z0-9]{28}$`)

func ownershipFor(selection projectSelection, key string) keyOwnership {
	return keyOwnership{Version: 1, Selection: selection, Fingerprint: helpers.Fingerprint(key)}
}

func credentialOwned(h *helpers.H, selection projectSelection) error {
	_, pending, err := credentialJournal.Pending(h)
	if err != nil {
		return err
	}
	if pending {
		return &helpers.ManualError{Hint: "Healthchecks has unfinished credential issuance; run its Setup to verify captured evidence without another issuance. " + keyHint}
	}
	key := h.Secret(h.Key("homarr_api_key"))
	encoded := h.Secret(h.Key("homarr_key_ownership"))
	h.Log.Secret(key, encoded)
	expected, err := json.Marshal(ownershipFor(selection, key))
	if err != nil || key == "" || !bytes.Equal(bytes.TrimSpace([]byte(encoded)), expected) {
		return &helpers.ManualError{Hint: "Healthchecks credential ownership does not match the selected owner, project and saved key. " + keyHint}
	}
	return nil
}

func ensureCredential(ctx context.Context, h *helpers.H) error {
	pending, exists, err := credentialJournal.Pending(h)
	if err != nil {
		return err
	}
	selection, session, err := selectedIdentity(ctx, h)
	if err != nil {
		return err
	}
	intent, err := json.Marshal(selection)
	if err != nil {
		return errors.New("Healthchecks project identity could not be recorded")
	}
	if exists {
		if !bytes.Equal(pending.Intent, intent) || pending.Issued == nil {
			return &helpers.ManualError{Hint: "Healthchecks has an uncertain or differently selected issuance intent; no further key will be issued. " + keyHint}
		}
		return finishCredential(ctx, h, selection, *pending.Issued)
	}
	key := h.Secret(h.Key("homarr_api_key"))
	owner := h.Secret(h.Key("homarr_key_ownership"))
	if owner != "" {
		if err := credentialOwned(h, selection); err != nil {
			return err
		}
		return verifyCredential(ctx, h, selection, key)
	}
	if key != "" {
		if err := verifyCredential(ctx, h, selection, key); err != nil {
			return err
		}
		ownership, err := json.Marshal(ownershipFor(selection, key))
		if err != nil {
			return errors.New("Healthchecks verified credential ownership could not be recorded")
		}
		if err := h.SetSecret(h.Key("homarr_key_ownership"), string(ownership)); err != nil {
			return errors.New("Healthchecks verified credential ownership could not be saved; repair private storage and retry Setup without replacing the key")
		}
		return nil
	}
	path := "/projects/" + selection.ProjectID + "/settings/"
	csrf, err := session.FormToken(ctx, path, "csrfmiddlewaretoken")
	if err != nil {
		return &helpers.ManualError{Hint: "Healthchecks project settings cannot safely prepare read-only issuance. " + keyHint}
	}
	h.Log.Secret(csrf)
	if err := readOnlySlotEmpty(ctx, session, path); err != nil {
		return err
	}
	if err := credentialJournal.Begin(h, intent); err != nil {
		return err
	}
	if err := readOnlySlotEmpty(ctx, session, path); err != nil {
		return err
	}
	response, requestErr := session.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: path, NoRedirect: true,
		Form:   url.Values{"csrfmiddlewaretoken": {csrf}, "create_key": {"api_key_readonly"}},
		Header: map[string]string{"Referer": h.BaseURL + path},
	})
	evidence := keyEvidence{Status: response.Status, TransportFailed: requestErr != nil}
	selected := ""
	depth := 0
	for _, tag := range createdTags.FindAll(response.Body, -1) {
		if createdModal.Match(tag) {
			evidence.Incomplete = evidence.Incomplete || depth != 0
			depth = 1
			continue
		}
		if depth == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(tag, []byte("</div")):
			depth--
		case bytes.HasPrefix(tag, []byte("<div")):
			depth++
		default:
			value := createdValue.FindSubmatch(tag)
			if !createdReadOnly.Match(tag) || len(value) != 2 {
				continue
			}
			candidate := html.UnescapeString(string(value[1]))
			h.Log.Secret(candidate)
			evidence.CandidateCount++
			selected = candidate
		}
	}
	evidence.Incomplete = evidence.Incomplete || depth != 0
	if evidence.CandidateCount != 1 {
		selected = ""
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		return errors.New("Healthchecks selected credential evidence could not be encoded; preserve its pending issuance")
	}
	issued := helpers.CredentialEvidence{Secret: selected, Data: data}
	if err := credentialJournal.Capture(h, issued); err != nil {
		return err
	}
	return finishCredential(ctx, h, selection, issued)
}

func readOnlySlotEmpty(ctx context.Context, session *helpers.Session, path string) error {
	response, err := session.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true})
	if err != nil || response.Status != http.StatusOK {
		return &helpers.ManualError{Hint: "Healthchecks project settings are unavailable for safe key issuance. " + keyHint}
	}
	var selected [][]byte
	for _, row := range settingsRows.FindAllSubmatch(response.Body, -1) {
		if readOnlyLabel.Match(row[1]) {
			selected = append(selected, row[1])
		}
	}
	if len(selected) != 1 || len(emptyReadOnly.FindAll(selected[0], -1)) != 1 || populatedReadOnly.Match(selected[0]) || bytes.Contains(selected[0], []byte("<code")) {
		return &helpers.ManualError{Hint: "Healthchecks read-only slot is populated or ambiguous; it will not be replaced. " + keyHint}
	}
	return nil
}

func finishCredential(ctx context.Context, h *helpers.H, selection projectSelection, issued helpers.CredentialEvidence) error {
	var evidence keyEvidence
	if json.Unmarshal(issued.Data, &evidence) != nil {
		return &helpers.ManualError{Hint: "Healthchecks captured issuance evidence is unreadable. " + keyHint}
	}
	canonical, err := json.Marshal(evidence)
	if err != nil || !bytes.Equal(canonical, issued.Data) || evidence.Status != http.StatusOK || evidence.TransportFailed || evidence.Incomplete || evidence.CandidateCount != 1 || !issuedKeyPattern.MatchString(issued.Secret) {
		return &helpers.ManualError{Hint: "Healthchecks captured key or issuance outcome needs explicit recovery; it will not be reissued. " + keyHint}
	}
	if err := verifyCredential(ctx, h, selection, issued.Secret); err != nil {
		return err
	}
	ownership, err := json.Marshal(ownershipFor(selection, issued.Secret))
	if err != nil {
		return errors.New("Healthchecks verified credential ownership could not be encoded")
	}
	return credentialJournal.Commit(h, ownership)
}
