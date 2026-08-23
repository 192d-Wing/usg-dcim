// Handler tests for GET /dhcp/scopes/{id}/reconcile (PR 12). The
// classifier is covered exhaustively in internal/dhcp/reconcile;
// these focus on the HTTP-layer concerns:
//
//   - 404 missing scope / 404 fabric-not-found
//   - 403 fabric-out-of-scope
//   - Scope with no subnet → handler skips the ListIPAddressesIn
//     SubnetForReconcile call AND the body still has total=0 +
//     fixed-key zero-counts
//   - Happy path threads scope.reservations_json through to the
//     classifier; one collision-shaped fixture lands in Counts
//   - Wire shape: top-level keys (scope_id, subnet_id, total, counts,
//     entries) match Python's response
package ipam

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
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type reconcileFakeQ struct {
	fakeQ
	scopeFabricID  uuid.UUID
	scopeFabricErr error
	scope          dbq.GetDhcpScopeRow
	scopeErr       error

	ipRows     []dbq.ListIPAddressesInSubnetForReconcileRow
	ipRowsCall int
}

func (f *reconcileFakeQ) GetDhcpScopeFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.scopeFabricID, f.scopeFabricErr
}
func (f *reconcileFakeQ) GetDhcpScope(_ context.Context, _ uuid.UUID) (dbq.GetDhcpScopeRow, error) {
	return f.scope, f.scopeErr
}
func (f *reconcileFakeQ) ListIPAddressesInSubnetForReconcile(_ context.Context, _ uuid.UUID) ([]dbq.ListIPAddressesInSubnetForReconcileRow, error) {
	f.ipRowsCall++
	return f.ipRows, nil
}

func mountReconcile(f *reconcileFakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestReconcileDhcpScope_NotFound_404(t *testing.T) {
	f := &reconcileFakeQ{scopeFabricErr: pgx.ErrNoRows}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+uuid.New().String()+"/reconcile", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcile(f).ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestReconcileDhcpScope_ForbiddenWithoutScopedCap(t *testing.T) {
	scopeID := uuid.New()
	fabricID := uuid.New()
	f := &reconcileFakeQ{scopeFabricID: fabricID}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:reconcile"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:reconcile": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountReconcile(f).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestReconcileDhcpScope_NoSubnetSkipsIPLoad(t *testing.T) {
	scopeID := uuid.New()
	f := &reconcileFakeQ{
		scopeFabricID: uuid.New(),
		scope: dbq.GetDhcpScopeRow{
			ID: scopeID, IPFamily: 4, Prefix: "10.0.0.0/24",
			SubnetID: nil, // no linked subnet
			ReservationsJSON: json.RawMessage(`[]`),
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcile(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.ipRowsCall != 0 {
		t.Errorf("ListIPAddressesInSubnetForReconcile must not run when scope has no subnet, called %d times", f.ipRowsCall)
	}
	// Wire shape still carries every fixed-key bucket at 0 so
	// dashboards reading counts.collision don't see undefined.
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte(`"subnet_id":null`),
		[]byte(`"total":0`),
		[]byte(`"clean":0`),
		[]byte(`"collision":0`),
		[]byte(`"unbacked":0`),
	} {
		if !bytes.Contains(body, want) {
			t.Errorf("body missing %q, got %s", want, body)
		}
	}
}

func TestReconcileDhcpScope_CollisionThreadedThrough(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	ipID := uuid.New()
	f := &reconcileFakeQ{
		scopeFabricID: uuid.New(),
		scope: dbq.GetDhcpScopeRow{
			ID: scopeID, IPFamily: 4, Prefix: "10.0.0.0/24",
			SubnetID: &subnetID,
			ReservationsJSON: json.RawMessage(`[{"mac":"aa:bb:cc:dd:ee:01","ip":"10.0.0.5"}]`),
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
		// Same IP, source=static → reservation collides with an
		// operator-allocated address.
		ipRows: []dbq.ListIPAddressesInSubnetForReconcileRow{
			{ID: ipID, Address: "10.0.0.5", Source: "static"},
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcile(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if f.ipRowsCall != 1 {
		t.Errorf("ListIPAddressesInSubnetForReconcile calls = %d, want 1", f.ipRowsCall)
	}
	body := w.Body.Bytes()
	for _, want := range [][]byte{
		[]byte(`"collision":1`),
		[]byte(`"total":1`),
		[]byte(`"status":"collision"`),
		[]byte(`"ip_source":"static"`),
	} {
		if !bytes.Contains(body, want) {
			t.Errorf("body missing %q, got %s", want, body)
		}
	}
}

// ---- POST /reconcile/sync handler tests ----

// reconcileSyncFakeQ extends the read fake with capture of the two
// mutating queries the sync handler invokes via the reconcile
// orchestrator.
type reconcileSyncFakeQ struct {
	reconcileFakeQ
	inserts  []dbq.InsertReservationIPAddressParams
	promotes []dbq.PromoteDhcpLeaseToReservationParams
}

func (f *reconcileSyncFakeQ) InsertReservationIPAddress(_ context.Context, arg dbq.InsertReservationIPAddressParams) (uuid.UUID, error) {
	f.inserts = append(f.inserts, arg)
	return uuid.New(), nil
}
func (f *reconcileSyncFakeQ) PromoteDhcpLeaseToReservation(_ context.Context, arg dbq.PromoteDhcpLeaseToReservationParams) error {
	f.promotes = append(f.promotes, arg)
	return nil
}

func mountReconcileSync(f *reconcileSyncFakeQ, rec *recordingAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, Audit: rec}).Mount(r)
	return r
}

func TestReconcileSyncDhcpScope_HappyPath_InsertsAndAudits(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	f := &reconcileSyncFakeQ{
		reconcileFakeQ: reconcileFakeQ{
			scopeFabricID: uuid.New(),
			scope: dbq.GetDhcpScopeRow{
				ID: scopeID, IPFamily: 4, Prefix: "10.0.0.0/24",
				SubnetID: &subnetID,
				ReservationsJSON: json.RawMessage(`[{"mac":"aa:bb:cc:dd:ee:01","ip":"10.0.0.5"}]`),
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
			// No existing IP row → reservation lands in upserted.
		},
	}
	rec := &recordingAudit{}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile/sync", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcileSync(f, rec).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.inserts) != 1 {
		t.Errorf("inserts = %d, want 1", len(f.inserts))
	}
	if len(rec.calls) != 1 || rec.calls[0].Action != "dhcp_scope.reconcile_sync" {
		t.Errorf("audit calls = %+v", rec.calls)
	}
	// Audit metadata carries the per-decision counters Python uses
	// at api/ipam.py:2346-2355.
	var meta map[string]any
	if err := json.Unmarshal(rec.calls[0].MetadataJson, &meta); err != nil {
		t.Fatal(err)
	}
	if total, _ := meta["upserted"].(float64); int(total) != 1 {
		t.Errorf("metadata.upserted = %v, want 1", meta["upserted"])
	}
	for _, key := range []string{
		"promoted", "skipped_collision", "skipped_clean",
		"skipped_mac_mismatch", "skipped_duid_mismatch", "skipped_no_subnet",
	} {
		if _, ok := meta[key]; !ok {
			t.Errorf("metadata missing %q (Python parity for audit shape)", key)
		}
	}
}

func TestReconcileSyncDhcpScope_DhcpToReservationPromotion(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	ipID := uuid.New()
	mac := "aa:bb:cc:dd:ee:01"
	f := &reconcileSyncFakeQ{
		reconcileFakeQ: reconcileFakeQ{
			scopeFabricID: uuid.New(),
			scope: dbq.GetDhcpScopeRow{
				ID: scopeID, SubnetID: &subnetID,
				ReservationsJSON: json.RawMessage(`[{"mac":"aa:bb:cc:dd:ee:01","ip":"10.0.0.5"}]`),
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
			ipRows: []dbq.ListIPAddressesInSubnetForReconcileRow{
				{ID: ipID, Address: "10.0.0.5", Source: "dhcp", DhcpMac: &mac},
			},
		},
	}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile/sync", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcileSync(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if len(f.promotes) != 1 || f.promotes[0].ID != ipID {
		t.Errorf("promotes = %+v", f.promotes)
	}
	if len(f.inserts) != 0 {
		t.Errorf("promotion path must not insert; got %d inserts", len(f.inserts))
	}
}

func TestReconcileSyncDhcpScope_NoSubnetSkipsEverything(t *testing.T) {
	scopeID := uuid.New()
	f := &reconcileSyncFakeQ{
		reconcileFakeQ: reconcileFakeQ{
			scopeFabricID: uuid.New(),
			scope: dbq.GetDhcpScopeRow{
				ID: scopeID, SubnetID: nil,
				ReservationsJSON: json.RawMessage(`[{"mac":"aa:bb:cc:dd:ee:01","ip":"10.0.0.5"}]`),
				CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
		},
	}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile/sync", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcileSync(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if len(f.inserts) != 0 || len(f.promotes) != 0 {
		t.Errorf("no-subnet must not mutate; got inserts=%d promotes=%d", len(f.inserts), len(f.promotes))
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"skipped_no_subnet":1`)) {
		t.Errorf("body missing skipped_no_subnet:1, got %s", w.Body.String())
	}
}

func TestReconcileSyncDhcpScope_ForbiddenWithoutScopedCap(t *testing.T) {
	scopeID := uuid.New()
	f := &reconcileSyncFakeQ{reconcileFakeQ: reconcileFakeQ{scopeFabricID: uuid.New()}}
	req := httptest.NewRequest("POST", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile/sync", nil)
	p := auth.Principal{
		Capabilities: []string{"ipam:dhcp-scopes:reconcile-sync"},
		Scopes: map[string]auth.Scope{
			"ipam:dhcp-scopes:reconcile-sync": {FabricIDs: map[uuid.UUID]struct{}{uuid.New(): {}}},
		},
	}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	mountReconcileSync(f, &recordingAudit{}).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestReconcileDhcpScope_WireShapeMatchesPython(t *testing.T) {
	// Python's response (api/ipam.py:2299) keys: scope_id, subnet_id,
	// total, counts, entries. Regression guard against a struct-tag
	// or field-order change.
	scopeID := uuid.New()
	f := &reconcileFakeQ{
		scopeFabricID: uuid.New(),
		scope: dbq.GetDhcpScopeRow{
			ID: scopeID, ReservationsJSON: json.RawMessage(`[]`),
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}
	req := httptest.NewRequest("GET", "/ipam/dhcp/scopes/"+scopeID.String()+"/reconcile", nil)
	req = withPrincipal(req, "*")
	w := httptest.NewRecorder()
	mountReconcile(f).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	for _, want := range [][]byte{
		[]byte(`"scope_id":"` + scopeID.String() + `"`),
		[]byte(`"counts":{`),
		[]byte(`"entries":[]`),
	} {
		if !bytes.Contains(w.Body.Bytes(), want) {
			t.Errorf("body missing %q, got %s", want, w.Body.String())
		}
	}
}
