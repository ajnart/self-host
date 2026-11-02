package stash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ajnart/self-host/internal/helpers"
)

const recoveryHint = "Preserve the selected Stash instance, account, library paths and existing API key. Complete initial setup with authentication enabled and no library scans only after reviewing its ownership, then restore the existing key through the private stash.api_key input and the matching stash.account_username when needed. Retry self-host setup stash. Never regenerate a key to repair verification. Automatic bootstrap and key issuance are not implemented in this App slice."

const identityQuery = `query { systemStatus { status } configuration { general { username } } version { version hash } }`

type identity struct {
	SystemStatus *struct {
		Status string `json:"status"`
	} `json:"systemStatus"`
	Configuration *struct {
		General *struct {
			Username string `json:"username"`
		} `json:"general"`
	} `json:"configuration"`
	Version *struct {
		Version string `json:"version"`
		Hash    string `json:"hash"`
	} `json:"version"`
}

func verify(ctx context.Context, h *helpers.H) error {
	key := h.Secret(h.Key("api_key"))
	if key == "" {
		return &helpers.ManualError{Hint: recoveryHint}
	}
	h.Log.Secret(key)
	username := h.Secret(h.Key("account_username"))
	if username == "" {
		username = h.AdminUser()
	}
	if username == "" {
		return &helpers.ManualError{Hint: "Select the known existing Stash account through private stash.account_username input with its matching existing instance key, then retry self-host setup stash. No account or key will be created or replaced."}
	}
	anonymous, err := identityRequest(ctx, h, "")
	if err != nil {
		return err
	}
	if anonymous.Status != http.StatusUnauthorized {
		return &helpers.ManualError{Hint: "Stash's GraphQL endpoint did not reject anonymous access as required. Preserve its data and existing authentication; review the selected instance and its authentication boundary before retrying self-host setup stash. Setup will not reset an account, change paths or bypass authentication."}
	}
	response, err := identityRequest(ctx, h, key)
	if err != nil {
		return err
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		return &helpers.ManualError{Hint: "Stash rejected the saved instance key. Restore the matching existing key through private stash.api_key input, preserve the server's account and all other consumers, then retry self-host setup stash. Do not regenerate or clear the instance key."}
	}
	if response.Status != http.StatusOK {
		return fmt.Errorf("Stash identity verification returned HTTP %d; check the selected App and retry self-host setup stash", response.Status)
	}
	var envelope struct {
		Data   *identity       `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(response.Body, &envelope) != nil || envelope.Data == nil || len(envelope.Errors) != 0 {
		return errors.New("Stash did not return a successful identity query; check its supported GraphQL API and retry self-host setup stash")
	}
	data := envelope.Data
	if data.SystemStatus == nil || data.Configuration == nil || data.Configuration.General == nil || data.Version == nil || data.Version.Version == "" || data.Version.Hash == "" {
		return errors.New("Stash returned incomplete account, system or version data; retry self-host setup stash")
	}
	if data.SystemStatus.Status == "SETUP" {
		return &helpers.ManualError{Hint: recoveryHint}
	}
	if data.SystemStatus.Status == "NEEDS_MIGRATION" {
		return &helpers.ManualError{Hint: "Stash requires a database migration. Back up the selected instance and obtain explicit approval for its supported migration before retrying self-host setup stash. Keep the account, key and library paths unchanged."}
	}
	if data.SystemStatus.Status != "OK" {
		return errors.New("Stash did not confirm a ready database; check the selected instance and retry self-host setup stash")
	}
	if data.Configuration.General.Username != username {
		return &helpers.ManualError{Hint: "Stash's authenticated account does not match the selected account. Restore only the matching existing stash.account_username and stash.api_key through private input, then retry self-host setup stash. Do not rename the server's account or replace its key."}
	}
	return nil
}

func identityRequest(ctx context.Context, h *helpers.H, key string) (helpers.Response, error) {
	headers := map[string]string{}
	if key != "" {
		headers["ApiKey"] = key
	}
	response, err := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/graphql", NoRedirect: true,
		Header: headers, JSON: map[string]string{"query": identityQuery},
	})
	if err != nil {
		if ctx.Err() != nil {
			return helpers.Response{}, ctx.Err()
		}
		return helpers.Response{}, errors.New("Stash identity request failed; check its private Address and trusted TLS configuration, then retry self-host setup stash")
	}
	return response, nil
}
