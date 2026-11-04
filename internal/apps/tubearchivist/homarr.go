package tubearchivist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const homarrRecoveryHint = "Restore the matching private Project credentials for the Tube Archivist administrator, review the account in Tube Archivist settings, then run self-host setup tubearchivist and self-host setup homarr. Never delete cache/db.sqlite3, reset passwords, or rotate or revoke tokens to continue."

var homarrIssuance = helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending"}

type tokenOwnership struct {
	AccountID   string `json:"accountId"`
	Fingerprint string `json:"fingerprint"`
}

func homarrKeyNeeded(h *helpers.H) bool {
	return h.Peer("homarr") != nil || h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_token_ownership")) != "" || h.Secret(h.Key("homarr_token_pending")) != ""
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + homarrRecoveryHint}
}

func homarrKeyOwned(h *helpers.H) (key, owner string, err error) {
	inconsistent := homarrManual("Tube Archivist's owned Homarr token or its account checkpoint is missing or inconsistent.")
	if _, pending, err := homarrIssuance.Pending(h); err != nil || pending {
		return "", "", inconsistent
	}
	key = h.Secret(h.Key("homarr_api_key"))
	var ownership tokenOwnership
	decoder := json.NewDecoder(strings.NewReader(h.Secret(h.Key("homarr_token_ownership"))))
	decoder.DisallowUnknownFields()
	if key == "" || decoder.Decode(&ownership) != nil || ownership.Fingerprint != helpers.Fingerprint(key) {
		return "", "", inconsistent
	}
	if id, err := strconv.ParseInt(ownership.AccountID, 10, 64); err != nil || id <= 0 {
		return "", "", inconsistent
	}
	return key, ownership.AccountID, nil
}

func tokenIdentity(ctx context.Context, h *helpers.H, key string) (id string, err error) {
	h.Log.Secret(key)
	header := map[string]string{"Authorization": "Token " + key}
	account, err := h.HTTP(ctx, helpers.Request{Path: "/api/user/account/", NoRedirect: true, Header: header})
	if err != nil {
		return "", h.Log.RedactError(fmt.Errorf("Tube Archivist token account request failed: %w", err))
	}
	if account.Status == http.StatusUnauthorized || account.Status == http.StatusForbidden {
		return "", homarrManual("Tube Archivist rejected the saved Homarr token.")
	}
	if account.Status != http.StatusOK {
		return "", fmt.Errorf("Tube Archivist token account request returned HTTP %d; run self-host setup tubearchivist", account.Status)
	}
	var identity struct {
		ID *int64 `json:"id"`
	}
	if account.Decode(&identity) != nil || identity.ID == nil || *identity.ID <= 0 {
		return "", homarrManual("Tube Archivist did not identify the saved Homarr token's account.")
	}
	ping, err := h.HTTP(ctx, helpers.Request{Path: "/api/ping/", NoRedirect: true, Header: header})
	if err != nil {
		return "", h.Log.RedactError(fmt.Errorf("Tube Archivist token ping request failed: %w", err))
	}
	if ping.Status != http.StatusOK {
		return "", fmt.Errorf("Tube Archivist token ping returned HTTP %d; run self-host setup tubearchivist", ping.Status)
	}
	var state struct {
		Response string `json:"response"`
		User     *int64 `json:"user"`
		Version  string `json:"version"`
	}
	if ping.Decode(&state) != nil || state.Response != "pong" || state.User == nil || *state.User != *identity.ID {
		return "", homarrManual("Tube Archivist's token ping did not match its account.")
	}
	if !helpers.VersionCompatible(state.Version, "0.6.0") {
		return "", homarrManual("Tube Archivist did not report a compatible v0.6.0-or-newer 0.x API.")
	}
	return strconv.FormatInt(*identity.ID, 10), nil
}

func homarrKeyWorks(ctx context.Context, h *helpers.H, accountID string) (bool, error) {
	if h.Secret(h.Key("homarr_api_key")) == "" {
		return false, nil
	}
	key, owner, err := homarrKeyOwned(h)
	if err != nil {
		return false, err
	}
	if owner != accountID {
		return false, homarrManual("The saved Homarr token belongs to a different Tube Archivist account.")
	}
	id, err := tokenIdentity(ctx, h, key)
	if err != nil {
		return false, err
	}
	if id != owner {
		return false, homarrManual("The saved Homarr token resolves to a different Tube Archivist account.")
	}
	return true, nil
}

func setupHomarrKey(ctx context.Context, h *helpers.H, session *helpers.Session, accountID string) error {
	if !homarrKeyNeeded(h) {
		return nil
	}
	return h.Step(ctx, "homarr-api-key", "prepare the owned Tube Archivist token for Homarr", helpers.StepSpec{
		Check: func(ctx context.Context) (bool, error) {
			return homarrKeyWorks(ctx, h, accountID)
		},
		Do: func(ctx context.Context) error {
			if h.Secret(h.Key("homarr_api_key")) != "" {
				_, err := homarrKeyWorks(ctx, h, accountID)
				return err
			}
			if _, pending, err := homarrIssuance.Pending(h); err != nil || pending || h.Secret(h.Key("homarr_token_ownership")) != "" {
				return homarrManual("Tube Archivist token creation was already attempted without a saved token.")
			}
			intent, err := json.Marshal(map[string]string{"accountId": accountID})
			if err != nil {
				return err
			}
			if err := homarrIssuance.Begin(h, intent); err != nil {
				return err
			}
			resp, err := session.HTTP(ctx, helpers.Request{Path: "/api/appsettings/token/", NoRedirect: true})
			if err != nil {
				return homarrManual("Tube Archivist token creation was not confirmed.")
			}
			var created struct {
				Token string `json:"token"`
			}
			if resp.Status != http.StatusOK || resp.Decode(&created) != nil || len(created.Token) < 16 || strings.ContainsAny(created.Token, " \t\r\n") {
				return homarrManual("Tube Archivist token creation was not confirmed.")
			}
			if err := homarrIssuance.Capture(h, helpers.CredentialEvidence{Secret: created.Token}); err != nil {
				return err
			}
			ownership, err := json.Marshal(tokenOwnership{AccountID: accountID, Fingerprint: helpers.Fingerprint(created.Token)})
			if err != nil {
				return err
			}
			return homarrIssuance.Commit(h, ownership)
		},
		Hint:       homarrRecoveryHint,
		Credential: "admin",
	})
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	key, owner, err := homarrKeyOwned(h)
	if err != nil {
		return err
	}
	id, err := tokenIdentity(ctx, h, key)
	if err != nil {
		return err
	}
	if id != owner {
		return homarrManual("The saved Homarr token resolves to a different Tube Archivist account.")
	}
	return nil
}

func HomarrAccountID(h *helpers.H) (int64, error) {
	_, owner, err := homarrKeyOwned(h)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(owner, 10, 64)
}
