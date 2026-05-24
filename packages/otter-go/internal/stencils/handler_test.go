package stencils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestList_ReturnsPaletteAndStencils(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{}).Mount(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/stencils", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	var body response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := body.Palette["dell"]; !ok {
		t.Error("palette missing 'dell'")
	}
	if len(body.Stencils) == 0 {
		t.Error("stencils empty")
	}
	// Vertical-PDU entries should serialize the bool field.
	var sawVertical bool
	for _, s := range body.Stencils {
		if s.Vertical != nil && *s.Vertical {
			sawVertical = true
			break
		}
	}
	if !sawVertical {
		t.Error("expected at least one vertical PDU entry")
	}
}
