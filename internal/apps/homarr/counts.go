package homarr

import "math"

func measuredNumber(value any, integral bool) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && (!integral || number == math.Trunc(number) && number <= 1<<53-1)
}

func countValue(value any) (float64, bool) {
	number, ok := measuredNumber(value, false)
	return number, ok && math.Trunc(number) == number
}

func isCount(value any) bool {
	_, ok := countValue(value)
	return ok
}

func validCountStats(sample statsSample, keys ...string) bool {
	units := map[string]string{}
	for _, key := range keys {
		units[key] = "count"
	}
	return validMeasuredStats(sample, units)
}

func validMeasuredStats(sample statsSample, units map[string]string) bool {
	for key, unit := range units {
		number, ok := measuredNumber(sample.values[key], false)
		if !ok || sample.units[key] != unit || unit == "count" && math.Trunc(number) != number {
			return false
		}
	}
	return len(units) > 0
}
