// PR 67 — handler tests for POST /ipam/subnets/bulk.
package ipam

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// fakeBulkQ stubs CreateSubnet + GetSupernet for the bulk handler.
// CreateSubnet can be made to return a unique-violation per call so
// the "skipped" path is testable without a live DB.
type fakeBulkQ struct {
	fakeQ
	supernet     dbq.GetSupernetRow
	supernetErr  error
	uniqueViolN  int // first N inserts will return 23505
	insertedRows []dbq.CreateSubnetParams
}

func (f *fakeBulkQ) GetSupernet(_ context.Context, _ uuid.UUID) (dbq.GetSupernetRow, error) {
	return f.supernet, f.supernetErr
}

func (f *fakeBulkQ) CreateSubnet(_ context.Context, a dbq.CreateSubnetParams) (dbq.CreateSubnetRow, error) {
	if f.uniqueViolN > 0 {
		f.uniqueViolN--
		return dbq.CreateSubnetRow{}, &pgconn.PgError{Code: "23505", Message: "duplicate key"}
	}
	f.insertedRows = append(f.insertedRows, a)
	return dbq.CreateSubnetRow{ID: uuid.New(), SupernetID: a.SupernetID, Prefix: a.Prefix}, nil
}

func mountBulk(f *fakeBulkQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// doBulk POSTs `rows` as JSON with a principal holding the bulk cap
// + fabric scope set to the test's parent fabric.
func doBulk(t *testing.T, f *fakeBulkQ, rows []map[string]any) *bulkResult {
	t.Helper()
	body, _ := json.Marshal(rows)
	req := httptest.NewRequest("POST", "/ipam/subnets/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Principal: bulk cap is global so any parent fabric is in-scope.
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

func TestBulkSubnets_AllInserted(t *testing.T) {
	supID := uuid.New()
	f := &fakeBulkQ{supernet: dbq.GetSupernetRow{ID: supID, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/16"}}
	out := doBulk(t, f, []map[string]any{
		{"supernet_id": supID, "prefix": "10.0.0.0/24"},
		{"supernet_id": supID, "prefix": "10.0.1.0/24"},
	})
	if out.Inserted != 2 || out.Skipped != 0 || out.Failed != 0 {
		t.Errorf("counts = %+v, want inserted=2", out)
	}
	if len(f.insertedRows) != 2 {
		t.Errorf("inserted %d rows", len(f.insertedRows))
	}
}

func TestBulkSubnets_UniqueViolationSkips(t *testing.T) {
	// First insert returns 23505 → skipped. Second succeeds.
	supID := uuid.New()
	f := &fakeBulkQ{
		supernet:    dbq.GetSupernetRow{ID: supID, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/16"},
		uniqueViolN: 1,
	}
	out := doBulk(t, f, []map[string]any{
		{"supernet_id": supID, "prefix": "10.0.0.0/24"},
		{"supernet_id": supID, "prefix": "10.0.1.0/24"},
	})
	if out.Inserted != 1 || out.Skipped != 1 || out.Failed != 0 {
		t.Errorf("counts = %+v, want inserted=1 skipped=1", out)
	}
	if len(out.Errors) != 0 {
		t.Errorf("errors should be empty on unique-violation skip: %v", out.Errors)
	}
}

func TestBulkSubnets_BadCIDRFails(t *testing.T) {
	supID := uuid.New()
	f := &fakeBulkQ{supernet: dbq.GetSupernetRow{ID: supID, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/16"}}
	out := doBulk(t, f, []map[string]any{
		{"supernet_id": supID, "prefix": "garbage"},
		{"supernet_id": supID, "prefix": "10.0.1.0/24"},
	})
	if out.Inserted != 1 || out.Failed != 1 {
		t.Errorf("counts = %+v", out)
	}
	if len(out.Errors) != 1 || out.Errors[0]["row"].(float64) != 0 {
		t.Errorf("expected one error keyed by row 0: %v", out.Errors)
	}
}

func TestBulkSubnets_RowOutsideSupernetFails(t *testing.T) {
	// supernet 10.0.0.0/16 but the row asks for 11.0.0.0/24.
	supID := uuid.New()
	f := &fakeBulkQ{supernet: dbq.GetSupernetRow{ID: supID, FabricID: uuid.New(), VrfID: uuid.New(), Prefix: "10.0.0.0/16"}}
	out := doBulk(t, f, []map[string]any{
		{"supernet_id": supID, "prefix": "11.0.0.0/24"},
	})
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want failed=1", out)
	}
}

func TestBulkSubnets_MissingPrefixFails(t *testing.T) {
	supID := uuid.New()
	f := &fakeBulkQ{}
	out := doBulk(t, f, []map[string]any{{"supernet_id": supID}})
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want failed=1", out)
	}
}

func TestBulkSubnets_MalformedPayloadIs400(t *testing.T) {
	// A JSON object instead of an array → 400, not 200 with errors.
	req := httptest.NewRequest("POST", "/ipam/subnets/bulk", bytes.NewReader([]byte(`{"not":"array"}`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(&fakeBulkQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for non-array payload", rec.Code)
	}
}

func TestBulkSubnets_RequiresBulkCapability(t *testing.T) {
	// A principal without the bulk cap should get 403, not enter
	// the handler.
	req := httptest.NewRequest("POST", "/ipam/subnets/bulk", bytes.NewReader([]byte(`[]`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"ipam:subnets:create"}} // wrong cap
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(&fakeBulkQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without ipam:bulk:execute", rec.Code)
	}
}

func TestBulkSubnets_FabricScopeDeniedFailsRow(t *testing.T) {
	// Principal holds the bulk cap but is fabric-scoped to a
	// different fabric than the parent supernet's. The row should
	// fail (not the whole batch); other rows pass.
	supFabric := uuid.New()
	otherFabric := uuid.New()
	supID := uuid.New()
	f := &fakeBulkQ{supernet: dbq.GetSupernetRow{ID: supID, FabricID: supFabric, VrfID: uuid.New(), Prefix: "10.0.0.0/16"}}

	body, _ := json.Marshal([]map[string]any{{"supernet_id": supID, "prefix": "10.0.0.0/24"}})
	req := httptest.NewRequest("POST", "/ipam/subnets/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{
		Capabilities: []string{"ipam:bulk:execute"},
		Scopes: map[string]auth.Scope{
			"ipam:bulk:execute": {FabricIDs: map[uuid.UUID]struct{}{otherFabric: {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulk(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out bulkResult
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want failed=1 (fabric-scope denied)", out)
	}
}

func TestBulkSubnets_EmptyPayloadIsZeroCounts(t *testing.T) {
	out := doBulk(t, &fakeBulkQ{}, []map[string]any{})
	if out.Inserted != 0 || out.Skipped != 0 || out.Failed != 0 {
		t.Errorf("empty payload should be all zeros: %+v", out)
	}
	if out.Errors == nil {
		t.Errorf("Errors should be [] not null")
	}
}
