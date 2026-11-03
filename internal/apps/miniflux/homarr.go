package miniflux

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const HomarrRecoveryHint = "Miniflux Homarr access must belong to the recorded administrator reader. Preserve the original token, ownership and pending issuance records. Reconcile only that recorded credential in Miniflux Settings > API Keys; restore the independently confirmed matching private recovery inputs without changing accounts, feeds or read/unread state. A key name or empty inventory is not ownership or permission to reissue. Never clear a pending record or rotate a rejected key to retry. Review existing Compose before approving private input recovery, then run self-host setup miniflux and self-host setup homarr. Miniflux tokens inherit the reader's full account privileges, not a read-only scope."

const homarrKeyDescription = "self-host Homarr statistics"

var homarrIssuance = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending",
}

type homarrIntent struct {
	Version     int     `json:"version"`
	ReaderID    int64   `json:"reader_id"`
	Username    string  `json:"username"`
	IsAdmin     bool    `json:"is_admin"`
	Description string  `json:"description"`
	ExistingIDs []int64 `json:"existing_ids"`
}

type homarrEvidence struct {
	Status      int    `json:"status"`
	Complete    bool   `json:"complete"`
	ID          int64  `json:"id"`
	ReaderID    int64  `json:"reader_id"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

type homarrOwnership struct {
	Version     int    `json:"version"`
	ID          int64  `json:"id"`
	ReaderID    int64  `json:"reader_id"`
	Username    string `json:"username"`
	IsAdmin     bool   `json:"is_admin"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	Fingerprint string `json:"fingerprint"`
}

type homarrKey struct {
	ID          int64  `json:"id"`
	ReaderID    int64  `json:"user_id"`
	Description string `json:"description"`
	Token       string `json:"token"`
	CreatedAt   string `json:"created_at"`
}

func homarrManual() error { return &helpers.ManualError{Hint: HomarrRecoveryHint} }

func HomarrReaderID(h *helpers.H) (int64, error) {
	raw := h.Secret(h.Key("reader_id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0, homarrManual()
	}
	return id, nil
}

func homarrRecord(raw []byte, value any) bool {
	if json.Unmarshal(raw, value) != nil {
		return false
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return false
	}
	normalized, err := json.Marshal(json.RawMessage(raw))
	return err == nil && bytes.Equal(canonical, normalized)
}

func validHomarrCreation(raw string) bool {
	created, err := time.Parse(time.RFC3339Nano, raw)
	return err == nil && !created.IsZero()
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if _, pending, err := homarrIssuance.Pending(h); err != nil {
		return err
	} else if pending {
		return homarrManual()
	}
	token := h.Secret(h.Key("homarr_api_key"))
	raw := h.Secret(h.Key("homarr_token_ownership"))
	h.Log.Secret(token, raw)
	var ownership homarrOwnership
	if token == "" || !homarrRecord([]byte(raw), &ownership) {
		return homarrManual()
	}
	return verifyHomarrKey(ctx, h, token, ownership)
}

func verifyHomarrKey(ctx context.Context, h *helpers.H, token string, ownership homarrOwnership) error {
	readerID, err := HomarrReaderID(h)
	if err != nil {
		return err
	}
	if ownership.Version != 1 || ownership.ID <= 0 || ownership.ReaderID != readerID || ownership.Username != h.AdminUser() || !ownership.IsAdmin || ownership.Description != homarrKeyDescription || !validHomarrCreation(ownership.CreatedAt) || strings.TrimSpace(token) == "" || ownership.Fingerprint != helpers.Fingerprint(token) {
		return homarrManual()
	}
	if _, err := verifyReader(ctx, h); err != nil {
		return err
	}
	h.Log.Secret(token)
	resp, err := authenticatedRequest(ctx, h, helpers.Request{Path: "/v1/me", Header: map[string]string{"X-Auth-Token": token}}, HomarrRecoveryHint)
	if err != nil {
		return err
	}
	identity, err := decodeReader(resp)
	if err != nil || identity.ID != readerID || identity.Username != ownership.Username || !*identity.IsAdmin {
		return homarrManual()
	}
	keys, err := homarrInventory(ctx, h, token, readerID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key.ID == ownership.ID {
			if key.Token == token && key.Description == ownership.Description && key.CreatedAt == ownership.CreatedAt {
				return nil
			}
			return homarrManual()
		}
	}
	return homarrManual()
}

func homarrInventory(ctx context.Context, h *helpers.H, token string, readerID int64) ([]homarrKey, error) {
	request := helpers.Request{Path: "/v1/api-keys"}
	if token == "" {
		request.User, request.Password = h.AdminUser(), h.AdminPassword()
		h.Log.Secret(request.Password)
	} else {
		request.Header = map[string]string{"X-Auth-Token": token}
		h.Log.Secret(token)
	}
	resp, err := authenticatedRequest(ctx, h, request, HomarrRecoveryHint)
	if err != nil {
		if resp.Status == http.StatusNotFound || resp.Status == http.StatusMethodNotAllowed {
			return nil, homarrManual()
		}
		return nil, err
	}
	h.Log.Secret(string(resp.Body))
	var keys []homarrKey
	if json.Unmarshal(resp.Body, &keys) != nil || keys == nil {
		return nil, homarrManual()
	}
	seenIDs, seenTokens := map[int64]bool{}, map[string]bool{}
	for _, key := range keys {
		h.Log.Secret(key.Token)
		if key.ID <= 0 || key.ReaderID != readerID || strings.TrimSpace(key.Token) == "" || strings.TrimSpace(key.Description) == "" || !validHomarrCreation(key.CreatedAt) || seenIDs[key.ID] || seenTokens[key.Token] {
			return nil, homarrManual()
		}
		seenIDs[key.ID], seenTokens[key.Token] = true, true
	}
	return keys, nil
}

func prepareHomarr(ctx context.Context, h *helpers.H) error {
	pending, exists, err := homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	if exists {
		if pending.Issued == nil {
			return homarrManual()
		}
		if err := homarrIssuance.Capture(h, *pending.Issued); err != nil {
			return err
		}
		return acceptHomarr(ctx, h, pending)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_token_ownership")) != "" {
		return VerifyHomarr(ctx, h)
	}
	readerID, err := verifyReader(ctx, h)
	if err != nil {
		return err
	}
	recorded, err := HomarrReaderID(h)
	if err != nil || recorded != readerID {
		return homarrManual()
	}
	keys, err := homarrInventory(ctx, h, "", readerID)
	if err != nil {
		return err
	}
	ids := []int64{}
	for _, key := range keys {
		if strings.EqualFold(key.Description, homarrKeyDescription) {
			return homarrManual()
		}
		ids = append(ids, key.ID)
	}
	slices.Sort(ids)
	intent := homarrIntent{Version: 1, ReaderID: readerID, Username: h.AdminUser(), IsAdmin: true, Description: homarrKeyDescription, ExistingIDs: ids}
	raw, err := json.Marshal(intent)
	if err != nil {
		return homarrManual()
	}
	if err := homarrIssuance.Begin(h, raw); err != nil {
		return err
	}
	h.Log.Secret(h.AdminPassword())
	resp, err := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/v1/api-keys", User: h.AdminUser(), Password: h.AdminPassword(), NoRedirect: true,
		JSON: map[string]string{"description": homarrKeyDescription},
	})
	if err != nil {
		return homarrManual()
	}
	h.Log.Secret(string(resp.Body))
	token, evidence := extractHomarrEvidence(h, resp)
	data, err := json.Marshal(evidence)
	if err != nil {
		return homarrManual()
	}
	issued := helpers.CredentialEvidence{Secret: token, Data: data}
	if err := homarrIssuance.Capture(h, issued); err != nil {
		return err
	}
	return acceptHomarr(ctx, h, helpers.PendingCredential{Intent: raw, Issued: &issued})
}

func acceptHomarr(ctx context.Context, h *helpers.H, pending helpers.PendingCredential) error {
	var intent homarrIntent
	var evidence homarrEvidence
	readerID, err := HomarrReaderID(h)
	if err != nil || !homarrRecord(pending.Intent, &intent) || pending.Issued == nil || !homarrRecord(pending.Issued.Data, &evidence) {
		return homarrManual()
	}
	if intent.Version != 1 || intent.ReaderID != readerID || intent.Username != h.AdminUser() || !intent.IsAdmin || intent.Description != homarrKeyDescription || intent.ExistingIDs == nil || !slices.IsSorted(intent.ExistingIDs) {
		return homarrManual()
	}
	for i, id := range intent.ExistingIDs {
		if id <= 0 || i > 0 && id == intent.ExistingIDs[i-1] {
			return homarrManual()
		}
	}
	if evidence.Status != http.StatusCreated || !evidence.Complete || evidence.ID <= 0 || evidence.ReaderID != readerID || evidence.Description != intent.Description || !validHomarrCreation(evidence.CreatedAt) || slices.Contains(intent.ExistingIDs, evidence.ID) || strings.TrimSpace(pending.Issued.Secret) == "" {
		return homarrManual()
	}
	ownership := homarrOwnership{
		Version: 1, ID: evidence.ID, ReaderID: readerID, Username: intent.Username, IsAdmin: intent.IsAdmin,
		Description: evidence.Description, CreatedAt: evidence.CreatedAt, Fingerprint: helpers.Fingerprint(pending.Issued.Secret),
	}
	raw, err := json.Marshal(ownership)
	if err != nil {
		return homarrManual()
	}
	active, current := h.Secret(h.Key("homarr_api_key")), h.Secret(h.Key("homarr_token_ownership"))
	h.Log.Secret(active, current)
	var savedOwnership homarrOwnership
	if active != "" && active != pending.Issued.Secret || current != "" && (!homarrRecord([]byte(current), &savedOwnership) || savedOwnership != ownership) {
		return homarrManual()
	}
	if err := verifyHomarrKey(ctx, h, pending.Issued.Secret, ownership); err != nil {
		return err
	}
	return homarrIssuance.Commit(h, raw)
}

func extractHomarrEvidence(h *helpers.H, response helpers.Response) (string, homarrEvidence) {
	evidence := homarrEvidence{Status: response.Status}
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return "", evidence
	}
	secret := ""
	valid := true
	seen := map[string]bool{}
	for decoder.More() {
		field, err := decoder.Token()
		name, ok := field.(string)
		if err != nil || !ok {
			return secret, evidence
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return secret, evidence
		}
		if seen[name] {
			valid = false
		}
		seen[name] = true
		switch name {
		case "token":
			var candidate string
			if json.Unmarshal(value, &candidate) != nil || bytes.Equal(value, []byte("null")) {
				valid = false
			}
			h.Log.Secret(candidate)
			if secret == "" {
				secret = candidate
			}
		case "id":
			valid = json.Unmarshal(value, &evidence.ID) == nil && valid
		case "user_id":
			valid = json.Unmarshal(value, &evidence.ReaderID) == nil && valid
		case "description":
			valid = json.Unmarshal(value, &evidence.Description) == nil && valid
		case "created_at":
			valid = json.Unmarshal(value, &evidence.CreatedAt) == nil && valid
		case "error_message":
			valid = false
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return secret, evidence
	}
	if _, err := decoder.Token(); err != io.EOF {
		return secret, evidence
	}
	evidence.Complete = valid
	return secret, evidence
}
