// PR 68 — handler tests for POST /ipam/addresses/bulk.
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

type fakeBulkAddrQ struct {
	fakeQ
	subnet       dbq.GetSubnetRow
	subnetErr    error
	uniqueViolN  int
	insertedRows []dbq.CreateIPAddressParams
}

func (f *fakeBulkAddrQ) GetSubnet(_ context.Context, _ uuid.UUID) (dbq.GetSubnetRow, error) {
	return f.subnet, f.subnetErr
}

func (f *fakeBulkAddrQ) CreateIPAddress(_ context.Context, a dbq.CreateIPAddressParams) (dbq.CreateIPAddressRow, error) {
	if f.uniqueViolN > 0 {
		f.uniqueViolN--
		return dbq.CreateIPAddressRow{}, &pgconn.PgError{Code: "23505", Message: "duplicate"}
	}
	f.insertedRows = append(f.insertedRows, a)
	return dbq.CreateIPAddressRow{ID: uuid.New(), SubnetID: a.SubnetID, Address: a.Address}, nil
}

func mountBulkAddr(f *fakeBulkAddrQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func doBulkAddr(t *testing.T, f *fakeBulkAddrQ, rows []map[string]any) *bulkResult {
	t.Helper()
	body, _ := json.Marshal(rows)
	req := httptest.NewRequest("POST", "/ipam/addresses/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulkAddr(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out bulkResult
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestBulkAddresses_AllInserted(t *testing.T) {
	sub := uuid.New()
	f := &fakeBulkAddrQ{subnet: dbq.GetSubnetRow{ID: sub, FabricID: uuid.New(), Prefix: "10.0.0.0/24"}}
	out := doBulkAddr(t, f, []map[string]any{
		{"subnet_id": sub, "address": "10.0.0.10"},
		{"subnet_id": sub, "address": "10.0.0.11"},
	})
	if out.Inserted != 2 || out.Skipped != 0 || out.Failed != 0 {
		t.Errorf("counts = %+v, want inserted=2", out)
	}
}

func TestBulkAddresses_DefaultsApplied(t *testing.T) {
	// Rows without role/status/source should land with the same
	// defaults the single-row create uses.
	sub := uuid.New()
	f := &fakeBulkAddrQ{subnet: dbq.GetSubnetRow{ID: sub, FabricID: uuid.New(), Prefix: "10.0.0.0/24"}}
	out := doBulkAddr(t, f, []map[string]any{{"subnet_id": sub, "address": "10.0.0.10"}})
	if out.Inserted != 1 {
		t.Fatalf("counts = %+v", out)
	}
	got := f.insertedRows[0]
	if got.Role != "data" || got.Status != "active" || got.Source != "static" {
		t.Errorf("defaults = role=%s status=%s source=%s, want data/active/static",
			got.Role, got.Status, got.Source)
	}
}

func TestBulkAddresses_UniqueViolationSkips(t *testing.T) {
	sub := uuid.New()
	f := &fakeBulkAddrQ{
		subnet:      dbq.GetSubnetRow{ID: sub, FabricID: uuid.New(), Prefix: "10.0.0.0/24"},
		uniqueViolN: 1,
	}
	out := doBulkAddr(t, f, []map[string]any{
		{"subnet_id": sub, "address": "10.0.0.10"},
		{"subnet_id": sub, "address": "10.0.0.11"},
	})
	if out.Inserted != 1 || out.Skipped != 1 || out.Failed != 0 {
		t.Errorf("counts = %+v, want inserted=1 skipped=1", out)
	}
	if len(out.Errors) != 0 {
		t.Errorf("errors should be empty on unique-violation: %v", out.Errors)
	}
}

func TestBulkAddresses_AddressOutsideSubnetFails(t *testing.T) {
	sub := uuid.New()
	f := &fakeBulkAddrQ{subnet: dbq.GetSubnetRow{ID: sub, FabricID: uuid.New(), Prefix: "10.0.0.0/24"}}
	out := doBulkAddr(t, f, []map[string]any{{"subnet_id": sub, "address": "11.0.0.5"}})
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want failed=1", out)
	}
}

func TestBulkAddresses_BadAddressFails(t *testing.T) {
	sub := uuid.New()
	f := &fakeBulkAddrQ{subnet: dbq.GetSubnetRow{ID: sub, FabricID: uuid.New(), Prefix: "10.0.0.0/24"}}
	out := doBulkAddr(t, f, []map[string]any{{"subnet_id": sub, "address": "not-an-ip"}})
	if out.Failed != 1 {
		t.Errorf("counts = %+v", out)
	}
}

func TestBulkAddresses_MissingRequiredFails(t *testing.T) {
	out := doBulkAddr(t, &fakeBulkAddrQ{}, []map[string]any{{"address": "10.0.0.5"}})
	if out.Failed != 1 {
		t.Errorf("counts = %+v", out)
	}
}

func TestBulkAddresses_MalformedPayloadIs400(t *testing.T) {
	req := httptest.NewRequest("POST", "/ipam/addresses/bulk", bytes.NewReader([]byte(`"oops"`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulkAddr(&fakeBulkAddrQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestBulkAddresses_RequiresBulkCapability(t *testing.T) {
	req := httptest.NewRequest("POST", "/ipam/addresses/bulk", bytes.NewReader([]byte(`[]`)))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"ipam:addresses:create"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulkAddr(&fakeBulkAddrQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestBulkAddresses_FabricScopeDeniedFailsRow(t *testing.T) {
	subFabric, other := uuid.New(), uuid.New()
	sub := uuid.New()
	f := &fakeBulkAddrQ{subnet: dbq.GetSubnetRow{ID: sub, FabricID: subFabric, Prefix: "10.0.0.0/24"}}
	body, _ := json.Marshal([]map[string]any{{"subnet_id": sub, "address": "10.0.0.10"}})
	req := httptest.NewRequest("POST", "/ipam/addresses/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{
		Capabilities: []string{"ipam:bulk:execute"},
		Scopes: map[string]auth.Scope{
			"ipam:bulk:execute": {FabricIDs: map[uuid.UUID]struct{}{other: {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountBulkAddr(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out bulkResult
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Failed != 1 || out.Inserted != 0 {
		t.Errorf("counts = %+v, want failed=1", out)
	}
}

func TestBulkAddresses_EmptyPayloadIsZeroCounts(t *testing.T) {
	out := doBulkAddr(t, &fakeBulkAddrQ{}, []map[string]any{})
	if out.Inserted+out.Skipped+out.Failed != 0 {
		t.Errorf("empty payload should be all zeros: %+v", out)
	}
}
