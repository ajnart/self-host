package homarr

import (
	"context"
	"errors"
	"time"

	"github.com/ajnart/self-host/internal/helpers"
)

func readCalendarEvents(ctx context.Context, from *helpers.H, integrationID, kind string) ([]any, error) {
	const failure = "Homarr did not return the expected integration's calendar; check its credentials and retry self-host setup homarr"
	if integrationID == "" {
		return nil, errors.New(failure)
	}
	now := from.Now().UTC()
	value, err := apiKeyProcedure(ctx, from, "widget.calendar.findAllEvents", map[string]any{
		"integrationIds": []string{integrationID}, "year": now.Year(), "month": int(now.Month()),
		"releaseType": []string{}, "showUnmonitored": true,
	})
	if err != nil {
		return nil, errors.New(failure)
	}
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return nil, errors.New(failure)
	}
	result, ok := results[0].(map[string]any)
	if !ok || result["error"] != nil {
		return nil, errors.New(failure)
	}
	integration, ok := result["integration"].(map[string]any)
	if !ok || stringField(integration, "id") != integrationID || stringField(integration, "kind") != kind {
		return nil, errors.New(failure)
	}
	events, ok := result["events"].([]any)
	if !ok {
		return nil, errors.New(failure)
	}
	for _, value := range events {
		event, ok := value.(map[string]any)
		if !ok || stringField(event, "title") == "" {
			return nil, errors.New(failure)
		}
		if _, err := time.Parse(time.RFC3339Nano, stringField(event, "startDate")); err != nil {
			return nil, errors.New(failure)
		}
	}
	return events, nil
}
