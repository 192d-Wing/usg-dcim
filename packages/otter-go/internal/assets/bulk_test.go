// PR 69 — handler tests for POST /assets/bulk.
package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// fakeBulkAssetQ embeds fakeQ and lets the test inject a hit on
// FindAssetByManufacturerSerial so the update-branch is exercised
// without a live DB.
type fakeBulkAssetQ struct {
	fakeQ
	hits map[string]dbq.Asset // key = manufacturer|serial
}

func (f *fakeBulkAssetQ) FindAssetByManufacturerSerial(_ context.Context, mfr, ser string) (dbq.Asset, error) {
	if a, ok := f.hits[mfr+"|"+ser]; ok {
		return a, nil
	}
	return dbq.Asset{}, pgx.ErrNoRows
}

func mountBulk(f *fakeBulkAssetQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// doBulk POSTs `rows` with a principal holding the bulk cap +
// global scope so per-row EnforceSiteScope always passes.
func doBulk(t *testing.T, f *fakeBulkAssetQ, rows []map[string]any) *bulkResult {
	t.Helper()
	body, _ := json.Marshal(rows)
	req := httptest.NewRequest("POST", "/assets/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out bulkResult
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestBulkAssets_InsertAll(t *testing.T) {
	site := uuid.New()
	out := doBulk(t, &fakeBulkAssetQ{}, []map[string]any{
		{"site_id": site, "name": "r1", "kind": "server"},
		{"site_id": site, "name": "r2", "kind": "server"},
	})
	if out.Inserted != 2 || out.Updated != 0 || out.Failed != 0 {
		t.Errorf("counts = %+v, want inserted=2", out)
	}
}

func TestBulkAssets_UpdateExistingByMfrSerial(t *testing.T) {
	// Row carries manufacturer+serial that matches an existing
	// row → update path. No new asset created.
	site := uuid.New()
	existingID := uuid.New()
	f := &fakeBulkAssetQ{
		hits: map[string]dbq.Asset{
			"Dell|ABC123": {ID: existingID, SiteID: site, Name: "old-name"},
		},
	}
	out := doBulk(t, f, []map[string]any{
		{"site_id": site, "name": "new-name", "kind": "server", "manufacturer": "Dell", "serial": "ABC123"},
	})
	if out.Updated != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want updated=1", out)
	}
}

func TestBulkAssets_MissingMfrSerialAlwaysInserts(t *testing.T) {
	// Even if an asset with the same name exists, lack of
	// (manufacturer, serial) means no upsert key → always insert.
	site := uuid.New()
	out := doBulk(t, &fakeBulkAssetQ{}, []map[string]any{
		{"site_id": site, "name": "r1", "kind": "server"},               // both missing
		{"site_id": site, "name": "r2", "kind": "server", "serial": "X"}, // serial only
	})
	if out.Inserted != 2 || out.Updated != 0 {
		t.Errorf("counts = %+v, want inserted=2 (no upsert key)", out)
	}
}

func TestBulkAssets_MixedInsertAndUpdate(t *testing.T) {
	site := uuid.New()
	f := &fakeBulkAssetQ{
		hits: map[string]dbq.Asset{
			"Dell|S1": {ID: uuid.New(), SiteID: site, Name: "old"},
		},
	}
	out := doBulk(t, f, []map[string]any{
		{"site_id": site, "name": "match", "kind": "server", "manufacturer": "Dell", "serial": "S1"},
		{"site_id": site, "name": "fresh", "kind": "server", "manufacturer": "Dell", "serial": "S2"},
		{"site_id": site, "name": "no-key", "kind": "server"},
	})
	if out.Updated != 1 || out.Inserted != 2 || out.Failed != 0 {
		t.Errorf("counts = %+v, want updated=1 inserted=2", out)
	}
}

func TestBulkAssets_MissingRequiredFailsRow(t *testing.T) {
	out := doBulk(t, &fakeBulkAssetQ{}, []map[string]any{
		{"name": "r1", "kind": "server"}, // no site_id
	})
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v", out)
	}
	if len(out.Errors) != 1 || out.Errors[0]["row"].(float64) != 0 {
		t.Errorf("expected one error keyed by row 0: %v", out.Errors)
	}
}

func TestBulkAssets_MalformedPayloadIs400(t *testing.T) {
	req := httptest.NewRequest("POST", "/assets/bulk", bytes.NewReader([]byte(`"oops"`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(&fakeBulkAssetQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBulkAssets_RequiresBulkCapability(t *testing.T) {
	req := httptest.NewRequest("POST", "/assets/bulk", bytes.NewReader([]byte(`[]`)))
	req.Header.Set("Content-Type", "application/json")
	// Wrong cap (asset:create instead of inventory:bulk:execute).
	p := auth.Principal{Capabilities: []string{"inventory:assets:create"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(&fakeBulkAssetQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestBulkAssets_SiteScopeDeniedFailsRow(t *testing.T) {
	// Principal holds bulk cap but is site-scoped elsewhere — the
	// row's site_id is outside scope → row fails (whole batch
	// keeps going).
	rowSite, other := uuid.New(), uuid.New()
	body, _ := json.Marshal([]map[string]any{
		{"site_id": rowSite, "name": "r1", "kind": "server"},
	})
	req := httptest.NewRequest("POST", "/assets/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{
		Capabilities: []string{"inventory:bulk:execute"},
		Scopes: map[string]auth.Scope{
			"inventory:bulk:execute": {SiteIDs: map[uuid.UUID]struct{}{other: {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(&fakeBulkAssetQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out bulkResult
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want failed=1", out)
	}
}

func TestBulkAssets_EmptyPayloadIsZeroCounts(t *testing.T) {
	out := doBulk(t, &fakeBulkAssetQ{}, []map[string]any{})
	if out.Inserted+out.Updated+out.Skipped+out.Failed != 0 {
		t.Errorf("empty payload should be all zeros: %+v", out)
	}
	if out.Errors == nil {
		t.Errorf("Errors should be [] not null")
	}
}
