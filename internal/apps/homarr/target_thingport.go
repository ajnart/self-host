package homarr

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/ajnart/self-host/internal/apps/thingport"
	"github.com/ajnart/self-host/internal/helpers"
)

var thingportMetrics = map[string]string{
	"modelCount": "model_count", "collectionCount": "collection_count",
	"categoryCount": "category_count", "authorCount": "author_count",
}

func thingportTarget() target {
	return target{
		id: "thingport", name: "Thingport", kind: "thingport", internal: fixed("http://thingport:80"),
		secrets: func(_, to *helpers.H) []secret {
			return []secret{{Kind: "apiKey", Value: to.Secret(to.Key("homarr_api_key"))}}
		},
		widgets: []widget{statWidget("modelCount", "collectionCount", "categoryCount", "authorCount")},
		preflight: func(ctx context.Context, from, to *helpers.H) error {
			err := thingport.VerifyHomarr(ctx, to)
			if errors.Is(err, helpers.ErrManual) {
				from.Info("Thingport's Homarr token is a dashboard-only single-user token that this build does not issue. Restore a known token through the private thingport.homarr_api_key input, then run self-host setup thingport and self-host setup homarr; no token is regenerated")
			}
			return err
		},
		verify: verifyThingport,
		stats:  verifyThingportStats,
	}
}

func verifyThingport(ctx context.Context, from, to *helpers.H, integrationID string) error {
	if err := thingport.VerifyHomarr(ctx, to); err != nil {
		return err
	}
	value, err := apiKeyProcedure(ctx, from, "widget.thingport.getLibraryStats", map[string]any{"integrationId": integrationID})
	if err != nil {
		return err
	}
	stats, ok := value.(map[string]any)
	if !ok {
		return errors.New("Homarr did not return Thingport library statistics")
	}
	counts, err := thingport.HomarrCounts(ctx, to)
	if err != nil {
		return err
	}
	for metric, key := range thingportMetrics {
		count, ok := stats[metric].(float64)
		if !ok || count != float64(counts[key]) {
			return errors.New("Homarr returned Thingport counts that differ from the library; retry self-host setup homarr")
		}
	}
	return nil
}

func verifyThingportStats(sample statsSample) error {
	if !validCountStats(sample, slices.Collect(maps.Keys(thingportMetrics))...) {
		return errors.New("Homarr returned incomplete Thingport statistics; missing values are not zero")
	}
	return nil
}
