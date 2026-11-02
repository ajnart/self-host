package jellystat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/ajnart/self-host/internal/helpers"
)

const homarrKeyName = "self-host Homarr statistics"
const homarrRecovery = "Preserve Jellystat's saved key, pinned ownership and issuance journal. Restore the exact matching private records or explicitly reconcile the original selected key in Jellystat, then run self-host setup jellystat and self-host setup homarr. Never clear a pending journal, adopt a key by name, rotate a rejected key or replace the selected backend to retry Setup. Jellystat API keys grant full API access; this Link only reads."

var homarrJournal = helpers.CredentialIssuance{SecretName: "homarr_api_key", OwnershipName: "homarr_ownership", PendingName: "homarr_pending"}
var keyResponseSecrets = regexp.MustCompile(`"key"\s*:\s*("(?:[^"\\]|\\.)*")`)

type keyIdentity struct {
	Endpoint      string `json:"endpoint"`
	User          string `json:"user"`
	ServerAddress string `json:"serverAddress"`
	ServerAdminID string `json:"serverAdminId"`
	MediaServer   string `json:"mediaServer"`
}

type keyIntent struct {
	Identity        keyIdentity `json:"identity"`
	Name            string      `json:"name"`
	InventoryDigest string      `json:"inventoryDigest"`
	InventoryCount  int         `json:"inventoryCount"`
}

type keyCapture struct {
	Intent      keyIntent `json:"intent"`
	Status      int       `json:"status"`
	Selected    bool      `json:"selected"`
	Fingerprint string    `json:"fingerprint"`
}

type keyOwnership struct {
	Identity    keyIdentity `json:"identity"`
	Name        string      `json:"name"`
	Fingerprint string      `json:"fingerprint"`
}

type keyRecord struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + homarrRecovery}
}

func homarrRelevant(h *helpers.H) bool {
	return h.Peer("homarr") != nil || h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_ownership")) != "" || h.Secret(h.Key("homarr_pending")) != ""
}

func currentKeyIdentity(h *helpers.H) keyIdentity {
	address, _ := normalizedAddress(h.Secret(h.Key("server_address")))
	kind := h.Inputs["media_server"]
	if kind == "" {
		kind = "jellyfin"
	}
	return keyIdentity{Endpoint: h.InternalURL(), User: h.AdminUser(), ServerAddress: address, ServerAdminID: h.Secret(h.Key("server_admin_id")), MediaServer: kind}
}

func keyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func parseKeyInventory(h *helpers.H, raw []byte) ([]keyRecord, error) {
	RegisterKeySecrets(h, raw)
	var records []keyRecord
	err := json.Unmarshal(raw, &records)
	for _, record := range records {
		h.Log.Secret(record.Key)
	}
	if err != nil || records == nil {
		return nil, homarrManual("Jellystat did not return an explicit API-key inventory.")
	}
	seen := map[string]bool{}
	for _, record := range records {
		if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.Key) == "" || strings.ContainsAny(record.Key, "\r\n\x00") || seen[record.Key] {
			return nil, homarrManual("Jellystat returned an ambiguous API-key inventory.")
		}
		seen[record.Key] = true
	}
	return records, nil
}

func RegisterKeySecrets(h *helpers.H, raw []byte) {
	h.Log.Secret(string(raw))
	for _, match := range keyResponseSecrets.FindAllSubmatch(raw, -1) {
		var key string
		if json.Unmarshal(match[1], &key) == nil {
			h.Log.Secret(key)
		}
	}
}

func readKeyInventory(ctx context.Context, h *helpers.H, header map[string]string) ([]keyRecord, error) {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/keys", Header: header, NoRedirect: true})
	records, parseErr := parseKeyInventory(h, response.Body)
	if err != nil || response.Status != http.StatusOK {
		return nil, homarrManual("Jellystat did not accept the credential inventory read.")
	}
	return records, parseErr
}

func inventoryDigest(records []keyRecord) string {
	parts := make([]string, 0, len(records))
	for _, record := range records {
		parts = append(parts, keyFingerprint(record.Name+"\x00"+record.Key))
	}
	sort.Strings(parts)
	return keyFingerprint(strings.Join(parts, "\n"))
}

func selectedIssuedKey(before, after []keyRecord) (string, bool) {
	if len(after) != len(before)+1 {
		return "", false
	}
	previous := map[string]string{}
	for _, record := range before {
		previous[record.Key] = record.Name
	}
	selected := ""
	retained := 0
	for _, record := range after {
		if name, exists := previous[record.Key]; exists {
			if record.Name != name {
				return "", false
			}
			retained++
		} else if record.Name == homarrKeyName && selected == "" {
			selected = record.Key
		} else {
			return "", false
		}
	}
	return selected, selected != "" && retained == len(before)
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	_, err := verifiedSession(ctx, h)
	if err != nil {
		return err
	}
	return verifySavedHomarr(ctx, h)
}

func savedKeyOwnership(h *helpers.H) (keyOwnership, error) {
	key, raw := h.Secret(h.Key("homarr_api_key")), h.Secret(h.Key("homarr_ownership"))
	h.Log.Secret(key, raw)
	var owned keyOwnership
	if key == "" || json.Unmarshal([]byte(raw), &owned) != nil || owned.Name != homarrKeyName || owned.Fingerprint != keyFingerprint(key) || owned.Identity != currentKeyIdentity(h) {
		return keyOwnership{}, homarrManual("The Jellystat key or its pinned ownership is missing or differs from the selected instance.")
	}
	return owned, nil
}

func verifySavedHomarr(ctx context.Context, h *helpers.H) error {
	if _, exists, err := homarrJournal.Pending(h); err != nil {
		return err
	} else if exists {
		return homarrManual("Jellystat has an unresolved issuance journal; retry its App Setup to verify the original captured credential.")
	}
	owned, err := savedKeyOwnership(h)
	if err != nil {
		return err
	}
	return verifyIssuedKey(ctx, h, h.Secret(h.Key("homarr_api_key")), owned.Identity)
}

func verifyIssuedKey(ctx context.Context, h *helpers.H, key string, identity keyIdentity) error {
	if identity != currentKeyIdentity(h) {
		return homarrManual("Jellystat's account or selected backend changed after key issuance.")
	}
	header := map[string]string{"X-API-Token": key}
	var config configuration
	if err := request(ctx, h, helpers.Request{Path: "/api/getconfig", Header: header}, &config); err != nil {
		return err
	}
	if err := verifyAccount(h, config); err != nil {
		return err
	}
	if err := verifySelection(h, config); err != nil {
		return err
	}
	records, err := readKeyInventory(ctx, h, header)
	if err != nil {
		return err
	}
	matches := 0
	for _, record := range records {
		if record.Name == homarrKeyName {
			if record.Key != key {
				return homarrManual("The named Jellystat key no longer matches the issued credential.")
			}
			matches++
		}
	}
	if matches != 1 {
		return homarrManual("The issued Jellystat key cannot be identified uniquely.")
	}
	return nil
}

func setupHomarrKey(ctx context.Context, h *helpers.H) error {
	token, err := verifiedSession(ctx, h)
	if err != nil {
		return err
	}
	if pending, exists, err := homarrJournal.Pending(h); err != nil {
		return err
	} else if exists {
		return resumeHomarrKey(ctx, h, pending)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_ownership")) != "" {
		return verifySavedHomarr(ctx, h)
	}
	header := map[string]string{"Authorization": "Bearer " + token}
	before, err := readKeyInventory(ctx, h, header)
	if err != nil {
		return err
	}
	for _, record := range before {
		if record.Name == homarrKeyName {
			return homarrManual("An unowned Jellystat key already uses the selected creation name.")
		}
	}
	intent := keyIntent{Identity: currentKeyIdentity(h), Name: homarrKeyName, InventoryDigest: inventoryDigest(before), InventoryCount: len(before)}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return homarrManual("The Jellystat issuance intent could not be encoded.")
	}
	if err := homarrJournal.Begin(h, encoded); err != nil {
		return homarrManual("Private storage could not checkpoint Jellystat issuance; repair it before retrying.")
	}
	response, issueErr := h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/keys", Header: header, JSON: map[string]string{"name": homarrKeyName}, NoRedirect: true})
	after, parseErr := parseKeyInventory(h, response.Body)
	selected, valid := selectedIssuedKey(before, after)
	valid = valid && parseErr == nil
	if !valid {
		selected = ""
	}
	capture := keyCapture{Intent: intent, Status: response.Status, Selected: valid, Fingerprint: keyFingerprint(selected)}
	data, err := json.Marshal(capture)
	if err != nil {
		return homarrManual("The selected Jellystat issuance evidence could not be encoded.")
	}
	if err := homarrJournal.Capture(h, helpers.CredentialEvidence{Secret: selected, Data: data}); err != nil {
		return homarrManual("Private storage could not capture Jellystat issuance; preserve the journal and reconcile the original key.")
	}
	if issueErr != nil || response.Status != http.StatusOK || !valid {
		return homarrManual("Jellystat did not confirm one unambiguous successful issuance; the captured evidence remains held.")
	}
	pending, _, err := homarrJournal.Pending(h)
	if err != nil {
		return err
	}
	return resumeHomarrKey(ctx, h, pending)
}

func resumeHomarrKey(ctx context.Context, h *helpers.H, pending helpers.PendingCredential) error {
	var intent keyIntent
	var capture keyCapture
	if json.Unmarshal(pending.Intent, &intent) != nil || pending.Issued == nil || json.Unmarshal(pending.Issued.Data, &capture) != nil {
		return homarrManual("The pending Jellystat issuance cannot be verified safely.")
	}
	key := pending.Issued.Secret
	if intent.Name != homarrKeyName || intent.Identity != currentKeyIdentity(h) || intent.InventoryCount < 0 || len(intent.InventoryDigest) != 64 || capture.Intent != intent || capture.Status != http.StatusOK || !capture.Selected || key == "" || capture.Fingerprint != keyFingerprint(key) {
		return homarrManual("The captured Jellystat credential was rejected, uncertain or belongs to a different pinned instance.")
	}
	if err := verifyIssuedKey(ctx, h, key, intent.Identity); err != nil {
		return err
	}
	ownership, err := json.Marshal(keyOwnership{Identity: intent.Identity, Name: homarrKeyName, Fingerprint: keyFingerprint(key)})
	if err != nil {
		return homarrManual("The verified Jellystat key ownership could not be encoded.")
	}
	if err := homarrJournal.Commit(h, ownership); err != nil {
		return homarrManual("Private storage could not finish Jellystat issuance; repair it and retry the same captured key.")
	}
	return nil
}

func VerifyHomarrReadback(h *helpers.H, configBody, inventoryBody []byte) error {
	records, err := parseKeyInventory(h, inventoryBody)
	if err != nil {
		return err
	}
	if _, exists, err := homarrJournal.Pending(h); err != nil {
		return err
	} else if exists {
		return homarrManual("The saved Jellystat connection has unresolved issuance evidence.")
	}
	if _, err := savedKeyOwnership(h); err != nil {
		return err
	}
	var config configuration
	if json.Unmarshal(configBody, &config) != nil {
		return homarrManual("Homarr did not read Jellystat's selected configuration.")
	}
	if err := verifyAccount(h, config); err != nil {
		return err
	}
	if err := verifySelection(h, config); err != nil {
		return err
	}
	matches := 0
	for _, record := range records {
		if record.Name == homarrKeyName && record.Key == "[REDACTED]" {
			matches++
		} else if record.Name == homarrKeyName || strings.Contains(record.Key, "[REDACTED]") {
			return homarrManual("Homarr's saved connection uses a different Jellystat key.")
		}
	}
	if matches != 1 {
		return homarrManual("Homarr did not prove use of the exact issued Jellystat key.")
	}
	return nil
}
