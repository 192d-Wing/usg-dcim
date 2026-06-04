package nicreg

import (
	"strconv"
	"strings"
	"time"
)

// Payload values arrive as the JSON-decoded any-tree: strings, float64 for
// numbers, bool, []any for lists. These helpers narrow them to the Go types
// the sqlc detail-insert params expect, treating absent/empty as NULL.

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return ""
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// reqString returns the trimmed string for a NOT NULL column.
func reqString(p map[string]any, key string) string {
	return strings.TrimSpace(asString(p[key]))
}

// ptrString returns a *string for a nullable column: nil when absent/blank.
func ptrString(p map[string]any, key string) *string {
	s := strings.TrimSpace(asString(p[key]))
	if s == "" {
		return nil
	}
	return &s
}

func ptrInt32(p map[string]any, key string) *int32 {
	f, ok := asFloat(p[key])
	if !ok {
		return nil
	}
	n := int32(f)
	return &n
}

func ptrInt16(p map[string]any, key string) *int16 {
	f, ok := asFloat(p[key])
	if !ok {
		return nil
	}
	n := int16(f)
	return &n
}

func ptrInt64(p map[string]any, key string) *int64 {
	f, ok := asFloat(p[key])
	if !ok {
		return nil
	}
	n := int64(f)
	return &n
}

func boolVal(p map[string]any, key string) bool {
	b, _ := p[key].(bool)
	return b
}

// stringSlice narrows a []any of strings (repeat fields) to []string, dropping
// blank entries.
func stringSlice(p map[string]any, key string) []string {
	arr, ok := p[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, el := range arr {
		s := strings.TrimSpace(asString(el))
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseDateString parses a date string accepting yyyymmdd (NIC format),
// yyyy-mm-dd, or RFC3339. Returns (zero, false) if blank/unparseable.
func parseDateString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"20060102", "2006-01-02", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// dateVal parses a date payload field. Returns (zero, false) if unparseable.
func dateVal(p map[string]any, key string) (time.Time, bool) {
	return parseDateString(asString(p[key]))
}
