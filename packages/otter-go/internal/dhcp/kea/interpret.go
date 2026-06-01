// Response interpretation — port of Python's
// services/dhcp_push.py:353 _interpret_kea_response. Pure function;
// no I/O.
package kea

import (
	"encoding/json"
	"fmt"
)

// Status is the tri-state outcome of a Kea command. Mirrors the
// strings Python returned ("ok" / "error" / "unsupported") so the
// values can travel through audit logs and history rows unchanged.
type Status string

const (
	// StatusOK = every per-service entry had result code 0 (success)
	// or 3 (empty / not-found, which is success on a DELETE).
	StatusOK Status = "ok"

	// StatusError = at least one per-service entry had result code 1
	// (generic error) or 4 (conflict), or the response shape was bad.
	// Operators see the first error text in the Detail field.
	StatusError Status = "error"

	// StatusUnsupported = at least one entry had result code 2
	// (unsupported — typically the subnet_cmds hook library isn't
	// loaded on this Kea server). Distinct from Error because the
	// fix is operational (load the hook), not a DCIM bug.
	StatusUnsupported Status = "unsupported"
)

// Kea result codes per
// https://kea.readthedocs.io/en/latest/arm/ctrl-channel.html.
//
// We treat 0 + 3 as ok (3 on delete = "wasn't there, that's fine");
// 1, 4, and anything else surface as generic Error; 2 surfaces as
// Unsupported so the UI can render the hook-not-loaded hint.
const (
	keaResultSuccess     = 0
	keaResultGenericErr  = 1
	keaResultUnsupported = 2
	keaResultEmpty       = 3
	keaResultConflict    = 4
)

// InterpretResponse maps Kea's per-service response list to a tri-
// state status + an error string. Multi-service responses are
// scanned for the first non-ok entry — partial success is an error
// because the bundle puller can't safely apply a half-pushed config.
//
// Mirrors services/dhcp_push.py:_interpret_kea_response exactly: the
// returned strings are identical so a Python→Go cutover doesn't
// reshape audit log records mid-flight.
//
// raw is the response body bytes (the post() helper returns them).
// Callers that already json.Unmarshal'd into []any can use
// InterpretEntries to skip the re-decode.
func InterpretResponse(raw []byte) (Status, string) {
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return StatusError, fmt.Sprintf("%s: %s", ErrBadResponseShape, truncate(raw, 256))
	}
	return InterpretEntries(entries)
}

// InterpretEntries is the unmarshaled variant of InterpretResponse.
// Used by callers that need the raw entry list elsewhere (e.g.
// the diff path extracts arguments.subnet4 from the same response).
func InterpretEntries(entries []map[string]any) (Status, string) {
	if len(entries) == 0 {
		return StatusError, fmt.Sprintf("%s: empty response", ErrBadResponseShape)
	}
	var firstErr string
	sawUnsupported := false
	for _, entry := range entries {
		errText, unsupported := classifyEntry(entry)
		if errText == "" {
			continue
		}
		if unsupported {
			sawUnsupported = true
		}
		if firstErr == "" {
			firstErr = errText
		}
	}
	if firstErr == "" {
		return StatusOK, ""
	}
	if sawUnsupported {
		return StatusUnsupported, firstErr
	}
	return StatusError, firstErr
}

// classifyEntry returns (errText, unsupported) for a single per-
// service entry. errText is empty when the entry is a success
// (result 0 or 3); unsupported is true when result is 2 (hook not
// loaded). Missing / null / non-numeric `result` is treated as a
// generic error to match Python's `_interpret_kea_response` — a
// wedged Kea CA that returns `[{"result":null,"text":"daemon offline"}]`
// must surface as Error, not phantom success.
func classifyEntry(entry map[string]any) (errText string, unsupported bool) {
	text, _ := entry["text"].(string)
	code, hasCode := numericResultCode(entry["result"])
	if !hasCode {
		return fmt.Sprintf("kea result=%v: %s", entry["result"], text), false
	}
	switch code {
	case keaResultSuccess, keaResultEmpty:
		return "", false
	case keaResultUnsupported:
		return "unsupported: " + text, true
	default:
		return fmt.Sprintf("kea result=%d: %s", code, text), false
	}
}

// numericResultCode coerces an entry's "result" field (which JSON
// decodes to float64 in map[string]any) to an int. Returns (0,false)
// if absent or non-numeric — caller skips the entry rather than
// treating it as a success.
func numericResultCode(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
	}
	return 0, false
}
