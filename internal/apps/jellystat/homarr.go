package jellystat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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
	Incomplete  bool      `json:"incomplete"`
	Duplicate   bool      `json:"duplicate"`
	Error       bool      `json:"error"`
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

const keyInventoryByteLimit = 1 << 20
const keyInventoryRecordLimit = 1024
const keyInventoryFieldLimit = 32

type keyProjection struct {
	Records    []keyRecord
	Incomplete bool
	Duplicate  bool
	Error      bool
}

func (p keyProjection) valid() bool {
	return !p.Incomplete && !p.Duplicate && !p.Error
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
	projection := projectKeyInventory(h, raw)
	if !projection.valid() {
		return nil, homarrManual("Jellystat did not return a complete, unambiguous API-key inventory within the supported bounds.")
	}
	return projection.Records, nil
}

func projectKeyInventory(h *helpers.H, raw []byte) keyProjection {
	RegisterKeySecrets(h, raw)
	projection := keyProjection{Incomplete: true}
	bounded := len(raw) > keyInventoryByteLimit
	if bounded {
		raw = raw[:keyInventoryByteLimit]
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('[') {
		projection.Error = true
		return projection
	}
	seen := map[string]bool{}
	for count := 0; decoder.More(); count++ {
		if count == keyInventoryRecordLimit {
			projection.Error = true
			return projection
		}
		record, complete, duplicate, err := projectKeyRecord(h, decoder)
		projection.Duplicate = projection.Duplicate || duplicate || seen[record.Key]
		if strings.TrimSpace(record.Name) != "" && strings.TrimSpace(record.Key) != "" && !strings.ContainsAny(record.Key, "\r\n\x00") {
			projection.Records = append(projection.Records, record)
			seen[record.Key] = true
		} else {
			projection.Error = true
		}
		if !complete || err != nil {
			projection.Error = true
			return projection
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
		projection.Error = true
		return projection
	}
	if _, err := decoder.Token(); err != io.EOF || bounded {
		projection.Error = true
		return projection
	}
	projection.Incomplete = false
	return projection
}

func projectKeyRecord(h *helpers.H, decoder *json.Decoder) (keyRecord, bool, bool, error) {
	record := keyRecord{}
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return record, false, false, err
	}
	seen := map[string]bool{}
	for decoder.More() {
		if len(seen) == keyInventoryFieldLimit {
			return record, false, false, nil
		}
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return record, false, false, err
		}
		if seen[name] {
			return keyRecord{}, false, true, nil
		}
		seen[name] = true
		if name != "name" && name != "key" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return record, false, false, err
			}
			continue
		}
		token, err = decoder.Token()
		value, ok := token.(string)
		if err != nil || !ok {
			return record, false, false, err
		}
		if name == "name" {
			record.Name = value
		} else {
			record.Key = value
			h.Log.Secret(value)
		}
	}
	token, err = decoder.Token()
	return record, token == json.Delim('}'), false, err
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
	if retained != len(before) {
		return "", false
	}
	return selected, selected != ""
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
	if len(before) == 0 {
		return homarrManual("Jellystat's empty API-key inventory also represents configuration read failure, so it cannot authorize issuance. Restore a readable nonempty inventory or reconcile a known key with its exact private ownership records.")
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
		return err
	}
	response, issueErr := h.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/keys", Header: header, JSON: map[string]string{"name": homarrKeyName}, NoRedirect: true})
	projection := projectKeyInventory(h, response.Body)
	selected, consistent := selectedIssuedKey(before, projection.Records)
	capture := keyCapture{Intent: intent, Status: response.Status, Selected: consistent, Fingerprint: keyFingerprint(selected),
		Incomplete: projection.Incomplete, Duplicate: projection.Duplicate, Error: projection.Error || issueErr != nil}
	data, err := json.Marshal(capture)
	if err != nil {
		return homarrManual("The selected Jellystat issuance evidence could not be encoded.")
	}
	if err := homarrJournal.Capture(h, helpers.CredentialEvidence{Secret: selected, Data: data}); err != nil {
		return err
	}
	if capture.Error || capture.Incomplete || capture.Duplicate || response.Status != http.StatusOK || !consistent {
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
	if intent.Name != homarrKeyName || intent.Identity != currentKeyIdentity(h) || intent.InventoryCount <= 0 || len(intent.InventoryDigest) != 64 || capture.Intent != intent || capture.Status != http.StatusOK || !capture.Selected || capture.Incomplete || capture.Duplicate || capture.Error || key == "" || capture.Fingerprint != keyFingerprint(key) {
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
		return err
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
