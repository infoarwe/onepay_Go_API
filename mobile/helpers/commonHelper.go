package helper

import "strconv"

func ToInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int32:
		return int(val)
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		i, _ := strconv.Atoi(val)
		return i
	default:
		return 0
	}
}

// ToInt64 tolerantly converts whatever numeric-ish BSON/JSON type a field
// turned out to be, same rationale as ToInt but for ids that may exceed
// 32-bit range (driver/trip ids in the legacy schema).
func ToInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int:
		return int64(val)
	case int32:
		return int64(val)
	case int64:
		return val
	case float64:
		return int64(val)
	case string:
		i, _ := strconv.ParseInt(val, 10, 64)
		return i
	default:
		return 0
	}
}

// ToFloat64 tolerantly converts whatever numeric-ish BSON/JSON type a field
// turned out to be - used for lat/lng/distance fields the mobile client
// sends as strings but that get treated as floats at point of use.
func ToFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}

// ToString tolerantly stringifies whatever BSON/JSON type a field turned
// out to be, for fields the legacy handler treats as strings regardless of
// stored type.
func ToString(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		return ""
	}
}

func Contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}
