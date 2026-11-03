package tandoor

import (
	"bytes"
	"encoding/json"
	"io"
)

type issuanceOutcome struct {
	Complete      bool `json:"complete"`
	Incomplete    bool `json:"incomplete"`
	DecodeError   bool `json:"decodeError"`
	Duplicate     bool `json:"duplicate"`
	Limited       bool `json:"limited"`
	RequestFailed bool `json:"requestFailed"`
	ReportedError bool `json:"reportedError"`
}

func (o issuanceOutcome) acceptable() bool {
	return o.Complete && !o.Incomplete && !o.DecodeError && !o.Duplicate && !o.Limited && !o.RequestFailed && !o.ReportedError
}

func captureTokenResponse(body []byte, status int, requestFailed bool) (string, tokenEvidence) {
	evidence := tokenEvidence{Status: status, Outcome: issuanceOutcome{RequestFailed: requestFailed}}
	const limit = 64 << 10
	if len(body) > limit {
		body = body[:limit]
		evidence.Outcome.Limited = true
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	root, err := decoder.Token()
	if err != nil || root != json.Delim('{') {
		evidence.Outcome.DecodeError = true
		evidence.Outcome.Incomplete = true
		return "", evidence
	}
	key := ""
	seen := map[string]bool{}
	fields := 0
	for decoder.More() {
		fields++
		if fields > 64 {
			evidence.Outcome.Limited = true
			break
		}
		field, err := decoder.Token()
		name, valid := field.(string)
		if err != nil || !valid {
			evidence.Outcome.DecodeError = true
			break
		}
		if seen[name] {
			evidence.Outcome.Duplicate = true
		}
		seen[name] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			evidence.Outcome.DecodeError = true
			break
		}
		switch name {
		case "token", "scope", "expires":
			var value string
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
				evidence.Outcome.DecodeError = true
				continue
			}
			if len(value) > 1024 {
				evidence.Outcome.Limited = true
				continue
			}
			if name == "token" && key == "" {
				key = value
			}
			if name == "scope" && evidence.Scope == "" {
				evidence.Scope = value
			}
			if name == "expires" && evidence.Expires == "" {
				evidence.Expires = value
			}
		case "id":
			var id int64
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &id) != nil {
				evidence.Outcome.DecodeError = true
				continue
			}
			if evidence.ID == 0 {
				evidence.ID = id
			}
		case "status", "error", "errors", "detail":
			evidence.Outcome.ReportedError = true
		}
	}
	closing, err := decoder.Token()
	closed := err == nil && closing == json.Delim('}')
	if !closed {
		evidence.Outcome.DecodeError = true
	}
	if _, err := decoder.Token(); err != io.EOF {
		evidence.Outcome.DecodeError = true
	}
	evidence.Outcome.Incomplete = !closed || !seen["token"] || !seen["id"] || !seen["scope"] || !seen["expires"]
	evidence.Outcome.Complete = !evidence.Outcome.Incomplete && !evidence.Outcome.DecodeError && !evidence.Outcome.Duplicate && !evidence.Outcome.Limited && !evidence.Outcome.RequestFailed && !evidence.Outcome.ReportedError
	return key, evidence
}
