package karakeep

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const homarrKeyName = "self-host Homarr statistics"

const HomarrRecoveryHint = "Run self-host setup karakeep, then self-host setup homarr. Preserve the private Karakeep key, its ownership and issuance journal. Restore only the independently confirmed matching key/account/resource ID/keyId with exactly users:read through the Project's private credential boundary. Lost or rejected credentials, name collisions and uncertain issuance remain Manual: do not clear a journal, regenerate, revoke, adopt a same-name key or issue a replacement to bypass the hold. Review only the recorded credential in Karakeep Settings > API Keys. Reinstall replaces Compose, so preserve and review existing definitions before separately approving install --set-file recovery. Empty private recovery inputs preserve saved records."

var homarrIssuance = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending",
}

type homarrKey struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	KeyID  string   `json:"keyId"`
	Scopes []string `json:"scopes"`
}

type homarrOwnership struct {
	homarrKey
	AccountID   string `json:"accountId"`
	Fingerprint string `json:"fingerprint"`
}

type homarrIntent struct {
	AccountID string   `json:"accountId"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	BeforeIDs []string `json:"beforeIds"`
	BeforeKey []string `json:"beforeKeyIds"`
}

type homarrEvidence struct {
	Version   int       `json:"version"`
	Status    int       `json:"status"`
	Created   homarrKey `json:"created"`
	Complete  bool      `json:"complete"`
	Rejected  bool      `json:"rejected"`
	Uncertain bool      `json:"uncertain"`
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + HomarrRecoveryHint}
}

func homarrKeyID(key string) (string, bool) {
	parts := strings.Split(key, "_")
	if len(parts) != 3 || parts[0] != "ak2" || len(parts[1]) != 20 || len(parts[2]) != 32 {
		return "", false
	}
	for _, part := range parts[1:] {
		if _, err := hex.DecodeString(part); err != nil {
			return "", false
		}
	}
	return parts[1], true
}

func homarrScopes(scopes []string) bool {
	return slices.Equal(scopes, []string{"users:read"})
}

func readHomarrOwnership(h *helpers.H) (homarrOwnership, error) {
	key := h.Secret(h.Key("homarr_api_key"))
	raw := h.Secret(h.Key("homarr_key_ownership"))
	h.Log.Secret(key, raw)
	var ownership homarrOwnership
	keyID, valid := homarrKeyID(key)
	if json.Unmarshal([]byte(raw), &ownership) != nil || !valid || strings.TrimSpace(ownership.ID) == "" || ownership.ID != strings.TrimSpace(ownership.ID) || ownership.KeyID != keyID || ownership.Name != homarrKeyName || !homarrScopes(ownership.Scopes) || ownership.AccountID == "" || ownership.AccountID != h.Secret(h.Key("account_id")) || ownership.Fingerprint != helpers.Fingerprint(key) {
		return ownership, homarrManual("Karakeep's saved statistics credential and ownership do not form the recorded read-only account tuple.")
	}
	return ownership, nil
}

func HomarrAccountID(h *helpers.H) (string, error) {
	if _, exists, err := homarrIssuance.Pending(h); err != nil {
		return "", err
	} else if exists {
		return "", homarrManual("Karakeep has unresolved key issuance evidence.")
	}
	ownership, err := readHomarrOwnership(h)
	return ownership.AccountID, err
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if _, err := HomarrAccountID(h); err != nil {
		return err
	}
	if h.Secret(h.Key("admin_email")) == "" {
		return homarrManual("Karakeep has no recorded administrator email.")
	}
	_, err := withOwnerSession(ctx, h, func(ctx context.Context, session *helpers.Session, owner ownerIdentity, _ string) error {
		inventory, err := homarrInventory(ctx, session)
		if err != nil {
			return err
		}
		ownership, err := readHomarrOwnership(h)
		if err != nil {
			return err
		}
		return verifyHomarrTuple(ctx, h, owner, inventory, ownership, h.Secret(h.Key("homarr_api_key")))
	})
	return err
}

func verifyHomarrTuple(ctx context.Context, h *helpers.H, owner ownerIdentity, inventory []homarrKey, ownership homarrOwnership, key string) error {
	keyID, valid := homarrKeyID(key)
	if !valid || ownership.AccountID != owner.ID || ownership.AccountID != h.Secret(h.Key("account_id")) || ownership.KeyID != keyID || ownership.Name != homarrKeyName || !homarrScopes(ownership.Scopes) || ownership.Fingerprint != helpers.Fingerprint(key) {
		return homarrManual("Karakeep's statistics key does not match its intended account and read-only ownership.")
	}
	found := false
	for _, current := range inventory {
		if current.ID == ownership.ID {
			found = current.KeyID == ownership.KeyID && current.Name == ownership.Name && homarrScopes(current.Scopes)
		}
	}
	if !found {
		return homarrManual("The recorded Karakeep statistics key is missing or its metadata changed; existing records were preserved.")
	}
	response, err := h.HTTP(ctx, helpers.Request{
		Path: "/api/v1/users/me", NoRedirect: true, Header: map[string]string{"Authorization": "Bearer " + key},
	})
	if err != nil {
		return homarrManual("Karakeep statistics-key identity could not be verified over the selected trusted connection.")
	}
	var profile struct {
		ID        string          `json:"id"`
		Email     string          `json:"email"`
		LocalUser *bool           `json:"localUser"`
		Error     json.RawMessage `json:"error"`
	}
	if response.Status != http.StatusOK || response.Decode(&profile) != nil || hasBodyError(profile.Error) || profile.ID != owner.ID || !strings.EqualFold(profile.Email, owner.Email) || profile.LocalUser == nil || !*profile.LocalUser {
		return homarrManual("Karakeep rejected the statistics key or returned a different account; no replacement was issued.")
	}
	return nil
}

func homarrInventory(ctx context.Context, session *helpers.Session) ([]homarrKey, error) {
	response, err := session.HTTP(ctx, helpers.Request{Path: "/api/trpc/apiKeys.list", NoRedirect: true})
	if err != nil {
		return nil, homarrManual("Karakeep's authenticated key inventory could not be read.")
	}
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result *struct {
			Data *struct {
				JSON *struct {
					Keys *[]homarrKey `json:"keys"`
				} `json:"json"`
			} `json:"data"`
		} `json:"result"`
	}
	if response.Status != http.StatusOK || response.Decode(&envelope) != nil || hasBodyError(envelope.Error) || envelope.Result == nil || envelope.Result.Data == nil || envelope.Result.Data.JSON == nil || envelope.Result.Data.JSON.Keys == nil {
		return nil, homarrManual("Karakeep did not return a complete session-owned API-key inventory.")
	}
	inventory := *envelope.Result.Data.JSON.Keys
	ids, keyIDs := map[string]bool{}, map[string]bool{}
	for _, key := range inventory {
		if strings.TrimSpace(key.ID) == "" || key.ID != strings.TrimSpace(key.ID) || key.Name == "" || strings.TrimSpace(key.KeyID) == "" || key.KeyID != strings.TrimSpace(key.KeyID) || len(key.Scopes) == 0 || ids[key.ID] || keyIDs[key.KeyID] {
			return nil, homarrManual("Karakeep's key inventory is incomplete or ambiguous.")
		}
		for _, scope := range key.Scopes {
			if scope == "" {
				return nil, homarrManual("Karakeep's key inventory has incomplete scope metadata.")
			}
		}
		ids[key.ID], keyIDs[key.KeyID] = true, true
	}
	return inventory, nil
}

func ensureHomarrKey(ctx context.Context, h *helpers.H) error {
	pending, exists, err := homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	if h.Secret(h.Key("account_id")) == "" || h.Secret(h.Key("admin_email")) == "" {
		return homarrManual("Verify and record the intended Karakeep administrator before preparing its key.")
	}
	_, err = withOwnerSession(ctx, h, func(ctx context.Context, session *helpers.Session, owner ownerIdentity, origin string) error {
		inventory, err := homarrInventory(ctx, session)
		if err != nil {
			return err
		}
		if exists {
			return completeHomarrIssuance(ctx, h, owner, inventory, pending)
		}
		if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_key_ownership")) != "" {
			return homarrManual("Existing Karakeep key records prevent another issuance.")
		}
		intent := homarrIntent{AccountID: owner.ID, Name: homarrKeyName, Scopes: []string{"users:read"}, BeforeIDs: []string{}, BeforeKey: []string{}}
		for _, key := range inventory {
			if key.Name == homarrKeyName {
				return homarrManual("An unowned Karakeep key already uses the statistics name; it was not adopted or replaced.")
			}
			intent.BeforeIDs = append(intent.BeforeIDs, key.ID)
			intent.BeforeKey = append(intent.BeforeKey, key.KeyID)
		}
		raw, err := json.Marshal(intent)
		if err != nil {
			return homarrManual("Karakeep issuance intent could not be encoded.")
		}
		if err := homarrIssuance.Begin(h, raw); err != nil {
			return err
		}
		response, requestErr := session.HTTP(ctx, helpers.Request{
			Method: http.MethodPost, Path: "/api/trpc/apiKeys.create", NoRedirect: true,
			Header: map[string]string{"Origin": origin},
			JSON: map[string]any{"json": map[string]any{
				"name": homarrKeyName, "scopes": []string{"users:read"},
			}},
		})
		secret, evidence := captureHomarrCreation(response.Body)
		evidence.Status, evidence.Uncertain = response.Status, requestErr != nil
		data, err := json.Marshal(evidence)
		if err != nil {
			return homarrManual("Karakeep returned unresolved issuance evidence; the intent was preserved.")
		}
		if err := homarrIssuance.Capture(h, helpers.CredentialEvidence{Secret: secret, Data: data}); err != nil {
			return err
		}
		pending, _, err := homarrIssuance.Pending(h)
		if err != nil {
			return err
		}
		inventory, err = homarrInventory(ctx, session)
		if err != nil {
			return err
		}
		return completeHomarrIssuance(ctx, h, owner, inventory, pending)
	})
	return err
}

func completeHomarrIssuance(ctx context.Context, h *helpers.H, owner ownerIdentity, inventory []homarrKey, pending helpers.PendingCredential) error {
	var intent homarrIntent
	if json.Unmarshal(pending.Intent, &intent) != nil || intent.AccountID != owner.ID || intent.Name != homarrKeyName || !homarrScopes(intent.Scopes) || intent.BeforeIDs == nil || intent.BeforeKey == nil || len(intent.BeforeIDs) != len(intent.BeforeKey) {
		return homarrManual("Karakeep issuance intent does not match the intended account or permissions.")
	}
	ids, keyIDs := map[string]bool{}, map[string]bool{}
	for index, id := range intent.BeforeIDs {
		keyID := intent.BeforeKey[index]
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || strings.TrimSpace(keyID) == "" || keyID != strings.TrimSpace(keyID) || ids[id] || keyIDs[keyID] {
			return homarrManual("Karakeep's recorded pre-issuance inventory is ambiguous; its journal was preserved.")
		}
		ids[id], keyIDs[keyID] = true, true
	}
	if pending.Issued == nil {
		return homarrManual("Karakeep key issuance has an unresolved outcome; no second request was sent.")
	}
	var evidence homarrEvidence
	if json.Unmarshal(pending.Issued.Data, &evidence) != nil || evidence.Version != 1 || !evidence.Complete || evidence.Rejected || evidence.Uncertain || evidence.Status != http.StatusOK {
		return homarrManual("Karakeep did not confirm a successful key issuance; captured private evidence was preserved.")
	}
	created := evidence.Created
	keyID, valid := homarrKeyID(pending.Issued.Secret)
	if !valid || strings.TrimSpace(created.ID) == "" || created.ID != strings.TrimSpace(created.ID) || created.Name != homarrKeyName || created.KeyID != keyID || !homarrScopes(created.Scopes) || slices.Contains(intent.BeforeIDs, created.ID) || slices.Contains(intent.BeforeKey, keyID) {
		return homarrManual("Karakeep's captured key does not establish a new exact read-only resource; the journal was preserved.")
	}
	ownership := homarrOwnership{
		homarrKey: homarrKey{ID: created.ID, Name: created.Name, KeyID: keyID, Scopes: []string{"users:read"}},
		AccountID: owner.ID, Fingerprint: helpers.Fingerprint(pending.Issued.Secret),
	}
	if err := verifyHomarrTuple(ctx, h, owner, inventory, ownership, pending.Issued.Secret); err != nil {
		return err
	}
	raw, err := json.Marshal(ownership)
	if err != nil {
		return homarrManual("Karakeep key ownership could not be encoded.")
	}
	return homarrIssuance.Commit(h, raw)
}
