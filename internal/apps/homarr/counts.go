package homarr

import "math"

func countValue(value any) (float64, bool) {
	count, ok := value.(float64)
	return count, ok && count >= 0 && !math.IsNaN(count) && !math.IsInf(count, 0) && math.Trunc(count) == count
}

func validCountStats(sample statsSample, keys ...string) bool {
	for _, key := range keys {
		if _, ok := countValue(sample.values[key]); !ok || sample.units[key] != "count" {
			return false
		}
	}
	return len(keys) > 0
}
