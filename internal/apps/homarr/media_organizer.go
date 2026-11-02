package homarr

import (
	"context"
	"errors"

	"github.com/ajnart/self-host/internal/helpers"
)

type mediaOrganizerSample struct {
	missing      []any
	missingCount float64
	queued       []any
	queuedCount  float64
}

func readMediaOrganizer(ctx context.Context, from *helpers.H, integrationID string) (mediaOrganizerSample, error) {
	const failure = "Homarr did not return complete Media Missing data from the expected integration; check its credentials and retry self-host setup homarr"
	if integrationID == "" {
		return mediaOrganizerSample{}, errors.New(failure)
	}
	value, err := apiKeyProcedure(ctx, from, "widget.mediaOrganizer.getData", map[string]any{
		"integrationIds": []string{integrationID}, "pageSize": 50,
	})
	if err != nil {
		return mediaOrganizerSample{}, errors.New(failure)
	}
	results, ok := value.([]any)
	if !ok || len(results) != 1 {
		return mediaOrganizerSample{}, errors.New(failure)
	}
	result, ok := results[0].(map[string]any)
	if !ok || stringField(result, "integrationId") != integrationID || result["error"] != nil {
		return mediaOrganizerSample{}, errors.New(failure)
	}
	missing, hasMissing := result["missing"].([]any)
	queued, hasQueued := result["queued"].([]any)
	missingCount, validMissing := countValue(result["missingCount"])
	queuedCount, validQueued := countValue(result["queuedCount"])
	if !hasMissing || !hasQueued || !validMissing || !validQueued || len(missing) > 50 || len(queued) > 50 || float64(len(missing)) > missingCount || float64(len(queued)) > queuedCount {
		return mediaOrganizerSample{}, errors.New(failure)
	}
	return mediaOrganizerSample{missing: missing, missingCount: missingCount, queued: queued, queuedCount: queuedCount}, nil
}
