package immich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const homarrKeyName = "self-host Homarr statistics"

func homarrPermissions() []string { return []string{"user.read", "server.statistics"} }

type apiKey struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

type homarrKeyEvidence struct {
	Secret   string `json:"secret"`
	Response string `json:"response,omitempty"`
	Status   int    `json:"status"`
}

func issuedHomarrSecret(body []byte) string {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	for decoder.More() {
		field, err := decoder.Token()
		if err != nil {
			return ""
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return ""
		}
		if field == "secret" {
			var secret string
			if json.Unmarshal(value, &secret) == nil && strings.TrimSpace(secret) != "" {
				return secret
			}
		}
	}
	return ""
}

func validateHomarrKey(key apiKey) error {
	permissions := homarrPermissions()
	if strings.TrimSpace(key.ID) == "" || len(key.Permissions) != len(permissions) {
		return errors.New("Immich Homarr key must have an ID and grant only user.read and server.statistics")
	}
	for _, permission := range permissions {
		if !slices.Contains(key.Permissions, permission) {
			return errors.New("Immich Homarr key is missing its read-only statistics permissions")
		}
	}
	return nil
}

func homarrKeyRecovery(h *helpers.H) string {
	return "Open " + h.URL + " and review user settings > API Keys. Preserve pending evidence and saved credentials in a private recovery copy. Reconcile the remote issuance and independently verify the Project administrator, key ID and exact user.read/server.statistics permissions. Restore the confirmed matching IMMICH_HOMARR_API_KEY and IMMICH_HOMARR_API_KEY_ID through the Project secret boundary. Only after explicitly approving that reconciliation, remove IMMICH_HOMARR_API_KEY_PENDING in the same private edit, retaining the original evidence privately, then run self-host setup immich. Never clear a hold without confirmed credentials to trigger issuance, or delete/adopt a key solely by its name."
}

func manualHomarrKey(h *helpers.H, reason string) error {
	h.Info(reason + " " + homarrKeyRecovery(h))
	return helpers.ErrManual
}

func inspectHomarrKey(ctx context.Context, h *helpers.H, key string) (apiKey, bool, error) {
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/api-keys/me", NoRedirect: true, Header: map[string]string{"x-api-key": key}})
	if err != nil {
		return apiKey{}, false, err
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return apiKey{}, false, nil
	}
	if resp.Status != http.StatusOK {
		return apiKey{}, false, fmt.Errorf("Immich key verification returned HTTP %d", resp.Status)
	}
	var current apiKey
	if err := resp.Decode(&current); err != nil {
		return apiKey{}, false, err
	}
	if err := validateHomarrKey(current); err != nil {
		return apiKey{}, false, err
	}
	return current, true, nil
}

func recoverPendingHomarrKey(ctx context.Context, h *helpers.H, pending string) (bool, error) {
	h.Log.Secret(pending)
	var evidence homarrKeyEvidence
	if json.Unmarshal([]byte(pending), &evidence) != nil || strings.TrimSpace(evidence.Secret) == "" {
		return false, manualHomarrKey(h, "Immich key issuance has unresolved pending evidence; no replacement was issued.")
	}
	h.Log.Secret(evidence.Secret)
	if evidence.Status != http.StatusCreated {
		return false, manualHomarrKey(h, "Immich did not confirm successful issuance; its returned secret and response were preserved for review.")
	}
	if active := h.Secret(h.Key("homarr_api_key")); active != "" && active != evidence.Secret {
		return false, manualHomarrKey(h, "A pending Immich key differs from the saved credential; both records were preserved.")
	}
	current, accepted, err := inspectHomarrKey(ctx, h, evidence.Secret)
	if err != nil {
		return false, err
	}
	if !accepted || current.Name != homarrKeyName {
		return false, manualHomarrKey(h, "The pending Immich key was not accepted with its expected creation name; its evidence was preserved.")
	}
	id := h.Secret(h.Key("homarr_api_key_id"))
	if id != "" && id != current.ID {
		return false, manualHomarrKey(h, "The pending Immich key does not match the saved ID; neither record was changed.")
	}
	var created struct {
		ID     string `json:"id"`
		APIKey *struct {
			ID string `json:"id"`
		} `json:"apiKey"`
	}
	if json.Unmarshal([]byte(evidence.Response), &created) != nil {
		return false, manualHomarrKey(h, "The pending Immich issuance IDs cannot be read safely; its response and secret were preserved.")
	}
	if created.ID != "" && created.ID != current.ID || created.APIKey != nil && created.APIKey.ID != "" && created.APIKey.ID != current.ID {
		return false, manualHomarrKey(h, "The pending Immich key does not match its issuance IDs; its evidence was preserved.")
	}
	if err := verifyHomarrCredential(ctx, h, evidence.Secret); err != nil {
		return false, err
	}
	if err := h.SetSecret(h.Key("homarr_api_key_id"), current.ID); err != nil {
		return false, h.Log.RedactError(err)
	}
	if err := h.SetSecret(h.Key("homarr_api_key"), evidence.Secret); err != nil {
		return false, h.Log.RedactError(err)
	}
	if err := h.SetSecret(h.Key("homarr_api_key_pending"), ""); err != nil {
		return false, h.Log.RedactError(err)
	}
	return true, nil
}

func homarrKeyWorks(ctx context.Context, h *helpers.H) (bool, error) {
	if pending := h.Secret(h.Key("homarr_api_key_pending")); pending != "" {
		return recoverPendingHomarrKey(ctx, h, pending)
	}
	key := h.Secret(h.Key("homarr_api_key"))
	if key == "" {
		return false, nil
	}
	current, accepted, err := inspectHomarrKey(ctx, h, key)
	if err != nil || !accepted {
		return false, err
	}
	if err := VerifyHomarr(ctx, h); err != nil {
		return false, err
	}
	id := h.Secret(h.Key("homarr_api_key_id"))
	if id != "" && id != current.ID {
		return false, manualHomarrKey(h, "The saved Immich Homarr key does not match its recorded ID; neither key was changed.")
	}
	if id == "" {
		if err := h.SetSecret(h.Key("homarr_api_key_id"), current.ID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	key := h.Secret(h.Key("homarr_api_key"))
	if key == "" {
		return errors.New("Immich Homarr credential is missing; run self-host setup immich")
	}
	return verifyHomarrCredential(ctx, h, key)
}

func verifyHomarrCredential(ctx context.Context, h *helpers.H, key string) error {
	header := map[string]string{"x-api-key": key}
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/users/me", NoRedirect: true, Header: header})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Immich Homarr credential returned HTTP %d; run self-host setup immich", resp.Status)
	}
	var user struct {
		Email   string `json:"email"`
		IsAdmin bool   `json:"isAdmin"`
	}
	if err := resp.Decode(&user); err != nil {
		return err
	}
	if user.Email != email || !user.IsAdmin {
		return errors.New("Immich Homarr credential does not belong to the Project administrator")
	}
	resp, err = h.HTTP(ctx, helpers.Request{Path: "/api/server/statistics", NoRedirect: true, Header: header})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Immich statistics returned HTTP %d; run self-host setup immich", resp.Status)
	}
	var statistics struct {
		Photos *int64 `json:"photos"`
		Videos *int64 `json:"videos"`
		Usage  *int64 `json:"usage"`
	}
	if resp.Decode(&statistics) != nil || statistics.Photos == nil || statistics.Videos == nil || statistics.Usage == nil || *statistics.Photos < 0 || *statistics.Videos < 0 || *statistics.Usage < 0 {
		return errors.New("Immich returned incomplete server statistics")
	}
	resp, err = h.HTTP(ctx, helpers.Request{Path: "/api/users", NoRedirect: true, Header: header})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Immich users returned HTTP %d", resp.Status)
	}
	var users []struct{ ID string }
	if resp.Decode(&users) != nil || len(users) == 0 {
		return errors.New("Immich did not return its administrator in the statistics user list")
	}
	return nil
}

func createHomarrKey(ctx context.Context, h *helpers.H) error {
	if ok, err := homarrKeyWorks(ctx, h); err != nil || ok {
		return err
	}
	if h.Secret(h.Key("homarr_api_key_id")) != "" || h.Secret(h.Key("homarr_api_key")) != "" {
		return manualHomarrKey(h, "The saved Immich Homarr secret is missing or rejected; its existing credential record was preserved and no replacement was issued.")
	}
	resp, err := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/auth/login", NoRedirect: true,
		JSON: map[string]string{"email": email, "password": h.AdminPassword()},
	})
	if err != nil {
		return err
	}
	var login struct {
		AccessToken string `json:"accessToken"`
		UserEmail   string `json:"userEmail"`
		IsAdmin     bool   `json:"isAdmin"`
	}
	if !resp.OK() || resp.Decode(&login) != nil || login.AccessToken == "" || login.UserEmail != email || !login.IsAdmin {
		return errors.New("cannot sign in to Immich to prepare its Homarr key")
	}
	h.Log.Secret(login.AccessToken)
	header := map[string]string{"Authorization": "Bearer " + login.AccessToken}
	resp, err = h.HTTP(ctx, helpers.Request{Path: "/api/api-keys", NoRedirect: true, Header: header})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("Immich API-key discovery returned HTTP %d", resp.Status)
	}
	var keys []apiKey
	if err := resp.Decode(&keys); err != nil {
		return err
	}
	if keys == nil {
		return errors.New("Immich did not return its API-key list; retry self-host setup immich")
	}
	for _, key := range keys {
		if key.Name == homarrKeyName {
			return manualHomarrKey(h, "An Immich key already uses the Homarr statistics name, but no saved secret was accepted; the existing key was preserved.")
		}
		if strings.TrimSpace(key.ID) == "" || strings.TrimSpace(key.Name) == "" {
			return errors.New("Immich returned incomplete API-key discovery; retry self-host setup immich")
		}
	}
	if err := h.SetSecret(h.Key("homarr_api_key_pending"), `{"secret":""}`); err != nil {
		return h.Log.RedactError(err)
	}
	resp, err = h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/api-keys", NoRedirect: true, Header: header,
		JSON: map[string]any{"name": homarrKeyName, "permissions": homarrPermissions()},
	})
	if err != nil {
		return err
	}
	evidence := homarrKeyEvidence{Secret: issuedHomarrSecret(resp.Body), Response: string(resp.Body), Status: resp.Status}
	h.Log.Secret(evidence.Secret, evidence.Response)
	pending, err := json.Marshal(evidence)
	if err != nil {
		return h.Log.RedactError(err)
	}
	if err := h.SetSecret(h.Key("homarr_api_key_pending"), string(pending)); err != nil {
		return h.Log.RedactError(err)
	}
	_, err = recoverPendingHomarrKey(ctx, h, string(pending))
	return err
}
