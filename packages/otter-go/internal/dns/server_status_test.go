// PR 73 — handler tests for POST /dns/servers/{id}/render-status.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type fakeRenderQ struct {
	fakeQ
	got  dbq.SetDnsServerRenderStatusParams
	rows int64
}

func (f *fakeRenderQ) SetDnsServerRenderStatus(_ context.Context, arg dbq.SetDnsServerRenderStatusParams) (int64, error) {
	f.got = arg
	return f.rows, nil
}

func mountRender(f *fakeRenderQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func postRender(t *testing.T, h http.Handler, id string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/dns/servers/"+id+"/render-status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRenderStatus_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeRenderQ{rows: 1}
	etag := "abcd1234"
	ver := "1.11.3"
	rec := postRender(t, mountRender(f), id.String(), map[string]any{
		"status": "ok", "etag": etag, "coredns_version": ver,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if f.got.ID != id {
		t.Errorf("forwarded id = %s, want %s", f.got.ID, id)
	}
	if f.got.Status != "ok" {
		t.Errorf("forwarded status = %q", f.got.Status)
	}
	if f.got.Etag == nil || *f.got.Etag != etag {
		t.Errorf("forwarded etag = %v", f.got.Etag)
	}
	if f.got.CoreDNSVersion == nil || *f.got.CoreDNSVersion != ver {
		t.Errorf("forwarded coredns_version = %v", f.got.CoreDNSVersion)
	}
	var resp renderStatusResp
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.ServerID != id.String() || resp.Status != "ok" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestRenderStatus_ErrorRecorded(t *testing.T) {
	id := uuid.New()
	f := &fakeRenderQ{rows: 1}
	msg := "config-write returned 5"
	postRender(t, mountRender(f), id.String(), map[string]any{
		"status": "error", "error": msg,
	})
	if f.got.Status != "error" {
		t.Errorf("forwarded status = %q", f.got.Status)
	}
	if f.got.Error == nil || *f.got.Error != msg {
		t.Errorf("forwarded error = %v", f.got.Error)
	}
}

func TestRenderStatus_OptionalCoreDNSVersionLeftNullWhenMissing(t *testing.T) {
	// COALESCE in SQL means NULL preserves the prior value — we
	// pass through nil rather than empty-string.
	id := uuid.New()
	f := &fakeRenderQ{rows: 1}
	postRender(t, mountRender(f), id.String(), map[string]any{"status": "ok"})
	if f.got.CoreDNSVersion != nil {
		t.Errorf("CoreDNSVersion = %v, want nil for unset field", f.got.CoreDNSVersion)
	}
}

func TestRenderStatus_RejectsUnknownStatus(t *testing.T) {
	id := uuid.New()
	rec := postRender(t, mountRender(&fakeRenderQ{rows: 1}), id.String(), map[string]any{"status": "weird"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRenderStatus_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	rec := postRender(t, mountRender(&fakeRenderQ{rows: 0}), id.String(), map[string]any{"status": "ok"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRenderStatus_BadUUIDIs400(t *testing.T) {
	rec := postRender(t, mountRender(&fakeRenderQ{}), "not-a-uuid", map[string]any{"status": "ok"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRenderStatus_RequiresUpdateCapability(t *testing.T) {
	id := uuid.New()
	req := httptest.NewRequest("POST", "/dns/servers/"+id.String()+"/render-status",
		bytes.NewReader([]byte(`{"status":"ok"}`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"dns:servers:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountRender(&fakeRenderQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
