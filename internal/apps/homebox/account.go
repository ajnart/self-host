package homebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const accountRecovery = "Homebox Setup does not register accounts or replace credentials. For a genuinely new inventory only, explicitly approve temporarily enabling registration in the existing Compose file, register the intended Admin email with the password from self-host credentials, and disable registration again. For an existing inventory, restore its known login or supply the matching homebox.account_identity and homebox.api_key through private --set-file input after reviewing and separately approving any Compose replacement. Preserve items, attachments, locations, account settings and the existing pepper, then retry self-host setup homebox."

var accountIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type accountIdentity struct {
	AccountID string `json:"accountId"`
	Email     string `json:"email"`
	GroupID   string `json:"groupId"`
}

func validID(value string) bool {
	return accountIDPattern.MatchString(value) && value != "00000000-0000-0000-0000-000000000000"
}

func savedIdentity(h *helpers.H) (accountIdentity, bool, error) {
	raw := h.Secret(h.Key("account_identity"))
	if raw == "" {
		return accountIdentity{}, false, nil
	}
	h.Log.Secret(raw)
	var identity accountIdentity
	if json.Unmarshal([]byte(raw), &identity) != nil || !validID(identity.AccountID) || !validID(identity.GroupID) ||
		strings.TrimSpace(identity.Email) == "" || identity.Email != strings.ToLower(strings.TrimSpace(identity.Email)) {
		return identity, false, &helpers.ManualError{Hint: "Restore the independently verified Homebox account_identity with accountId, email and groupId through private input; the saved record was not replaced. " + accountRecovery}
	}
	return identity, true, nil
}

func authorization(ctx context.Context, h *helpers.H, identity accountIdentity, saved bool) (string, error) {
	if key := h.Secret(h.Key("api_key")); key != "" {
		h.Log.Secret(key)
		if !saved {
			return "", &helpers.ManualError{Hint: "An existing Homebox API key requires its independently verified account_identity before it can select an inventory. " + accountRecovery}
		}
		return "Bearer " + key, nil
	}
	email := identity.Email
	if !saved {
		email = (App{}).AdminEmail(h.AdminUser())
	}
	password := h.AdminPassword()
	if email == "" || password == "" {
		return "", &helpers.ManualError{Hint: "The known Homebox login is unavailable; no account was registered. " + accountRecovery}
	}
	h.Log.Secret(password)
	return h.Remember("homebox-login:"+email, func() (string, error) {
		response, err := h.HTTP(ctx, helpers.Request{
			Method: http.MethodPost, Path: "/api/v1/users/login", NoRedirect: true,
			Form: url.Values{"username": {email}, "password": {password}},
		})
		if err != nil {
			return "", h.Log.RedactError(fmt.Errorf("Homebox login request failed: %w", err))
		}
		if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
			return "", &helpers.ManualError{Hint: "Homebox did not accept the existing login; registration, password reset and group creation were not attempted. " + accountRecovery}
		}
		if response.Status != http.StatusOK {
			return "", fmt.Errorf("Homebox login returned HTTP %d", response.Status)
		}
		var login struct {
			Token string `json:"token"`
		}
		if response.Decode(&login) != nil || !strings.HasPrefix(login.Token, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(login.Token, "Bearer ")) == "" {
			return "", errors.New("Homebox did not return its authenticated login token")
		}
		h.Log.Secret(login.Token, strings.TrimPrefix(login.Token, "Bearer "))
		return login.Token, nil
	})
}

func readAccountData(ctx context.Context, h *helpers.H, token, path string, value any) error {
	response, err := h.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true, Header: map[string]string{"Authorization": token}})
	if err != nil {
		return h.Log.RedactError(fmt.Errorf("Homebox authenticated request failed: %w", err))
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		return &helpers.ManualError{Hint: "Homebox rejected the selected account credential or inventory access. No replacement credential or group was created. " + accountRecovery}
	}
	if response.Status != http.StatusOK {
		return fmt.Errorf("Homebox authenticated read returned HTTP %d", response.Status)
	}
	if response.Decode(value) != nil {
		return errors.New("Homebox returned malformed authenticated account or inventory data")
	}
	return nil
}

func requirePepper(h *helpers.H) error {
	if h.Secrets == nil || len(h.Secret(h.Key("api_key_pepper"))) < 32 {
		return &helpers.ManualError{Hint: "Homebox is held before secret generation or startup. Restore the original pepper of at least 32 bytes with private homebox.api_key_pepper input through --set-file and an owner-only credential file, after reviewing and separately approving any Compose replacement. For an explicitly approved new inventory only, provision one strong private pepper and keep it with the data. Do not generate a replacement for retained data: it invalidates every existing API key. Retry self-host install homebox after that recovery; self-host setup homebox does not provision the pepper."}
	}
	return nil
}

func verifyAccount(ctx context.Context, h *helpers.H, rememberIdentity bool) error {
	if err := requirePepper(h); err != nil {
		return err
	}
	identity, saved, err := savedIdentity(h)
	if err != nil {
		return err
	}
	if !saved && !rememberIdentity {
		return &helpers.ManualError{Hint: "Homebox has no verified account and inventory selection; run self-host setup homebox to verify the existing login without registering an account. " + accountRecovery}
	}
	token, err := authorization(ctx, h, identity, saved)
	if err != nil {
		return err
	}
	var self struct {
		Item *struct {
			ID             string   `json:"id"`
			Email          string   `json:"email"`
			DefaultGroupID string   `json:"defaultGroupId"`
			GroupIDs       []string `json:"groupIds"`
		} `json:"item"`
	}
	if err := readAccountData(ctx, h, token, "/api/v1/users/self", &self); err != nil {
		return err
	}
	if self.Item == nil || !validID(self.Item.ID) || !validID(self.Item.DefaultGroupID) || !slices.Contains(self.Item.GroupIDs, self.Item.DefaultGroupID) {
		return errors.New("Homebox did not return an authenticated account with its default inventory membership")
	}
	expectedEmail := (App{}).AdminEmail(h.AdminUser())
	if saved {
		expectedEmail = identity.Email
	}
	if self.Item.Email != expectedEmail || saved && (self.Item.ID != identity.AccountID || self.Item.DefaultGroupID != identity.GroupID) {
		return &helpers.ManualError{Hint: "The Homebox credential owner or default inventory differs from the selected identity. Preserve both inventories and restore the known matching login and account_identity; do not change the account's default group to make Setup pass. " + accountRecovery}
	}
	var group struct {
		ID       string `json:"id"`
		Currency string `json:"currency"`
	}
	if err := readAccountData(ctx, h, token, "/api/v1/groups", &group); err != nil {
		return err
	}
	if group.ID != self.Item.DefaultGroupID || strings.TrimSpace(group.Currency) == "" {
		return &helpers.ManualError{Hint: "Homebox did not confirm the selected default inventory and its currency. Preserve existing group and account settings and review the intended inventory before retrying self-host setup homebox."}
	}
	var statistics struct {
		Items        *int64   `json:"totalItems"`
		Locations    *int64   `json:"totalLocations"`
		Tags         *int64   `json:"totalTags"`
		Labels       *int64   `json:"totalLabels"`
		WithWarranty *int64   `json:"totalWithWarranty"`
		Users        *int64   `json:"totalUsers"`
		Value        *float64 `json:"totalItemPrice"`
	}
	if err := readAccountData(ctx, h, token, "/api/v1/groups/statistics", &statistics); err != nil {
		return err
	}
	for _, count := range []*int64{statistics.Items, statistics.Locations, statistics.WithWarranty, statistics.Users} {
		if count == nil || *count < 0 {
			return errors.New("Homebox returned incomplete inventory statistics")
		}
	}
	if statistics.Tags == nil && statistics.Labels == nil || statistics.Tags != nil && *statistics.Tags < 0 ||
		statistics.Labels != nil && *statistics.Labels < 0 || statistics.Value == nil || *statistics.Value < 0 ||
		*statistics.Users == 0 || *statistics.WithWarranty > *statistics.Items {
		return errors.New("Homebox returned inconsistent inventory statistics")
	}
	if saved {
		return nil
	}
	identity = accountIdentity{AccountID: self.Item.ID, Email: self.Item.Email, GroupID: group.ID}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return h.SetSecret(h.Key("account_identity"), string(raw))
}
