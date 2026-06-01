package kea

import (
	"strings"
	"testing"
)

func TestInterpretResponse_AllOK(t *testing.T) {
	got, errStr := InterpretResponse([]byte(`[{"result":0,"text":"ok"}]`))
	if got != StatusOK {
		t.Errorf("status: got %q, want %q", got, StatusOK)
	}
	if errStr != "" {
		t.Errorf("error string should be empty on OK; got %q", errStr)
	}
}

func TestInterpretResponse_EmptyTreatedAsOK_OnDelete(t *testing.T) {
	// result=3 means "wasn't there" — fine on a DELETE.
	got, _ := InterpretResponse([]byte(`[{"result":3,"text":"not found"}]`))
	if got != StatusOK {
		t.Errorf("result=3 should map to OK on delete; got %q", got)
	}
}

func TestInterpretResponse_GenericError(t *testing.T) {
	got, errStr := InterpretResponse([]byte(`[{"result":1,"text":"already exists"}]`))
	if got != StatusError {
		t.Errorf("status: got %q, want Error", got)
	}
	if !strings.Contains(errStr, "kea result=1") || !strings.Contains(errStr, "already exists") {
		t.Errorf("error string should include code+text; got %q", errStr)
	}
}

func TestInterpretResponse_UnsupportedTakesPrecedenceOnHookMissing(t *testing.T) {
	// result=2 is the hook-not-loaded case. Surface as Unsupported
	// even when mixed with OKs, so the UI can show "load subnet_cmds".
	got, errStr := InterpretResponse([]byte(`[{"result":2,"text":"command not supported"},{"result":0}]`))
	if got != StatusUnsupported {
		t.Errorf("status: got %q, want Unsupported", got)
	}
	if !strings.Contains(errStr, "unsupported:") {
		t.Errorf("error should start with 'unsupported:'; got %q", errStr)
	}
}

func TestInterpretResponse_GenericErrorOverridesUnsupportedWhenBothPresent(t *testing.T) {
	// First non-ok entry wins for the text. Python's helper has the
	// same first-error semantics — pin it so the Go port matches.
	got, errStr := InterpretResponse([]byte(`[{"result":1,"text":"first error"},{"result":2,"text":"hook missing"}]`))
	// sawUnsupported is true → status surfaces as Unsupported even
	// though the text is from the generic-error entry. Matches Python.
	if got != StatusUnsupported {
		t.Errorf("status: got %q, want Unsupported (sawUnsupported wins)", got)
	}
	if !strings.Contains(errStr, "first error") {
		t.Errorf("error text should be from the first non-OK entry; got %q", errStr)
	}
}

func TestInterpretResponse_ConflictMapsToError(t *testing.T) {
	got, _ := InterpretResponse([]byte(`[{"result":4,"text":"conflict on subnet id"}]`))
	if got != StatusError {
		t.Errorf("result=4 (conflict) should map to Error; got %q", got)
	}
}

func TestInterpretResponse_BadShapeNotAList(t *testing.T) {
	got, errStr := InterpretResponse([]byte(`{"result":0}`))
	if got != StatusError {
		t.Errorf("non-list response should map to Error; got %q", got)
	}
	if !strings.Contains(errStr, "unexpected kea response shape") {
		t.Errorf("error should mention shape; got %q", errStr)
	}
}

func TestInterpretResponse_EmptyList(t *testing.T) {
	got, errStr := InterpretResponse([]byte(`[]`))
	if got != StatusError {
		t.Errorf("empty list should map to Error; got %q", got)
	}
	if !strings.Contains(errStr, "empty response") {
		t.Errorf("error should mention emptiness; got %q", errStr)
	}
}

func TestInterpretResponse_BadJSON(t *testing.T) {
	got, errStr := InterpretResponse([]byte(`not json {`))
	if got != StatusError {
		t.Errorf("malformed JSON should map to Error; got %q", got)
	}
	if !strings.Contains(errStr, "unexpected kea response shape") {
		t.Errorf("error should mention shape on bad JSON; got %q", errStr)
	}
}

func TestInterpretResponse_MultiServicePartialSuccessIsError(t *testing.T) {
	// dhcp4 ok, dhcp6 fails — partial success is still error.
	// Python documents this at services/dhcp_push.py:367 ("Multi-
	// service responses get scanned for the first non-ok entry").
	got, errStr := InterpretResponse([]byte(`[{"result":0},{"result":1,"text":"v6 borked"}]`))
	if got != StatusError {
		t.Errorf("partial success should map to Error; got %q", got)
	}
	if !strings.Contains(errStr, "v6 borked") {
		t.Errorf("error should carry the failing entry's text; got %q", errStr)
	}
}

func TestInterpretEntries_AvoidsReDecode(t *testing.T) {
	// Callers that already unmarshaled (e.g. the diff path needs
	// arguments.subnet4 from the SAME response) skip the re-decode.
	entries := []map[string]any{{"result": float64(0), "text": "ok"}}
	got, _ := InterpretEntries(entries)
	if got != StatusOK {
		t.Errorf("InterpretEntries: got %q, want OK", got)
	}
}

func TestNumericResultCode_AcceptsFloat64AndIntAndJsonNumber(t *testing.T) {
	cases := []any{float64(2), 2, int64(2)}
	for _, v := range cases {
		n, ok := numericResultCode(v)
		if !ok || n != 2 {
			t.Errorf("numericResultCode(%T %v): got (%d,%v), want (2,true)", v, v, n, ok)
		}
	}
}

func TestNumericResultCode_RejectsMissingAndStringValues(t *testing.T) {
	// Reject = (0, false). InterpretEntries handles the missing/null/
	// non-numeric case at a higher level: it treats hasCode==false as
	// a generic error, matching Python's behavior (see the
	// ResultNullSurfacesAsError test below).
	for _, v := range []any{nil, "two", true} {
		if _, ok := numericResultCode(v); ok {
			t.Errorf("numericResultCode(%T %v) should reject non-numeric", v, v)
		}
	}
}

// ---- Python-parity for missing/null/non-numeric `result` ----
// Originally these were silently swallowed as StatusOK (code review
// caught it on PR 1). Python's `_interpret_kea_response` falls
// through to the generic-error branch when `entry.get("result")` is
// None, returning ("error", "kea result=None: <text>"). A Kea CA
// that wedges into returning a null result for a real backend
// failure must surface as Error so the push orchestrator records
// the failure rather than celebrating a phantom success.

func TestInterpretResponse_ResultNullSurfacesAsError(t *testing.T) {
	got, errStr := InterpretResponse([]byte(`[{"result":null,"text":"daemon offline"}]`))
	if got != StatusError {
		t.Errorf("result:null should map to Error (Python parity); got %q", got)
	}
	if !strings.Contains(errStr, "daemon offline") {
		t.Errorf("error should carry the entry text; got %q", errStr)
	}
}

func TestInterpretResponse_MissingResultKeySurfacesAsError(t *testing.T) {
	// `[{"text":"weird"}]` — partial/corrupt response shape.
	got, errStr := InterpretResponse([]byte(`[{"text":"weird"}]`))
	if got != StatusError {
		t.Errorf("missing result should map to Error; got %q", got)
	}
	if !strings.Contains(errStr, "weird") {
		t.Errorf("error should carry the text field; got %q", errStr)
	}
}

func TestInterpretResponse_NonNumericResultSurfacesAsError(t *testing.T) {
	// Malformed proxy / buggy Kea version returns a string result.
	got, errStr := InterpretResponse([]byte(`[{"result":"ERR","text":"bad"}]`))
	if got != StatusError {
		t.Errorf("non-numeric result should map to Error; got %q", got)
	}
	if !strings.Contains(errStr, "bad") {
		t.Errorf("error should carry the text field; got %q", errStr)
	}
}
