// PR 72 — handler tests for POST /dns/health-checks/{id}/result.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// fakeHCResultQ captures the last SetDnsHealthCheckResult call so
// tests can verify the handler forwards the right values. rows
// controls the "row exists / doesn't" branch.
type fakeHCResultQ struct {
	fakeQ
	gotID     uuid.UUID
	gotStatus string
	gotErr    *string
	rows      int64
}

func (f *fakeHCResultQ) SetDnsHealthCheckResult(_ context.Context, id uuid.UUID, status string, e *string) (int64, error) {
	f.gotID = id
	f.gotStatus = status
	f.gotErr = e
	return f.rows, nil
}

func mountHC(f *fakeHCResultQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func postResult(t *testing.T, h http.Handler, id, status string, errText *string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]any{"status": status}
	if errText != nil {
		body["error"] = *errText
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/dns/health-checks/"+id+"/result", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthCheckResult_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeHCResultQ{rows: 1}
	rec := postResult(t, mountHC(f), id.String(), "healthy", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if f.gotID != id {
		t.Errorf("forwarded id = %s, want %s", f.gotID, id)
	}
	if f.gotStatus != "healthy" {
		t.Errorf("forwarded status = %q", f.gotStatus)
	}
}

func TestHealthCheckResult_ForwardsErrorText(t *testing.T) {
	id := uuid.New()
	f := &fakeHCResultQ{rows: 1}
	msg := "connection refused"
	postResult(t, mountHC(f), id.String(), "unhealthy", &msg)
	if f.gotErr == nil || *f.gotErr != msg {
		t.Errorf("forwarded error = %v, want %q", f.gotErr, msg)
	}
}

func TestHealthCheckResult_TruncatesLongError(t *testing.T) {
	id := uuid.New()
	f := &fakeHCResultQ{rows: 1}
	long := strings.Repeat("x", 1024)
	postResult(t, mountHC(f), id.String(), "unhealthy", &long)
	if f.gotErr == nil || len(*f.gotErr) != 512 {
		t.Errorf("error len = %d, want 512", len(*f.gotErr))
	}
}

func TestHealthCheckResult_RejectsUnknownStatus(t *testing.T) {
	id := uuid.New()
	rec := postResult(t, mountHC(&fakeHCResultQ{rows: 1}), id.String(), "weird", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHealthCheckResult_NotFoundIs404(t *testing.T) {
	// SetDnsHealthCheckResult returns 0 rows-affected → 404.
	id := uuid.New()
	rec := postResult(t, mountHC(&fakeHCResultQ{rows: 0}), id.String(), "healthy", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHealthCheckResult_BadUUIDIs400(t *testing.T) {
	rec := postResult(t, mountHC(&fakeHCResultQ{rows: 1}), "not-a-uuid", "healthy", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHealthCheckResult_MalformedJSONIs400(t *testing.T) {
	id := uuid.New()
	req := httptest.NewRequest("POST", "/dns/health-checks/"+id.String()+"/result",
		bytes.NewReader([]byte(`not-json`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountHC(&fakeHCResultQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHealthCheckResult_RequiresUpdateCapability(t *testing.T) {
	id := uuid.New()
	req := httptest.NewRequest("POST", "/dns/health-checks/"+id.String()+"/result",
		bytes.NewReader([]byte(`{"status":"healthy"}`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"dns:health-checks:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountHC(&fakeHCResultQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
