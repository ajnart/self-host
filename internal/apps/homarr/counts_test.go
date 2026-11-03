package homarr

import (
	"math"
	"testing"
)

func TestHomarrCountValuesAndUnits(t *testing.T) {
	for _, value := range []any{nil, "0", false, 1, -1.0, 0.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, ok := countValue(value); ok {
			t.Fatalf("accepted invalid count %v", value)
		}
	}
	for _, value := range []float64{0, 1, 1e20} {
		if count, ok := countValue(value); !ok || count != value {
			t.Fatal("valid count was changed or rejected")
		}
	}
	sample := statsSample{values: map[string]any{"a": float64(0)}, units: map[string]string{"a": "count"}}
	if !validCountStats(sample, "a") || validCountStats(sample) || validCountStats(sample, "missing") {
		t.Fatal("count metric selection was not validated")
	}
	sample.units["a"] = "bytes"
	if validCountStats(sample, "a") {
		t.Fatal("wrong unit accepted")
	}
}
