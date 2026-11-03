package mealie

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const homarrKeyName = "self-host Homarr statistics"

const HomarrRecoveryHint = "Run self-host setup mealie, then self-host setup homarr. Mealie tokens inherit full access to the selected administrator account, with recipes/users scoped to its actual household and categories/tags shared by its group; they have no read-only scopes. Preserve the known token, ownership and pending issuance records privately. Reconcile only the recorded token in Mealie user settings > API Tokens; never delete/adopt a token by name, clear an uncertain pending record, switch household or issue a replacement to make Setup pass. Restore only a confirmed matching token/ownership through approved private-input recovery, preserving existing Compose edits and separately approving any reinstall replacement. Initial-account setup remains Manual; self-host never takes over the factory account or creates a dedicated user."

var homarrIssuance = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending",
}

type Identity struct {
	UserID      string `json:"userId"`
	GroupID     string `json:"groupId"`
	HouseholdID string `json:"householdId"`
}

type homarrIntent struct {
	Identity      Identity `json:"identity"`
	Name          string   `json:"name"`
	IntegrationID string   `json:"integrationId"`
	Authority     string   `json:"authority"`
}

type homarrOwnership struct {
	Intent      homarrIntent `json:"intent"`
	ID          int64        `json:"id"`
	CreatedAt   time.Time    `json:"createdAt"`
	Fingerprint string       `json:"fingerprint"`
}

type tokenMetadata struct {
	ID        int64      `json:"id"`
	Name      *string    `json:"name"`
	CreatedAt *time.Time `json:"createdAt"`
}

type homarrCapture struct {
	Status    int             `json:"status"`
	Complete  bool            `json:"complete"`
	Duplicate bool            `json:"duplicate"`
	ID        json.RawMessage `json:"id"`
	Name      json.RawMessage `json:"name"`
	CreatedAt json.RawMessage `json:"createdAt"`
}

func selectedIdentity(h *helpers.H) (Identity, error) {
	values := []string{}
	for _, name := range []string{"admin_user_id", "group_id", "household_id"} {
		value := h.Secret(h.Key(name))
		h.Log.Secret(value)
		if !identityID.MatchString(value) {
			return Identity{}, manual(h, "The intended existing Mealie administrator, group and household must be explicitly selected with valid UUIDs.")
		}
		values = append(values, strings.ToLower(value))
	}
	return Identity{values[0], values[1], values[2]}, nil
}

func intendedHomarrKey(h *helpers.H) (homarrIntent, error) {
	identity, err := selectedIdentity(h)
	return homarrIntent{Identity: identity, Name: homarrKeyName, IntegrationID: "homarr", Authority: "full-account-access"}, err
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + HomarrRecoveryHint}
}

func tokenInventory(ctx context.Context, h *helpers.H, key string) ([]tokenMetadata, error) {
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/users/self", NoRedirect: true,
		Header: (helpers.KeyScheme{Header: "Authorization", Prefix: "Bearer "}).Headers(key)})
	if err != nil {
		return nil, h.Log.RedactError(err)
	}
	selected, err := selectedIdentity(h)
	if err != nil {
		return nil, err
	}
	var result struct {
		Identity
		ID     string           `json:"id"`
		Tokens *[]tokenMetadata `json:"tokens"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&result) != nil || result.Tokens == nil || !strings.EqualFold(result.ID, selected.UserID) || !strings.EqualFold(result.GroupID, selected.GroupID) || !strings.EqualFold(result.HouseholdID, selected.HouseholdID) {
		return nil, homarrManual("Mealie did not return the selected account's authenticated token inventory.")
	}
	seen := map[int64]bool{}
	for _, token := range *result.Tokens {
		if token.ID <= 0 || token.Name == nil || seen[token.ID] {
			return nil, homarrManual("Mealie token inventory is incomplete or ambiguous.")
		}
		seen[token.ID] = true
	}
	return *result.Tokens, nil
}

func verifyOwnedKey(ctx context.Context, h *helpers.H, key string, owned homarrOwnership) error {
	expected, err := intendedHomarrKey(h)
	if err != nil {
		return err
	}
	h.Log.Secret(key)
	if key == "" || owned.Intent != expected || owned.ID <= 0 || owned.CreatedAt.IsZero() || owned.Fingerprint != helpers.Fingerprint(key) {
		return homarrManual("The saved Mealie token does not match its immutable account/household ownership record.")
	}
	if err := verifyIdentityToken(ctx, h, key); err != nil {
		return err
	}
	tokens, err := tokenInventory(ctx, h, key)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if token.ID == owned.ID && *token.Name == homarrKeyName && token.CreatedAt != nil && token.CreatedAt.Equal(owned.CreatedAt) {
			return nil
		}
	}
	return homarrManual("Mealie did not confirm the recorded token ID, name and creation time; no replacement was issued.")
}

func VerifyHomarr(ctx context.Context, h *helpers.H) (Identity, error) {
	if _, exists, err := homarrIssuance.Pending(h); err != nil {
		return Identity{}, err
	} else if exists {
		return Identity{}, homarrManual("Mealie token issuance still has pending evidence; retry the App's Setup to reconcile only that captured token.")
	}
	var owned homarrOwnership
	raw := h.Secret(h.Key("homarr_key_ownership"))
	h.Log.Secret(raw)
	if json.Unmarshal([]byte(raw), &owned) != nil {
		return Identity{}, homarrManual("Mealie token ownership is missing or unreadable.")
	}
	if err := verifyOwnedKey(ctx, h, h.Secret(h.Key("homarr_api_key")), owned); err != nil {
		return Identity{}, err
	}
	return owned.Intent.Identity, nil
}

func captureResponse(h *helpers.H, resp helpers.Response) helpers.CredentialEvidence {
	h.Log.Secret(string(resp.Body))
	data := homarrCapture{Status: resp.Status}
	key := ""
	decoder := json.NewDecoder(bytes.NewReader(resp.Body))
	first, err := decoder.Token()
	if err == nil && first == json.Delim('{') {
		seen := map[string]bool{}
		for decoder.More() {
			field, readErr := decoder.Token()
			name, ok := field.(string)
			if readErr != nil || !ok {
				break
			}
			var raw json.RawMessage
			if decoder.Decode(&raw) != nil {
				break
			}
			if seen[name] {
				data.Duplicate = true
			}
			seen[name] = true
			switch name {
			case "token":
				var candidate string
				if json.Unmarshal(raw, &candidate) == nil {
					h.Log.Secret(candidate)
					if key == "" {
						key = candidate
					}
				}
			case "id":
				data.ID = raw
			case "name":
				data.Name = raw
			case "createdAt":
				data.CreatedAt = raw
			}
		}
		last, endErr := decoder.Token()
		_, trailing := decoder.Token()
		data.Complete = endErr == nil && last == json.Delim('}') && trailing == io.EOF
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return helpers.CredentialEvidence{Secret: key}
	}
	return helpers.CredentialEvidence{Secret: key, Data: encoded}
}

func finishHomarrIssuance(ctx context.Context, h *helpers.H, pending helpers.PendingCredential) error {
	expected, err := intendedHomarrKey(h)
	if err != nil {
		return err
	}
	var intent homarrIntent
	if json.Unmarshal(pending.Intent, &intent) != nil || intent != expected || pending.Issued == nil {
		return homarrManual("Mealie issuance is uncertain or its selected account/household changed; the original evidence was preserved.")
	}
	var data homarrCapture
	var metadata tokenMetadata
	if json.Unmarshal(pending.Issued.Data, &data) != nil || data.Status != http.StatusCreated || !data.Complete || data.Duplicate || json.Unmarshal(data.ID, &metadata.ID) != nil || json.Unmarshal(data.Name, &metadata.Name) != nil || json.Unmarshal(data.CreatedAt, &metadata.CreatedAt) != nil || metadata.Name == nil || *metadata.Name != homarrKeyName || metadata.CreatedAt == nil {
		return homarrManual("Mealie did not confirm complete successful token issuance; its captured secret and selected evidence remain held.")
	}
	owned := homarrOwnership{Intent: intent, ID: metadata.ID, CreatedAt: *metadata.CreatedAt, Fingerprint: helpers.Fingerprint(pending.Issued.Secret)}
	if err := verifyIdentity(ctx, h); err != nil {
		return err
	}
	if err := verifyOwnedKey(ctx, h, pending.Issued.Secret, owned); err != nil {
		return err
	}
	ownership, err := json.Marshal(owned)
	if err != nil {
		return homarrManual("Mealie ownership evidence could not be encoded; the captured token remains held.")
	}
	return homarrIssuance.Commit(h, ownership)
}

func prepareHomarrKey(ctx context.Context, h *helpers.H) error {
	pending, exists, err := homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	if exists {
		return finishHomarrIssuance(ctx, h, pending)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_key_ownership")) != "" {
		_, err := VerifyHomarr(ctx, h)
		return err
	}
	if err := verifyIdentity(ctx, h); err != nil {
		return err
	}
	token, err := sessionToken(ctx, h)
	if err != nil {
		return err
	}
	tokens, err := tokenInventory(ctx, h, token)
	if err != nil {
		return err
	}
	for _, key := range tokens {
		if *key.Name == homarrKeyName {
			return homarrManual("An unowned Mealie token already uses the managed name; it was preserved and no token was adopted or issued.")
		}
	}
	intent, err := intendedHomarrKey(h)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return homarrManual("Mealie issuance intent could not be encoded.")
	}
	if err := homarrIssuance.Begin(h, raw); err != nil {
		return err
	}
	resp, err := h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/users/api-tokens", NoRedirect: true,
		Header: (helpers.KeyScheme{Header: "Authorization", Prefix: "Bearer "}).Headers(token),
		JSON:   map[string]string{"name": intent.Name, "integrationId": intent.IntegrationID}})
	if err != nil {
		return homarrManual("Mealie issuance did not return a confirmed response; preserve the intent and reconcile it without another issuance.")
	}
	if err := homarrIssuance.Capture(h, captureResponse(h, resp)); err != nil {
		return err
	}
	pending, _, err = homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	return finishHomarrIssuance(ctx, h, pending)
}
