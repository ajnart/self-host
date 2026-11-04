package anchor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const HomarrRecoveryHint = "Run self-host setup anchor, then self-host setup homarr. Restore only a known matching token and ownership JSON through the optional private inputs anchor.homarr_api_key and anchor.homarr_token_ownership using install --set-file and owner-only credential files. Empty inputs preserve saved values. Reinstall replaces Compose: first preserve and review existing authentication, mounts and ports and separately approve replacement. Anchor keeps one permanent token per account with no scopes or inventory, so self-host never replaces an existing or unrecorded token: revoke it yourself in Anchor Settings > API token only when you accept that every client using it stops, then retry Setup. The token can write the selected account's notes."

var homarrJournal = helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending"}

type tokenOwnership struct {
	AccountID   string `json:"accountId"`
	Email       string `json:"email"`
	Fingerprint string `json:"fingerprint"`
}

func validToken(token string) bool {
	decoded, err := hex.DecodeString(token)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == token
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + HomarrRecoveryHint}
}

func savedOwnership(h *helpers.H) (tokenOwnership, error) {
	raw := h.Secret(h.Key("homarr_token_ownership"))
	h.Log.Secret(raw)
	var ownership tokenOwnership
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ownership) != nil || ownership.AccountID == "" || !validEmail(ownership.Email) || len(ownership.Fingerprint) != 64 {
		return tokenOwnership{}, homarrManual("Anchor's saved Homarr token ownership is missing or incomplete.")
	}
	if _, err := hex.DecodeString(ownership.Fingerprint); err != nil {
		return tokenOwnership{}, homarrManual("Anchor's saved Homarr token ownership fingerprint is invalid.")
	}
	return ownership, nil
}

func discoverToken(ctx context.Context, h *helpers.H, session administratorSession) (string, error) {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/auth/api-token", NoRedirect: true, Header: map[string]string{"Authorization": "Bearer " + session.Token}})
	if err != nil || response.Status != http.StatusOK {
		return "", homarrManual("Anchor could not report the selected account's permanent token.")
	}
	var body struct {
		APIToken *string `json:"apiToken"`
	}
	decodeErr := response.Decode(&body)
	if body.APIToken != nil {
		h.Log.Secret(*body.APIToken)
	}
	if decodeErr != nil {
		return "", errors.New("Anchor returned an unreadable permanent-token response; no token was changed")
	}
	if body.APIToken == nil {
		return "", nil
	}
	if !validToken(*body.APIToken) {
		return "", homarrManual("Anchor returned an unexpected permanent-token format.")
	}
	return *body.APIToken, nil
}

func homarrTokenWorks(ctx context.Context, h *helpers.H) (bool, error) {
	_, pending, err := homarrJournal.Pending(h)
	if err != nil || pending {
		return false, err
	}
	token, recorded := h.Secret(h.Key("homarr_api_key")), h.Secret(h.Key("homarr_token_ownership"))
	h.Log.Secret(token, recorded)
	if token == "" && recorded == "" {
		return false, nil
	}
	if !validToken(token) {
		return false, homarrManual("Anchor's saved Homarr token is missing or malformed while its ownership is recorded.")
	}
	ownership, err := savedOwnership(h)
	if err != nil {
		return false, err
	}
	if ownership.Fingerprint != helpers.Fingerprint(token) {
		return false, homarrManual("Anchor's saved Homarr token and ownership do not match.")
	}
	session, err := selectedAdministrator(ctx, h)
	if err != nil {
		return false, err
	}
	if session.Identity.ID != ownership.AccountID || session.Identity.Email != ownership.Email {
		return false, homarrManual("Anchor's saved Homarr token belongs to a different account than the selected administrator.")
	}
	current, err := discoverToken(ctx, h, session)
	if err != nil {
		return false, err
	}
	if current != token {
		return false, homarrManual("Anchor's selected account no longer holds the saved Homarr token.")
	}
	return true, readNotes(ctx, h, token)
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	ok, err := homarrTokenWorks(ctx, h)
	if err != nil {
		return err
	}
	if !ok {
		return homarrManual("Anchor's Homarr token is missing or pending.")
	}
	return nil
}

func prepareHomarrToken(ctx context.Context, h *helpers.H) error {
	if ok, err := homarrTokenWorks(ctx, h); ok || err != nil {
		return err
	}
	session, err := selectedAdministrator(ctx, h)
	if err != nil {
		return err
	}
	current, err := discoverToken(ctx, h, session)
	if err != nil {
		return err
	}
	pending, exists, err := homarrJournal.Pending(h)
	if err != nil {
		return err
	}
	if exists {
		if pending.Issued == nil || current != pending.Issued.Secret {
			return homarrManual("An earlier Anchor token issuance is incomplete or no longer matches the account.")
		}
		return commitHomarrToken(h, session, current)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_token_ownership")) != "" || current != "" {
		return homarrManual("The selected Anchor account already has or records a permanent token that self-host did not issue.")
	}
	intent, err := json.Marshal(map[string]string{"accountId": session.Identity.ID, "email": session.Identity.Email})
	if err != nil {
		return errors.New("cannot record the Anchor token issuance intent")
	}
	if err := homarrJournal.Begin(h, intent); err != nil {
		return err
	}
	response, err := h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/auth/api-token/regenerate", NoRedirect: true, Header: map[string]string{"Authorization": "Bearer " + session.Token}})
	if err != nil {
		return fmt.Errorf("Anchor token issuance is uncertain; preserve the recorded intent and retry Setup: %w", err)
	}
	var issued struct {
		APIToken string `json:"apiToken"`
	}
	decodeErr := response.Decode(&issued)
	h.Log.Secret(issued.APIToken)
	if response.Status != http.StatusOK || decodeErr != nil || !validToken(issued.APIToken) {
		return errors.New("Anchor token issuance is uncertain; preserve the recorded intent, inspect Anchor Settings > API token and retry Setup")
	}
	if err := homarrJournal.Capture(h, helpers.CredentialEvidence{Secret: issued.APIToken}); err != nil {
		return err
	}
	confirmed, err := discoverToken(ctx, h, session)
	if err != nil {
		return err
	}
	if confirmed != issued.APIToken {
		return homarrManual("Anchor did not confirm the issued permanent token for the selected account.")
	}
	return commitHomarrToken(h, session, issued.APIToken)
}

func commitHomarrToken(h *helpers.H, session administratorSession, token string) error {
	ownership, err := json.Marshal(tokenOwnership{AccountID: session.Identity.ID, Email: session.Identity.Email, Fingerprint: helpers.Fingerprint(token)})
	if err != nil {
		return errors.New("cannot record the Anchor token ownership")
	}
	return homarrJournal.Commit(h, ownership)
}

func prepareHomarr(ctx context.Context, h *helpers.H) error {
	if h.Peer("homarr") == nil {
		h.Skip("homarr-token", "Homarr is not installed; no Anchor token is needed")
		return nil
	}
	return h.Step(ctx, "homarr-token", "prepare Anchor note statistics access", helpers.StepSpec{
		Check:      func(ctx context.Context) (bool, error) { return homarrTokenWorks(ctx, h) },
		Do:         func(ctx context.Context) error { return prepareHomarrToken(ctx, h) },
		Hint:       HomarrRecoveryHint,
		Credential: "admin",
	})
}
