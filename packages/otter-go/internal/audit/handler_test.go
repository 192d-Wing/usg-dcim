package audit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	last    dbq.ListAuditLogParams
	actions []string
}

func (f *fakeQ) ListAuditLog(_ context.Context, a dbq.ListAuditLogParams) ([]dbq.AuditLog, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountAuditLog(_ context.Context, _ dbq.CountAuditLogParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) ListAuditActions(_ context.Context) ([]string, error) { return f.actions, nil }

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	return rec
}

func TestListLog_AllFilters(t *testing.T) {
	uid, sid := uuid.New(), uuid.New()
	f := &fakeQ{}
	url := "/audit/log?actor_user_id=" + uid.String() +
		"&action=asset.update&target_type=asset&target_id=abc&site_id=" + sid.String() +
		"&since=2025-01-01T00:00:00Z&until=2025-12-31T23:59:59Z&success=false"
	rec := do(t, mount(f), url)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.ActorUserID == nil || *f.last.ActorUserID != uid {
		t.Error("actor_user_id")
	}
	if f.last.Action == nil || *f.last.Action != "asset.update" {
		t.Error("action")
	}
	if f.last.SiteID == nil || *f.last.SiteID != sid {
		t.Error("site_id")
	}
	if f.last.Since == nil {
		t.Error("since not parsed")
	}
	if f.last.Until == nil {
		t.Error("until not parsed")
	}
	if f.last.Success == nil || *f.last.Success {
		t.Error("success=false not threaded")
	}
}

func TestListLog_BadSince(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/audit/log?since=not-a-time")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestListActions_EmptyReturnsArray(t *testing.T) {
	rec := do(t, mount(&fakeQ{actions: nil}), "/audit/actions")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	var got []string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Error("should be empty array, not null")
	}
}
