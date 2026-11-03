package karakeep

import (
	"bytes"
	"encoding/json"
	"io"
)

type homarrCreationCapture struct {
	secret   string
	evidence homarrEvidence
}

func captureHomarrCreation(body []byte) (string, homarrEvidence) {
	const limit = 64 << 10
	capture := homarrCreationCapture{evidence: homarrEvidence{Version: 1, Complete: len(body) <= limit}}
	decoder := json.NewDecoder(bytes.NewReader(body[:min(len(body), limit)]))
	if !capture.readObject(decoder, 0) {
		capture.evidence.Complete = false
	}
	if _, err := decoder.Token(); err != io.EOF {
		capture.evidence.Complete = false
	}
	capture.evidence.Created.KeyID, _ = homarrKeyID(capture.secret)
	return capture.secret, capture.evidence
}

func (c *homarrCreationCapture) readObject(decoder *json.Decoder, level int) bool {
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		duplicate := seen[name]
		seen[name] = true
		if duplicate {
			c.evidence.Complete = false
		}
		if level < 3 && name == []string{"result", "data", "json"}[level] && !duplicate {
			if !c.readObject(decoder, level+1) {
				return false
			}
			continue
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return false
		}
		if name == "error" && hasBodyError(raw) {
			c.evidence.Rejected = true
		}
		if level == 3 && !duplicate && !c.readField(name, raw) {
			c.evidence.Complete = false
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	if level < 3 {
		return seen[[]string{"result", "data", "json"}[level]]
	}
	return seen["id"] && seen["name"] && seen["key"] && seen["scopes"]
}

func (c *homarrCreationCapture) readField(name string, raw json.RawMessage) bool {
	if name == "scopes" {
		var scopes []string
		if json.Unmarshal(raw, &scopes) != nil || len(scopes) == 0 || len(scopes) > 16 {
			return false
		}
		for _, scope := range scopes {
			if len(scope) == 0 || len(scope) > 64 {
				return false
			}
		}
		c.evidence.Created.Scopes = scopes
		return true
	}
	var destination *string
	switch name {
	case "id":
		destination = &c.evidence.Created.ID
	case "name":
		destination = &c.evidence.Created.Name
	case "key":
		destination = &c.secret
	default:
		return true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) == 0 || len(value) > 256 {
		return false
	}
	*destination = value
	return true
}
