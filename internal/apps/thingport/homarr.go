package thingport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/ajnart/self-host/internal/helpers"
)

func VerifyHomarr(ctx context.Context, h *helpers.H) error {
	if h.Secret(h.Key("homarr_api_key")) == "" {
		return &helpers.ManualError{Hint: tokenHint()}
	}
	if _, err := verifiedOwner(ctx, h); err != nil {
		return err
	}
	_, err := HomarrCounts(ctx, h)
	return err
}

func HomarrCounts(ctx context.Context, h *helpers.H) (map[string]int64, error) {
	key := h.Secret(h.Key("homarr_api_key"))
	if key == "" {
		return nil, &helpers.ManualError{Hint: tokenHint()}
	}
	response, err := h.HTTP(ctx, helpers.Request{
		Path: "/api/dashboard/summary", NoRedirect: true,
		Header: map[string]string{"Authorization": "Bearer " + key},
	})
	if err != nil {
		return nil, err
	}
	if response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden || response.Status >= 300 && response.Status < 400 {
		return nil, &helpers.ManualError{Hint: tokenHint()}
	}
	if response.Status != http.StatusOK {
		return nil, fmt.Errorf("Thingport library summary returned HTTP %d", response.Status)
	}
	var raw map[string]json.RawMessage
	if response.Decode(&raw) != nil {
		return nil, errors.New("Thingport returned an invalid library summary")
	}
	counts := make(map[string]int64)
	for _, name := range []string{"model_count", "collection_count", "category_count", "author_count"} {
		var count *int64
		if json.Unmarshal(raw[name], &count) != nil || count == nil || *count < 0 {
			return nil, errors.New("Thingport returned a missing or invalid library count; missing values are not zero")
		}
		counts[name] = *count
	}
	return counts, nil
}
