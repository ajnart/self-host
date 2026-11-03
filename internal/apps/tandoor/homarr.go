package tandoor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const HomarrRecoveryHint = "Preserve Tandoor's private Homarr credential, ownership and issuance records. Restore only their known matching records from a verified Project Backup. An intent-only or uncertain issuance needs explicit review of only that credential in Tandoor Settings > API; tokens have no managed names and cannot be recovered from masked values. Do not clear pending records, adopt another token or issue a replacement. Run self-host setup tandoor, then self-host setup homarr."

const tokenThrottleHint = throttleHint + " The token issuance attempt remains pending; preserve its private journal and reconcile that attempt explicitly instead of issuing another token."

var homarrIssuance = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_token_ownership", PendingName: "homarr_token_pending",
}

type ConnectionIdentity struct {
	AccountID     int64 `json:"accountId"`
	ActiveSpaceID int64 `json:"activeSpaceId"`
	FirstSpaceID  int64 `json:"firstSpaceId"`
}

type tokenIntent struct {
	ConnectionIdentity
	Scope     string             `json:"scope"`
	Expires   time.Time          `json:"expires"`
	Inventory *inventoryEvidence `json:"inventory"`
}

type tokenOwnership struct {
	TokenID int64 `json:"tokenId"`
	tokenIntent
	Fingerprint string `json:"fingerprint"`
}

type tokenEvidence struct {
	Status  int             `json:"status"`
	ID      int64           `json:"id"`
	Scope   string          `json:"scope"`
	Expires string          `json:"expires"`
	Outcome issuanceOutcome `json:"outcome"`
}

func tokenManual() error { return &helpers.ManualError{Hint: HomarrRecoveryHint} }

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validToken(token string) bool {
	return strings.HasPrefix(token, "tda_") && len(token) > 8 && !strings.ContainsAny(token, "* \t\r\n")
}

func validIntent(h *helpers.H, intent tokenIntent) bool {
	return intent.AccountID > 0 && intent.ActiveSpaceID > 0 && intent.FirstSpaceID > 0 && intent.Scope == "read" && intent.Expires.After(h.Now()) && validInventoryEvidence(h, intent.Inventory) && intent.Expires.After(intent.Inventory.ObservedAt)
}

func readOwnership(h *helpers.H) (tokenOwnership, error) {
	var ownership tokenOwnership
	key, raw := h.Secret(h.Key("homarr_api_key")), h.Secret(h.Key("homarr_token_ownership"))
	h.Log.Secret(key, raw)
	if json.Unmarshal([]byte(raw), &ownership) != nil || ownership.TokenID <= 0 || !validIntent(h, ownership.tokenIntent) || !validToken(key) || ownership.Fingerprint != tokenFingerprint(key) {
		return ownership, tokenManual()
	}
	return ownership, nil
}

func HomarrIdentity(h *helpers.H) (ConnectionIdentity, error) {
	if _, pending, err := homarrIssuance.Pending(h); err != nil {
		return ConnectionIdentity{}, err
	} else if pending {
		return ConnectionIdentity{}, tokenManual()
	}
	ownership, err := readOwnership(h)
	return ownership.ConnectionIdentity, err
}

func firstSpace(raw json.RawMessage) (int64, error) {
	var spaces []struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &spaces) != nil {
		var page struct {
			Results json.RawMessage `json:"results"`
		}
		if json.Unmarshal(raw, &page) != nil || json.Unmarshal(page.Results, &spaces) != nil {
			return 0, tokenManual()
		}
	}
	if len(spaces) == 0 || spaces[0].ID <= 0 {
		return 0, tokenManual()
	}
	return spaces[0].ID, nil
}

func checkIdentity(h *helpers.H, identity ConnectionIdentity, personal, current, spaces json.RawMessage) error {
	active, err := accountSpace(h, identity.AccountID, personal, current)
	if err != nil {
		return err
	}
	first, err := firstSpace(spaces)
	if err != nil {
		return err
	}
	if active != identity.ActiveSpaceID || first != identity.FirstSpaceID {
		return tokenManual()
	}
	return nil
}

func readIdentity(ctx context.Context, h *helpers.H, header map[string]string, hint string) (json.RawMessage, json.RawMessage, json.RawMessage, error) {
	var personal, current, spaces json.RawMessage
	for _, request := range []struct {
		path string
		body *json.RawMessage
	}{
		{"/api/user-space/all_personal/", &personal},
		{"/api/space/current/", &current},
		{"/api/space/", &spaces},
	} {
		if err := authenticatedRead(ctx, h, header, request.path, request.body, hint); err != nil {
			return nil, nil, nil, err
		}
	}
	return personal, current, spaces, nil
}

func verifyToken(ctx context.Context, h *helpers.H, key string, ownership tokenOwnership) error {
	if !validIntent(h, ownership.tokenIntent) || ownership.TokenID <= ownership.Inventory.MaxID || !validToken(key) || ownership.Fingerprint != tokenFingerprint(key) {
		return tokenManual()
	}
	header := map[string]string{"Authorization": "Bearer " + key}
	personal, current, spaces, err := readIdentity(ctx, h, header, HomarrRecoveryHint)
	if err != nil {
		return err
	}
	if err := checkIdentity(h, ownership.ConnectionIdentity, personal, current, spaces); err != nil {
		if errors.Is(err, helpers.ErrManual) {
			return tokenManual()
		}
		return err
	}
	inventory, err := readTokenInventory(ctx, h, header)
	if err != nil {
		return err
	}
	matched := false
	for _, token := range inventory {
		if token.ID != ownership.TokenID {
			continue
		}
		masked := "tda_************_******_***********" + key[len(key)-4:]
		if token.Scope != "read" || !token.Expires.Equal(ownership.Expires) || token.Token != key && token.Token != masked {
			return tokenManual()
		}
		matched = true
	}
	if !matched {
		return tokenManual()
	}
	return nil
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if _, err := HomarrIdentity(h); err != nil {
		return err
	}
	ownership, err := readOwnership(h)
	if err != nil {
		return err
	}
	return verifyToken(ctx, h, h.Secret(h.Key("homarr_api_key")), ownership)
}

func acceptCaptured(ctx context.Context, h *helpers.H, pending helpers.PendingCredential) error {
	if pending.Issued == nil {
		return tokenManual()
	}
	var intent tokenIntent
	var evidence tokenEvidence
	if json.Unmarshal(pending.Intent, &intent) != nil || !validIntent(h, intent) || json.Unmarshal(pending.Issued.Data, &evidence) != nil {
		return tokenManual()
	}
	if evidence.Status == http.StatusTooManyRequests {
		return &helpers.ManualError{Hint: tokenThrottleHint}
	}
	if !evidence.Outcome.acceptable() || evidence.Status != http.StatusCreated || evidence.ID <= intent.Inventory.MaxID || evidence.Scope != "read" || !validToken(pending.Issued.Secret) {
		return tokenManual()
	}
	expires, err := time.Parse(time.RFC3339Nano, evidence.Expires)
	if err != nil || !expires.Equal(intent.Expires) {
		return tokenManual()
	}
	ownership := tokenOwnership{TokenID: evidence.ID, tokenIntent: intent, Fingerprint: tokenFingerprint(pending.Issued.Secret)}
	if err := verifyToken(ctx, h, pending.Issued.Secret, ownership); err != nil {
		return err
	}
	body, err := json.Marshal(ownership)
	if err != nil {
		return errors.New("Tandoor verified token ownership could not be encoded")
	}
	return homarrIssuance.Commit(h, body)
}

func ensureHomarrToken(ctx context.Context, h *helpers.H) error {
	pending, exists, err := homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	if exists {
		return acceptCaptured(ctx, h, pending)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_token_ownership")) != "" {
		return VerifyHomarr(ctx, h)
	}
	session, id, err := ownerSession(ctx, h)
	if err != nil {
		return err
	}
	personal, current, spaces, err := readIdentity(ctx, h, map[string]string{"X-Session-Token": session}, ownerRecoveryHint)
	if err != nil {
		return err
	}
	active, err := accountSpace(h, id, personal, current)
	if err != nil {
		return err
	}
	first, err := firstSpace(spaces)
	if err != nil {
		return err
	}
	inventory, err := readTokenInventory(ctx, h, map[string]string{"X-Session-Token": session})
	if err != nil {
		return err
	}
	observation := inventoryEvidence{Count: len(inventory), ObservedAt: h.Now().UTC()}
	for _, token := range inventory {
		if token.ID > observation.MaxID {
			observation.MaxID = token.ID
		}
	}
	intent := tokenIntent{ConnectionIdentity: ConnectionIdentity{AccountID: id, ActiveSpaceID: active, FirstSpaceID: first}, Scope: "read", Expires: h.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Second), Inventory: &observation}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return errors.New("Tandoor token intent could not be encoded")
	}
	if err := homarrIssuance.Begin(h, encoded); err != nil {
		return err
	}
	resp, requestErr := h.HTTP(ctx, helpers.Request{
		Method: http.MethodPost, Path: "/api/access-token/", NoRedirect: true,
		Header: map[string]string{"X-Session-Token": session}, JSON: map[string]string{"scope": "read", "expires": intent.Expires.Format(time.RFC3339)},
	})
	key, evidence := captureTokenResponse(resp.Body, resp.Status, requestErr != nil)
	h.Log.Secret(key)
	data, err := json.Marshal(evidence)
	if err != nil {
		return errors.New("Tandoor token response evidence could not be encoded; preserve its issuance journal")
	}
	if err := homarrIssuance.Capture(h, helpers.CredentialEvidence{Secret: key, Data: data}); err != nil {
		return err
	}
	pending, _, err = homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	return acceptCaptured(ctx, h, pending)
}
