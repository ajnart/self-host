package autobrr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

const HomarrRecoveryHint = "Run self-host setup autobrr, then self-host setup homarr. In Autobrr Settings > API keys preserve existing keys and reconcile only the recorded credential with its known private secret, ownership and issuance evidence. Restore a known matching tuple through private input recovery; empty inputs preserve values. Reinstall replaces Compose and needs separate review and approval. Never delete/adopt a key by name, reset authentication, clear an uncertain issuance to obtain another key, enable TRACE logging or alter trackers, filters or download clients. Autobrr API keys are full-access even with empty scopes."

const homarrKeyName = "self-host Homarr statistics"

var homarrIssuance = helpers.CredentialIssuance{
	SecretName: "homarr_api_key", OwnershipName: "homarr_key_ownership", PendingName: "homarr_key_pending",
}

type keyIntent struct {
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Scopes   []string `json:"scopes"`
	Existing []string `json:"existing"`
}

type keyEvidence struct {
	Status    int             `json:"status"`
	Complete  bool            `json:"complete"`
	Name      json.RawMessage `json:"name"`
	Scopes    json.RawMessage `json:"scopes"`
	CreatedAt json.RawMessage `json:"created_at"`
}

type keyOwnership struct {
	Username    string    `json:"username"`
	Name        string    `json:"name"`
	Scopes      []string  `json:"scopes"`
	CreatedAt   time.Time `json:"created_at"`
	Fingerprint string    `json:"fingerprint"`
}

type apiKey struct {
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	Scopes    []string  `json:"scopes"`
	CreatedAt time.Time `json:"created_at"`
}

func homarrManual(reason string) error {
	return &helpers.ManualError{Hint: reason + " " + HomarrRecoveryHint}
}

func savedOwnership(h *helpers.H) (keyOwnership, error) {
	raw := h.Secret(h.Key("homarr_key_ownership"))
	key := h.Secret(h.Key("homarr_api_key"))
	h.Log.Secret(raw, key)
	var ownership keyOwnership
	if json.Unmarshal([]byte(raw), &ownership) != nil || key == "" || ownership.Username == "" || ownership.Username != h.AdminUser() || ownership.Name != homarrKeyName || ownership.Scopes == nil || len(ownership.Scopes) != 0 || ownership.CreatedAt.IsZero() || ownership.Fingerprint != helpers.Fingerprint(key) {
		return ownership, homarrManual("The saved Autobrr credential and ownership do not match the intended administrator.")
	}
	return ownership, nil
}

func keyInventory(h *helpers.H, body []byte) ([]apiKey, error) {
	h.Log.Secret(string(body))
	var raw []struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(body, &raw)
	for _, key := range raw {
		h.Log.Secret(key.Key)
	}
	var keys []apiKey
	if json.Unmarshal(body, &keys) != nil || keys == nil {
		return nil, homarrManual("Autobrr did not return an unambiguous API-key inventory.")
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if strings.TrimSpace(key.Key) == "" || key.CreatedAt.IsZero() || seen[key.Key] {
			return nil, homarrManual("Autobrr returned incomplete or duplicate API-key identities.")
		}
		seen[key.Key] = true
	}
	return keys, nil
}

func readKeys(ctx context.Context, h *helpers.H, session *helpers.Session) ([]apiKey, error) {
	resp, err := session.HTTP(ctx, helpers.Request{Path: "/api/keys", NoRedirect: true})
	if err != nil || resp.Status != http.StatusOK {
		return nil, homarrManual("Autobrr API-key discovery could not be verified.")
	}
	return keyInventory(h, resp.Body)
}

func matchKey(keys []apiKey, key string, ownership keyOwnership) error {
	found := false
	matchingMetadata := 0
	for _, current := range keys {
		metadataMatches := current.Name == ownership.Name && current.CreatedAt.Equal(ownership.CreatedAt) && current.Scopes != nil && len(current.Scopes) == 0
		if metadataMatches {
			matchingMetadata++
		}
		if current.Key == key {
			if !metadataMatches {
				return homarrManual("The recorded Autobrr key metadata no longer matches its issuance.")
			}
			found = true
		}
	}
	if matchingMetadata > 1 {
		return homarrManual("Multiple Autobrr keys share the recorded creation metadata; the credential identity is ambiguous.")
	}
	if !found {
		return homarrManual("The recorded Autobrr key is absent from the authenticated inventory.")
	}
	return nil
}

func VerifyHomarrInventory(h *helpers.H, body []byte) error {
	ownership, err := savedOwnership(h)
	if err != nil {
		return err
	}
	keys, err := keyInventory(h, body)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key.Key == h.Secret(h.Key("homarr_api_key")) {
			return homarrManual("Homarr returned the expected Autobrr key without redacting it; the saved connection did not prove it used that credential.")
		}
	}
	return matchKey(keys, "[REDACTED]", ownership)
}

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if _, exists, err := homarrIssuance.Pending(h); err != nil {
		return homarrManual("The Autobrr issuance journal needs private recovery.")
	} else if exists {
		return homarrManual("An Autobrr key issuance is still pending; Setup must reconcile it before Homarr can use it.")
	}
	ownership, err := savedOwnership(h)
	if err != nil {
		return err
	}
	return verifyKey(ctx, h, h.Secret(h.Key("homarr_api_key")), ownership)
}

func verifyKey(ctx context.Context, h *helpers.H, key string, ownership keyOwnership) error {
	session, accepted, err := adminSession(ctx, h)
	if err != nil {
		return err
	}
	if !accepted {
		return homarrManual("The intended Autobrr administrator cannot sign in.")
	}
	resp, err := session.HTTP(ctx, helpers.Request{Path: "/api/config", NoRedirect: true})
	if err != nil {
		return homarrManual("Autobrr logging safety could not be verified before sending its key.")
	}
	if err := verifyConfiguration(resp); err != nil {
		return err
	}
	resp, err = h.HTTP(ctx, helpers.Request{Path: "/api/config", NoRedirect: true, Header: map[string]string{"X-API-Token": key}})
	if err != nil || resp.Status != http.StatusOK {
		return homarrManual("Autobrr did not accept its recorded statistics credential.")
	}
	if err := verifyConfiguration(resp); err != nil {
		return err
	}
	keys, err := readKeys(ctx, h, session)
	if err != nil {
		return err
	}
	if err := matchKey(keys, key, ownership); err != nil {
		return err
	}
	return verifyStatistics(ctx, h, key)
}

func verifyConfiguration(resp helpers.Response) error {
	var config struct {
		Version  string `json:"version"`
		Database string `json:"database"`
		Port     int    `json:"port"`
		LogLevel string `json:"log_level"`
	}
	if resp.Status != http.StatusOK || resp.Decode(&config) != nil || strings.TrimSpace(config.Version) == "" || config.Database != "sqlite" && config.Database != "postgres" || config.Port < 1 || config.Port > 65535 || strings.TrimSpace(config.LogLevel) == "" {
		return homarrManual("Autobrr did not return its authenticated configuration identity.")
	}
	if strings.EqualFold(strings.TrimSpace(config.LogLevel), "TRACE") {
		return homarrManual("Autobrr TRACE logging exposes API keys; review and explicitly change that logging choice before using Homarr.")
	}
	return nil
}

func verifyStatistics(ctx context.Context, h *helpers.H, key string) error {
	header := map[string]string{"X-API-Token": key}
	resp, err := h.HTTP(ctx, helpers.Request{Path: "/api/release/stats", NoRedirect: true, Header: header})
	if err != nil || resp.Status != http.StatusOK {
		return homarrManual("Autobrr release statistics could not be authenticated.")
	}
	var stats struct {
		Approved *int64 `json:"push_approved_count"`
		Rejected *int64 `json:"push_rejected_count"`
	}
	if resp.Decode(&stats) != nil || stats.Approved == nil || stats.Rejected == nil || *stats.Approved < 0 || *stats.Rejected < 0 {
		return homarrManual("Autobrr returned incomplete release statistics; missing values are not zeros.")
	}
	for _, path := range []string{"/api/filters", "/api/release/indexers"} {
		resp, err := h.HTTP(ctx, helpers.Request{Path: path, NoRedirect: true, Header: header})
		if err != nil || resp.Status != http.StatusOK {
			return homarrManual("Autobrr statistics lists could not be authenticated.")
		}
		var values []json.RawMessage
		if resp.Decode(&values) != nil || values == nil {
			return homarrManual("Autobrr did not return explicit filter and indexer statistics lists.")
		}
		for _, value := range values {
			if path == "/api/filters" {
				var filter struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				}
				if json.Unmarshal(value, &filter) != nil || filter.ID <= 0 || strings.TrimSpace(filter.Name) == "" {
					return homarrManual("Autobrr returned incomplete filter statistics identities.")
				}
			} else {
				var indexer string
				if json.Unmarshal(value, &indexer) != nil || strings.TrimSpace(indexer) == "" {
					return homarrManual("Autobrr returned incomplete release-indexer statistics identities.")
				}
			}
		}
	}
	return nil
}

func prepareHomarrKey(ctx context.Context, h *helpers.H) error {
	pending, exists, err := homarrIssuance.Pending(h)
	if err != nil {
		return homarrManual("The Autobrr key issuance journal cannot be reconciled safely.")
	}
	if exists {
		return recoverHomarrKey(ctx, h, pending)
	}
	if h.Secret(h.Key("homarr_api_key")) != "" || h.Secret(h.Key("homarr_key_ownership")) != "" {
		return VerifyHomarr(ctx, h)
	}
	session, accepted, err := adminSession(ctx, h)
	if err != nil {
		return err
	}
	if !accepted {
		return homarrManual("The intended Autobrr administrator cannot prepare a key.")
	}
	config, err := session.HTTP(ctx, helpers.Request{Path: "/api/config", NoRedirect: true})
	if err != nil {
		return homarrManual("Autobrr logging safety could not be verified before issuing a key.")
	}
	if err := verifyConfiguration(config); err != nil {
		return err
	}
	keys, err := readKeys(ctx, h, session)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key.Name == homarrKeyName {
			return homarrManual("An unowned Autobrr key already uses the statistics name; it was preserved.")
		}
	}
	if len(keys) != 0 {
		return homarrManual("Autobrr's nonempty API-key list may contain only validation-cache entries, so absence of the statistics name cannot authorize issuance. Preserve all keys and restore a known matching private credential/ownership tuple; do not clear the cache or restart to force discovery.")
	}
	intent := keyIntent{Username: h.AdminUser(), Name: homarrKeyName, Scopes: []string{}, Existing: []string{}}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return errors.New("Autobrr key intent could not be encoded")
	}
	if err := homarrIssuance.Begin(h, encoded); err != nil {
		return err
	}
	resp, requestErr := session.HTTP(ctx, helpers.Request{Method: http.MethodPost, Path: "/api/keys", NoRedirect: true,
		JSON: map[string]any{"name": homarrKeyName, "scopes": []string{}}})
	if requestErr != nil {
		return homarrManual("Autobrr key issuance was interrupted; no replacement will be issued.")
	}
	issued := extractKey(h, resp)
	if err := homarrIssuance.Capture(h, issued); err != nil {
		return err
	}
	return recoverHomarrKey(ctx, h, helpers.PendingCredential{Intent: encoded, Issued: &issued})
}

func extractKey(h *helpers.H, resp helpers.Response) helpers.CredentialEvidence {
	h.Log.Secret(string(resp.Body))
	decoder := json.NewDecoder(bytes.NewReader(resp.Body))
	issued := helpers.CredentialEvidence{}
	evidence := keyEvidence{Status: resp.Status}
	token, err := decoder.Token()
	if err == nil && token == json.Delim('{') {
		seen := map[string]bool{}
		valid := true
		for decoder.More() {
			token, err := decoder.Token()
			field, ok := token.(string)
			var value json.RawMessage
			if err != nil || !ok || seen[field] || decoder.Decode(&value) != nil {
				valid = false
				break
			}
			seen[field] = true
			switch field {
			case "key":
				_ = json.Unmarshal(value, &issued.Secret)
				h.Log.Secret(issued.Secret)
			case "name":
				evidence.Name = value
			case "scopes":
				evidence.Scopes = value
			case "created_at":
				evidence.CreatedAt = value
			}
		}
		last, endErr := decoder.Token()
		_, tailErr := decoder.Token()
		evidence.Complete = valid && endErr == nil && last == json.Delim('}') && tailErr == io.EOF
	}
	issued.Data, _ = json.Marshal(evidence)
	return issued
}

func recoverHomarrKey(ctx context.Context, h *helpers.H, pending helpers.PendingCredential) error {
	if pending.Issued == nil || pending.Issued.Secret == "" {
		return homarrManual("An Autobrr issuance intent has no recoverable one-time secret.")
	}
	var intent keyIntent
	var evidence keyEvidence
	if json.Unmarshal(pending.Intent, &intent) != nil || intent.Username == "" || intent.Username != h.AdminUser() || intent.Name != homarrKeyName || intent.Scopes == nil || len(intent.Scopes) != 0 || intent.Existing == nil || json.Unmarshal(pending.Issued.Data, &evidence) != nil {
		return homarrManual("The captured Autobrr issuance intent or evidence is incomplete.")
	}
	ownership := keyOwnership{Username: intent.Username, Fingerprint: helpers.Fingerprint(pending.Issued.Secret)}
	if evidence.Status != http.StatusCreated || !evidence.Complete || json.Unmarshal(evidence.Name, &ownership.Name) != nil || json.Unmarshal(evidence.Scopes, &ownership.Scopes) != nil || json.Unmarshal(evidence.CreatedAt, &ownership.CreatedAt) != nil || ownership.Name != homarrKeyName || ownership.Scopes == nil || len(ownership.Scopes) != 0 || ownership.CreatedAt.IsZero() || slices.Contains(intent.Existing, ownership.Fingerprint) {
		return homarrManual("Autobrr did not confirm an unambiguous new statistics key; its captured evidence was preserved.")
	}
	if err := homarrIssuance.Capture(h, *pending.Issued); err != nil {
		return err
	}
	if err := verifyKey(ctx, h, pending.Issued.Secret, ownership); err != nil {
		return err
	}
	encoded, err := json.Marshal(ownership)
	if err != nil {
		return errors.New("Autobrr key ownership could not be encoded")
	}
	return homarrIssuance.Commit(h, encoded)
}
