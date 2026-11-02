package immich

import (
	"context"
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

func manualHomarrKey(h *helpers.H, reason string) error {
	h.Info(reason + " Open " + h.URL + " and review user settings > API Keys. Restore the matching IMMICH_HOMARR_API_KEY and IMMICH_HOMARR_API_KEY_ID through the Project secret boundary, then run self-host setup immich. Delete a key only after confirming it belongs to this Project, never solely by its name.")
	return helpers.ErrManual
}

func homarrKeyWorks(ctx context.Context, h *helpers.H) (bool, error) {
	key := h.Secret(h.Key("homarr_api_key"))
	if key == "" {
		return false, nil
	}
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/api-keys/me", NoRedirect: true, Header: map[string]string{"x-api-key": key}})
	if err != nil {
		return false, err
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		return false, nil
	}
	if resp.Status != http.StatusOK {
		return false, fmt.Errorf("Immich key verification returned HTTP %d", resp.Status)
	}
	var current apiKey
	if err := resp.Decode(&current); err != nil {
		return false, err
	}
	if err := validateHomarrKey(current); err != nil {
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
	resp, err = h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/api-keys", NoRedirect: true, Header: header,
		JSON: map[string]any{"name": homarrKeyName, "permissions": homarrPermissions()},
	})
	if err != nil {
		return err
	}
	if resp.Status != http.StatusCreated {
		return fmt.Errorf("Immich API-key creation returned HTTP %d", resp.Status)
	}
	var created struct {
		apiKey
		Secret string  `json:"secret"`
		APIKey *apiKey `json:"apiKey"`
	}
	if resp.Decode(&created) != nil || strings.TrimSpace(created.Secret) == "" {
		return errors.New("Immich did not return the newly created key; inspect its API keys before retrying")
	}
	metadata := created.apiKey
	if metadata.ID == "" && created.APIKey != nil {
		metadata = *created.APIKey
	}
	if err := validateHomarrKey(metadata); err != nil {
		return err
	}
	if metadata.Name != homarrKeyName {
		return errors.New("Immich returned an unexpected new API-key name; inspect its API keys before retrying")
	}
	if created.APIKey != nil {
		if err := validateHomarrKey(*created.APIKey); err != nil {
			return err
		}
		if created.APIKey.ID != metadata.ID || created.APIKey.Name != metadata.Name {
			return errors.New("Immich returned inconsistent new API-key metadata; inspect its API keys before retrying")
		}
	}
	if err := h.SetSecret(h.Key("homarr_api_key"), created.Secret); err != nil {
		return err
	}
	return h.SetSecret(h.Key("homarr_api_key_id"), metadata.ID)
}
