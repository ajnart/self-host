package helpers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type CredentialIssuance struct {
	SecretName    string
	OwnershipName string
	PendingName   string
}

type CredentialEvidence struct {
	Secret string          `json:"secret"`
	Data   json.RawMessage `json:"data"`
}

type PendingCredential struct {
	Intent json.RawMessage
	Issued *CredentialEvidence
}

type credentialEnvelope struct {
	Version int                 `json:"version"`
	Intent  json.RawMessage     `json:"intent"`
	Issued  *CredentialEvidence `json:"issued"`
}

func (c CredentialIssuance) Pending(h *H) (PendingCredential, bool, error) {
	if err := c.validate(h); err != nil {
		return PendingCredential{}, false, err
	}
	raw := h.Secret(h.Key(c.PendingName))
	h.Log.Secret(raw)
	if raw == "" {
		return PendingCredential{}, false, nil
	}
	var extracted credentialEnvelope
	_ = json.Unmarshal([]byte(raw), &extracted)
	if extracted.Issued != nil {
		h.Log.Secret(extracted.Issued.Secret, string(extracted.Issued.Data))
	}
	fields, ok := credentialFields([]byte(raw), "version", "intent", "issued")
	if !ok || len(fields) != 3 || json.Unmarshal(fields["version"], &extracted.Version) != nil || extracted.Version != 1 {
		return PendingCredential{}, true, credentialManual(h, "The credential issuance journal cannot be read safely.")
	}
	intent, ok := credentialObject(fields["intent"])
	if !ok {
		return PendingCredential{}, true, credentialManual(h, "The credential issuance intent is incomplete.")
	}
	pending := PendingCredential{Intent: intent}
	if bytes.Equal(bytes.TrimSpace(fields["issued"]), []byte("null")) {
		return pending, true, nil
	}
	issued, ok := credentialFields(fields["issued"], "secret", "data")
	if !ok || len(issued) != 2 {
		return PendingCredential{}, true, credentialManual(h, "The captured credential evidence is incomplete.")
	}
	var evidence CredentialEvidence
	if json.Unmarshal(issued["secret"], &evidence.Secret) != nil || bytes.Equal(bytes.TrimSpace(issued["secret"]), []byte("null")) {
		return PendingCredential{}, true, credentialManual(h, "The captured credential cannot be read safely.")
	}
	h.Log.Secret(evidence.Secret)
	if !bytes.Equal(bytes.TrimSpace(issued["data"]), []byte("null")) {
		evidence.Data, ok = credentialObject(issued["data"])
		if !ok {
			return PendingCredential{}, true, credentialManual(h, "The captured credential evidence cannot be read safely.")
		}
	}
	pending.Issued = &evidence
	return pending, true, nil
}

func (c CredentialIssuance) Begin(h *H, intent json.RawMessage) error {
	if err := c.validate(h); err != nil {
		return err
	}
	for _, name := range []string{c.PendingName, c.SecretName, c.OwnershipName} {
		value := h.Secret(h.Key(name))
		h.Log.Secret(value)
		if value != "" {
			return credentialManual(h, "Existing credential records prevent another issuance.")
		}
	}
	intent, ok := credentialObject(intent)
	if !ok {
		return credentialManual(h, "A valid credential issuance intent is required.")
	}
	return c.savePending(h, PendingCredential{Intent: intent})
}

func (c CredentialIssuance) Capture(h *H, issued CredentialEvidence) error {
	h.Log.Secret(issued.Secret, string(issued.Data))
	pending, exists, err := c.Pending(h)
	if err != nil {
		return err
	}
	if !exists {
		return credentialManual(h, "No credential issuance intent was recorded.")
	}
	if len(issued.Data) > 0 {
		var ok bool
		issued.Data, ok = credentialObject(issued.Data)
		if !ok {
			return credentialManual(h, "The selected credential evidence must be a JSON object.")
		}
	}
	if pending.Issued != nil && (pending.Issued.Secret != issued.Secret || !bytes.Equal(pending.Issued.Data, issued.Data)) {
		return credentialManual(h, "A different captured credential already needs recovery.")
	}
	pending.Issued = &issued
	return c.savePending(h, pending)
}

func (c CredentialIssuance) Commit(h *H, ownership json.RawMessage) error {
	h.Log.Secret(string(ownership))
	pending, exists, err := c.Pending(h)
	if err != nil {
		return err
	}
	if !exists || pending.Issued == nil || strings.TrimSpace(pending.Issued.Secret) == "" {
		return credentialManual(h, "A captured credential is required before completing issuance.")
	}
	ownership, ok := credentialObject(ownership)
	if !ok {
		return credentialManual(h, "Verified credential ownership must be a JSON object.")
	}
	active := h.Secret(h.Key(c.SecretName))
	current := h.Secret(h.Key(c.OwnershipName))
	h.Log.Secret(active, current)
	if active != "" && active != pending.Issued.Secret {
		return credentialManual(h, "The saved credential conflicts with captured issuance evidence.")
	}
	if current != "" {
		compacted, valid := credentialObject([]byte(current))
		if !valid || !bytes.Equal(compacted, ownership) {
			return credentialManual(h, "The saved ownership conflicts with captured issuance evidence.")
		}
	}
	if err := c.savePending(h, pending); err != nil {
		return err
	}
	if err := h.SetSecret(h.Key(c.OwnershipName), string(ownership)); err != nil {
		return errors.New("credential ownership could not be saved; repair private storage and retry Setup without issuing another credential")
	}
	if err := h.SetSecret(h.Key(c.SecretName), pending.Issued.Secret); err != nil {
		return errors.New("the captured credential could not be saved; repair private storage and retry Setup without issuing another credential")
	}
	if err := h.SetSecret(h.Key(c.PendingName), ""); err != nil {
		return errors.New("credential issuance completion could not be saved; repair private storage and retry Setup without issuing another credential")
	}
	return nil
}

func (c CredentialIssuance) validate(h *H) error {
	seen := map[string]bool{}
	for _, name := range []string{c.SecretName, c.OwnershipName, c.PendingName} {
		if name == "" || name[0] < 'a' || name[0] > 'z' || seen[name] {
			return credentialManual(h, "Credential issuance requires distinct App-local secret names.")
		}
		for _, ch := range name {
			if ch != '_' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
				return credentialManual(h, "Credential issuance requires distinct App-local secret names.")
			}
		}
		seen[name] = true
	}
	return nil
}

func (c CredentialIssuance) savePending(h *H, pending PendingCredential) error {
	encoded, err := json.Marshal(credentialEnvelope{Version: 1, Intent: pending.Intent, Issued: pending.Issued})
	if err != nil {
		return credentialManual(h, "The credential issuance evidence could not be encoded safely.")
	}
	if err := h.SetSecret(h.Key(c.PendingName), string(encoded)); err != nil {
		return errors.New("credential issuance evidence could not be saved; repair private storage and retry Setup without issuing another credential")
	}
	return nil
}

func credentialManual(h *H, reason string) error {
	return &ManualError{Hint: reason + " Preserve the private credential and issuance records, reconcile only the recorded provider credential, then run self-host setup " + h.App + ". Do not clear a pending record to issue a replacement."}
}

func credentialObject(raw []byte) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	encoded, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		return nil, false
	}
	return json.RawMessage(encoded), true
}

func credentialFields(raw []byte, allowed ...string) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || fields[name] != nil {
			return nil, false
		}
		valid := false
		for _, key := range allowed {
			valid = valid || key == name
		}
		var value json.RawMessage
		if !valid || decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	if last, err := decoder.Token(); err != nil || last != json.Delim('}') {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return fields, true
}
