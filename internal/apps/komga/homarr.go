package komga

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const homarrKeyComment = "self-host Homarr statistics"

const HomarrRecoveryHint = "Select an existing Komga account with only USER rights, all-library metadata access and no age/label restrictions. Supply its confirmed ID/email/password using the optional private komga.homarr_user_id, komga.homarr_user_email and komga.homarr_user_password inputs with owner-only files and install --set-file. Preserve and separately approve any existing Compose replacement; never reinstall blindly. Run self-host setup komga, then self-host setup homarr. Existing or uncertain keys require restoring the confirmed matching private credential/ownership or explicitly reconciling the recorded issuance; never clear a journal to issue another key, adopt a key by its comment, replace a user or broaden their permissions. Keys inherit ordinary account rights and are not strictly read-only. This verifies only the App-owned credential; managed Homarr linking remains Manual until a supported exact saved-key verification contract exists. Preserve Homarr's saved key and user resources."

var homarrIssuance = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending",
}

type homarrUser struct {
	ID                 string          `json:"id"`
	Email              string          `json:"email"`
	Roles              []string        `json:"roles"`
	SharedAllLibraries *bool           `json:"sharedAllLibraries"`
	SharedLibrariesIDs *[]string       `json:"sharedLibrariesIds"`
	LabelsAllow        *[]string       `json:"labelsAllow"`
	LabelsExclude      *[]string       `json:"labelsExclude"`
	AgeRestriction     json.RawMessage `json:"ageRestriction"`
}

type homarrKey struct {
	ID               string `json:"id"`
	UserID           string `json:"userId"`
	Comment          string `json:"comment"`
	CreatedDate      string `json:"createdDate"`
	LastModifiedDate string `json:"lastModifiedDate"`
}

type homarrIntent struct {
	UserID  string `json:"userId"`
	Email   string `json:"email"`
	Comment string `json:"comment"`
}

type homarrEvidence struct {
	Key      homarrKey `json:"key"`
	Status   int       `json:"status"`
	Complete bool      `json:"complete"`
}

type homarrOwnership struct {
	Key         homarrKey `json:"key"`
	Email       string    `json:"email"`
	Fingerprint string    `json:"fingerprint"`
}

func homarrRequested(h *helpers.H) bool {
	if h.Peer("homarr") != nil {
		return true
	}
	for _, name := range []string{"homarr_user_id", "homarr_user_email", "homarr_user_password", "homarr_api_key", "homarr_key_ownership", "homarr_key_pending"} {
		if h.Secret(h.Key(name)) != "" {
			return true
		}
	}
	return false
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + HomarrRecoveryHint}
}

func selectedHomarrUser(h *helpers.H) (homarrIntent, error) {
	selected := homarrIntent{
		UserID: h.Secret(h.Key("homarr_user_id")), Email: h.Secret(h.Key("homarr_user_email")), Comment: homarrKeyComment,
	}
	if strings.TrimSpace(selected.UserID) == "" || selected.UserID != strings.TrimSpace(selected.UserID) || !validEmail(selected.Email) {
		return homarrIntent{}, homarrManual("No complete existing limited Komga account is explicitly selected.")
	}
	return selected, nil
}

func VerifyHomarrIdentity(h *helpers.H, body []byte) error {
	selected, err := selectedHomarrUser(h)
	if err != nil {
		return err
	}
	var user homarrUser
	if json.Unmarshal(body, &user) != nil || user.ID != selected.UserID || user.Email != selected.Email || len(user.Roles) != 1 || user.Roles[0] != "USER" || user.SharedAllLibraries == nil || !*user.SharedAllLibraries || user.SharedLibrariesIDs == nil || len(*user.SharedLibrariesIDs) != 0 || user.LabelsAllow == nil || len(*user.LabelsAllow) != 0 || user.LabelsExclude == nil || len(*user.LabelsExclude) != 0 || len(user.AgeRestriction) != 0 && !bytes.Equal(bytes.TrimSpace(user.AgeRestriction), []byte("null")) {
		return homarrManual("Komga did not verify the selected account's exact identity and limited metadata permissions; no account or policy was changed.")
	}
	return nil
}

func homarrRequest(ctx context.Context, h *helpers.H, request helpers.Request) (helpers.Response, error) {
	request.NoRedirect = true
	response, err := h.HTTP(ctx, request)
	if err != nil || response.Status != http.StatusOK {
		return helpers.Response{}, homarrManual("Komga's selected-account request was unavailable or rejected; preserve its credentials and verify connectivity, API compatibility and trusted TLS.")
	}
	return response, nil
}

func validHomarrKey(key homarrKey, userID string) bool {
	created, err := time.Parse(time.RFC3339Nano, key.CreatedDate)
	modified, modifiedErr := time.Parse(time.RFC3339Nano, key.LastModifiedDate)
	return strings.TrimSpace(key.ID) != "" && key.ID == strings.TrimSpace(key.ID) && key.UserID == userID && strings.TrimSpace(key.Comment) != "" && err == nil && modifiedErr == nil && !created.IsZero() && !modified.Before(created)
}

func homarrKeyInventory(ctx context.Context, h *helpers.H, auth helpers.Request, userID string) ([]homarrKey, error) {
	auth.Path = "/api/v2/users/me/api-keys"
	response, err := homarrRequest(ctx, h, auth)
	if err != nil {
		return nil, err
	}
	h.Log.Secret(string(response.Body))
	var keys *[]homarrKey
	if response.Decode(&keys) != nil || keys == nil {
		return nil, homarrManual("Komga did not return an explicit API-key inventory.")
	}
	seen := map[string]bool{}
	for _, key := range *keys {
		if !validHomarrKey(key, userID) || seen[key.ID] {
			return nil, homarrManual("Komga returned incomplete or ambiguous API-key metadata.")
		}
		seen[key.ID] = true
	}
	return *keys, nil
}

func verifyHomarrKey(ctx context.Context, h *helpers.H, secret string, key homarrKey) error {
	selected, err := selectedHomarrUser(h)
	if err != nil {
		return err
	}
	if !validHomarrKey(key, selected.UserID) || key.Comment != homarrKeyComment || strings.TrimSpace(secret) == "" {
		return homarrManual("The recorded Komga key's selected ownership is incomplete.")
	}
	h.Log.Secret(secret)
	auth := helpers.Request{Header: map[string]string{"X-API-Key": secret}, Path: "/api/v2/users/me"}
	response, err := homarrRequest(ctx, h, auth)
	if err != nil {
		return err
	}
	if err := VerifyHomarrIdentity(h, response.Body); err != nil {
		return err
	}
	keys, err := homarrKeyInventory(ctx, h, auth, selected.UserID)
	if err != nil {
		return err
	}
	for _, current := range keys {
		if current.ID == key.ID {
			if current == key {
				return nil
			}
			return homarrManual("Komga's recorded key metadata changed; the existing key was preserved.")
		}
	}
	return homarrManual("Komga's recorded key is no longer present; no replacement was issued.")
}

func homarrFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if _, err := signedIn(ctx, h, (App{}).AdminEmail(h.AdminUser()), h.AdminPassword()); err != nil {
		return err
	}
	if _, pending, err := homarrIssuance.Pending(h); err != nil {
		return err
	} else if pending {
		return homarrManual("Komga's pending key issuance needs Setup reconciliation before the key can be used.")
	}
	secret := h.Secret(h.Key("homarr_api_key"))
	var ownership homarrOwnership
	if secret == "" || json.Unmarshal([]byte(h.Secret(h.Key("homarr_key_ownership"))), &ownership) != nil || ownership.Fingerprint != homarrFingerprint(secret) || ownership.Email != h.Secret(h.Key("homarr_user_email")) {
		return homarrManual("Komga's key and confirmed ownership are missing or mismatched.")
	}
	return verifyHomarrKey(ctx, h, secret, ownership.Key)
}

func recoverHomarrKey(ctx context.Context, h *helpers.H, pending helpers.PendingCredential) error {
	selected, err := selectedHomarrUser(h)
	if err != nil {
		return err
	}
	var intent homarrIntent
	if json.Unmarshal(pending.Intent, &intent) != nil || intent != selected || pending.Issued == nil {
		return homarrManual("Komga's issuance intent is uncertain or differs from the selected existing account; no issuance was retried.")
	}
	var evidence homarrEvidence
	if json.Unmarshal(pending.Issued.Data, &evidence) != nil || !evidence.Complete || evidence.Status != http.StatusOK {
		return homarrManual("Komga's captured issuance was not confirmed as successful; its selected evidence was preserved.")
	}
	if err := verifyHomarrKey(ctx, h, pending.Issued.Secret, evidence.Key); err != nil {
		return err
	}
	ownership, err := json.Marshal(homarrOwnership{Key: evidence.Key, Email: intent.Email, Fingerprint: homarrFingerprint(pending.Issued.Secret)})
	if err != nil {
		return homarrManual("Komga's verified key ownership could not be encoded.")
	}
	return homarrIssuance.Commit(h, ownership)
}

func captureHomarrKey(response helpers.Response) (string, homarrEvidence) {
	evidence := homarrEvidence{Status: response.Status}
	decoder := json.NewDecoder(bytes.NewReader(response.Body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return "", evidence
	}
	secret := ""
	seen := map[string]bool{}
	valid := true
	fields := map[string]*string{
		"key": &secret, "id": &evidence.Key.ID, "userId": &evidence.Key.UserID, "comment": &evidence.Key.Comment,
		"createdDate": &evidence.Key.CreatedDate, "lastModifiedDate": &evidence.Key.LastModifiedDate,
	}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return secret, evidence
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return secret, evidence
		}
		if seen[name] {
			valid = false
			if name == "key" {
				secret = ""
			}
			continue
		}
		seen[name] = true
		if destination := fields[name]; destination != nil && json.Unmarshal(raw, destination) != nil {
			valid = false
		}
	}
	last, err := decoder.Token()
	_, trailing := decoder.Token()
	evidence.Complete = valid && err == nil && last == json.Delim('}') && trailing == io.EOF
	return secret, evidence
}

func prepareHomarrKey(ctx context.Context, h *helpers.H) error {
	if _, err := signedIn(ctx, h, (App{}).AdminEmail(h.AdminUser()), h.AdminPassword()); err != nil {
		return err
	}
	pending, exists, err := homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	if exists {
		return recoverHomarrKey(ctx, h, pending)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_key_ownership")) != "" {
		return VerifyHomarr(ctx, h)
	}
	selected, err := selectedHomarrUser(h)
	if err != nil {
		return err
	}
	password := h.Secret(h.Key("homarr_user_password"))
	if strings.TrimSpace(password) == "" {
		return homarrManual("The selected existing Komga account's private password is needed before its first key can be issued.")
	}
	h.Log.Secret(password)
	auth := helpers.Request{User: selected.Email, Password: password, Path: "/api/v2/users/me"}
	response, err := homarrRequest(ctx, h, auth)
	if err != nil {
		return err
	}
	if err := VerifyHomarrIdentity(h, response.Body); err != nil {
		return err
	}
	keys, err := homarrKeyInventory(ctx, h, auth, selected.UserID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key.Comment == homarrKeyComment {
			return homarrManual("An existing Komga key has the managed comment but no accepted recorded credential; it was not adopted or replaced.")
		}
	}
	intent, err := json.Marshal(selected)
	if err != nil {
		return homarrManual("Komga's selected account could not be recorded before issuance.")
	}
	if err := homarrIssuance.Begin(h, intent); err != nil {
		return err
	}
	auth.Method, auth.Path, auth.NoRedirect = http.MethodPost, "/api/v2/users/me/api-keys", true
	auth.JSON = struct {
		Comment string `json:"comment"`
	}{homarrKeyComment}
	response, err = h.HTTP(ctx, auth)
	if err != nil {
		return homarrManual("Komga's key issuance response was not received; preserve the pending intent and reconcile it without another POST.")
	}
	h.Log.Secret(string(response.Body))
	secret, evidence := captureHomarrKey(response)
	h.Log.Secret(secret)
	data, err := json.Marshal(evidence)
	if err != nil {
		return homarrManual("Komga's selected issuance evidence could not be encoded; the intent was preserved.")
	}
	if err := homarrIssuance.Capture(h, helpers.CredentialEvidence{Secret: secret, Data: data}); err != nil {
		return err
	}
	pending, exists, err = homarrIssuance.Pending(h)
	if err != nil {
		return err
	}
	if !exists {
		return homarrManual("Komga's captured key evidence is unavailable; no issuance may be retried.")
	}
	return recoverHomarrKey(ctx, h, pending)
}
