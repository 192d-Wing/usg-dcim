package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// mwWindowFake extends fakeQ to control GetMaintenanceWindow returns
// so the merged-window update path can be exercised without DB access.
type mwWindowFake struct {
	fakeQ
	current dbq.MaintenanceWindow
}

func (f *mwWindowFake) GetMaintenanceWindow(_ context.Context, id uuid.UUID) (dbq.MaintenanceWindow, error) {
	if id == f.current.ID {
		return f.current, nil
	}
	return f.fakeQ.GetMaintenanceWindow(context.Background(), id)
}
func (f *mwWindowFake) GetMaintenanceWindowSiteID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return f.current.SiteID, nil
}

func mountMW(f *mwWindowFake) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func wildcardPOST(method, path string, body []byte) *http.Request {
	return authtest.Request(method, path, authtest.PrincipalWithCaps("*"), bytes.NewReader(body))
}

// Mirrors Python's _validate_window: a POST that puts ends_at at or
// before starts_at must 400, not silently persist an inverted window
// (which would never match the suppression query in services/
// alerts.py).
func TestCreateMW_InvertedWindow_400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"name":      "midnight",
		"starts_at": "2027-01-02T00:00:00Z",
		"ends_at":   "2027-01-01T00:00:00Z",
	})
	rec := httptest.NewRecorder()
	mountMW(&mwWindowFake{}).ServeHTTP(rec,
		wildcardPOST(http.MethodPost, "/alerts/maintenance-windows", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for inverted window; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Equal bounds (ends_at == starts_at) must also reject — Python used
// `<=` in the validator.
func TestCreateMW_EqualWindow_400(t *testing.T) {
	at := "2027-01-01T00:00:00Z"
	body, _ := json.Marshal(map[string]any{
		"name":      "instant",
		"starts_at": at,
		"ends_at":   at,
	})
	rec := httptest.NewRecorder()
	mountMW(&mwWindowFake{}).ServeHTTP(rec,
		wildcardPOST(http.MethodPost, "/alerts/maintenance-windows", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero-length window; got %d", rec.Code)
	}
}

func TestCreateMW_ValidWindow_OK(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"name":      "ok",
		"starts_at": "2027-01-01T00:00:00Z",
		"ends_at":   "2027-01-02T00:00:00Z",
	})
	rec := httptest.NewRecorder()
	mountMW(&mwWindowFake{}).ServeHTTP(rec,
		wildcardPOST(http.MethodPost, "/alerts/maintenance-windows", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for valid window; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// PATCH that moves ends_at before the current starts_at must reject
// (post-merge window is inverted). Mirrors Python's
// _validate_window(diff.get("starts_at", obj.starts_at),
//                   diff.get("ends_at", obj.ends_at))
func TestUpdateMW_PatchedEndBeforeCurrentStart_400(t *testing.T) {
	id := uuid.New()
	current := dbq.MaintenanceWindow{
		ID:       id,
		Name:     "existing",
		StartsAt: time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC),
	}
	f := &mwWindowFake{current: current}
	// Move ends_at to 2027-01-04 (before current starts_at 01-05) →
	// post-merge window is (start=Jan 5, end=Jan 4) → inverted.
	body, _ := json.Marshal(map[string]any{"ends_at": "2027-01-04T00:00:00Z"})
	rec := httptest.NewRecorder()
	mountMW(f).ServeHTTP(rec,
		wildcardPOST(http.MethodPatch, "/alerts/maintenance-windows/"+id.String(), body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// PATCH that only changes a non-time field skips the window validation
// (no GetMaintenanceWindow call needed, no inverted-window risk).
func TestUpdateMW_NameOnlyPatch_NoValidationCall(t *testing.T) {
	id := uuid.New()
	current := dbq.MaintenanceWindow{
		ID:       id,
		Name:     "existing",
		StartsAt: time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC),
		EndsAt:   time.Date(2027, 1, 10, 0, 0, 0, 0, time.UTC),
	}
	f := &mwWindowFake{current: current}
	body, _ := json.Marshal(map[string]any{"name": "renamed"})
	rec := httptest.NewRecorder()
	mountMW(f).ServeHTTP(rec,
		wildcardPOST(http.MethodPatch, "/alerts/maintenance-windows/"+id.String(), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200; got %d body=%s", rec.Code, rec.Body.String())
	}
}
