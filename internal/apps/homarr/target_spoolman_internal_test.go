package homarr

import (
	"math"
	"reflect"
	"testing"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/helpers"
)

func SpoolmanTestLink() app.Link { return spoolmanTarget().link() }

func TestSpoolmanDescriptorUsesOnlyTheSupportedCountAndNoSecrets(t *testing.T) {
	descriptor := spoolmanTarget()
	peer := helpers.New(helpers.Config{Service: "spoolman", ContainerPort: 8000})
	if descriptor.id != "spoolman" || descriptor.name != "Spoolman" || descriptor.kind != "spoolman" || descriptor.internal(peer) != "http://spoolman:8000" {
		t.Fatal("Spoolman descriptor must use the exact native kind and Compose endpoint")
	}
	if descriptor.secrets != nil || len(descriptor.credentials(nil, peer)) != 0 || descriptor.preflight == nil || descriptor.verify == nil || descriptor.stats == nil {
		t.Fatal("Spoolman must use the no-secret alternative with native preflight and central Stats verification")
	}
	if len(descriptor.widgets) != 1 || descriptor.widgets[0].kind != "stats" || !reflect.DeepEqual(descriptor.statsMetrics(), []string{"spools"}) {
		t.Fatal("unknown filament weight must not become a required or synthesized metric")
	}
}

func TestSpoolmanCountSemanticsPreserveLegitimateZerosAndUnknownWeight(t *testing.T) {
	for _, count := range []float64{0, 1, 12} {
		values := map[string]any{"spools": count, "remainingWeight": nil}
		sample := statsSample{values: values, units: map[string]string{"spools": "count", "remainingWeight": "grams"}, retryAt: float64(0)}
		if err := verifySpoolmanStats(sample); err != nil {
			t.Fatal(err)
		}
		if value, present := values["remainingWeight"]; !present || value != nil || values["spools"] != count {
			t.Fatal("count validation must not change or fabricate remaining weight")
		}
	}
	for _, value := range []any{nil, "private-inventory-id", false, 1, -1.0, 0.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := verifySpoolmanStats(statsSample{values: map[string]any{"spools": value}, units: map[string]string{"spools": "count"}, retryAt: float64(0)}); err == nil {
			t.Fatal("invalid spool count passed semantic validation")
		}
	}
	for _, unit := range []string{"", "grams", "bytes"} {
		if err := verifySpoolmanStats(statsSample{values: map[string]any{"spools": float64(0)}, units: map[string]string{"spools": unit}, retryAt: float64(0)}); err == nil {
			t.Fatal("a spool count with the wrong unit passed semantic validation")
		}
	}
	for _, retryAt := range []any{nil, "0", false, 1, -1.0, 0.5, 1.0, math.NaN(), math.Inf(1)} {
		if err := verifySpoolmanStats(statsSample{values: map[string]any{"spools": float64(0)}, units: map[string]string{"spools": "count"}, retryAt: retryAt}); err == nil {
			t.Fatal("invalid or pending retry metadata passed a completed Spoolman refresh")
		}
	}
}
