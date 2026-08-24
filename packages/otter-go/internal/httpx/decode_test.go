package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type decodeTarget struct {
	Name    string  `json:"name"`
	LengthM *string `json:"length_m"`
}

func TestDecodeJSON_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"trunk","length_m":"5"}`))
	var dst decodeTarget
	if !DecodeJSON(rec, req, &dst) {
		t.Fatalf("want true, got false (body=%s)", rec.Body.String())
	}
	if dst.Name != "trunk" || dst.LengthM == nil || *dst.LengthM != "5" {
		t.Errorf("decoded wrong: %+v", dst)
	}
	// Nothing may be written on success — the handler owns the response.
	if rec.Body.Len() != 0 {
		t.Errorf("body written on success: %q", rec.Body.String())
	}
}

func TestDecodeJSON_WireTypeMismatch(t *testing.T) {
	rec := httptest.NewRecorder()
	// number where the struct wants a string — the bug class this
	// helper exists for.
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"length_m": 5}`))
	var dst decodeTarget
	if DecodeJSON(rec, req, &dst) {
		t.Fatal("want false on type mismatch")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid request body") {
		t.Errorf("body should lead with the honest prefix, got %q", body)
	}
	if !strings.Contains(body, "length_m") {
		t.Errorf("body should name the offending field, got %q", body)
	}
}

func TestDecodeJSON_MalformedJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{not json`))
	var dst decodeTarget
	if DecodeJSON(rec, req, &dst) {
		t.Fatal("want false on malformed JSON")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Errorf("got %q", rec.Body.String())
	}
}

func TestDecodeJSON_EmptyBody(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	var dst decodeTarget
	if DecodeJSON(rec, req, &dst) {
		t.Fatal("want false on empty body (EOF)")
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
