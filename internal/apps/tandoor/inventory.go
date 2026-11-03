package tandoor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

type inventoryEvidence struct {
	Count      int       `json:"count"`
	MaxID      int64     `json:"maxId"`
	ObservedAt time.Time `json:"observedAt"`
}

func validInventoryEvidence(h *helpers.H, evidence *inventoryEvidence) bool {
	return evidence != nil && evidence.Count >= 0 && evidence.MaxID >= int64(evidence.Count) && (evidence.Count == 0) == (evidence.MaxID == 0) && !evidence.ObservedAt.IsZero() && !evidence.ObservedAt.After(h.Now())
}

type tokenMetadata struct {
	ID      int64     `json:"id"`
	Scope   string    `json:"scope"`
	Expires time.Time `json:"expires"`
	Token   string    `json:"token"`
}

func (m *tokenMetadata) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("invalid Tandoor token metadata")
	}
	seen := map[string]bool{}
	for decoder.More() {
		field, err := decoder.Token()
		name, valid := field.(string)
		name = strings.ToLower(name)
		if err != nil || !valid || seen[name] {
			return errors.New("ambiguous Tandoor token metadata")
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return errors.New("invalid Tandoor token metadata")
		}
	}
	type metadata tokenMetadata
	return json.Unmarshal(raw, (*metadata)(m))
}

func supportedScopes(value string) bool {
	seen := map[string]bool{}
	for _, scope := range strings.Fields(value) {
		if seen[scope] {
			return false
		}
		switch scope {
		case "read", "write", "bookmarklet", "mealplan":
			seen[scope] = true
		default:
			return false
		}
	}
	return len(seen) > 0
}

func readTokenInventory(ctx context.Context, h *helpers.H, header map[string]string) ([]tokenMetadata, error) {
	response, err := h.HTTP(ctx, helpers.Request{Path: "/api/access-token/", Header: header, NoRedirect: true})
	if err != nil {
		return nil, errors.New("Tandoor token inventory request failed")
	}
	if response.Status == http.StatusTooManyRequests {
		return nil, &helpers.ManualError{Hint: throttleHint}
	}
	if response.Status >= 300 && response.Status < 500 {
		return nil, tokenManual()
	}
	if response.Status != http.StatusOK {
		return nil, fmt.Errorf("Tandoor token inventory returned HTTP %d", response.Status)
	}
	var inventory []tokenMetadata
	if json.Unmarshal(response.Body, &inventory) != nil || inventory == nil {
		return nil, tokenManual()
	}
	seen := map[int64]bool{}
	for _, token := range inventory {
		if token.ID <= 0 || seen[token.ID] || token.Expires.IsZero() || !supportedScopes(token.Scope) {
			return nil, tokenManual()
		}
		seen[token.ID] = true
	}
	return inventory, nil
}
