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
	scope          dbq.DhcpScope
	scopeErr       error

	ipRows     []dbq.DhcpReconcileIPRow
	ipRowsCall int
}

func (f *reconcileFakeQ) GetDhcpScopeFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.scopeFabricID, f.scopeFabricErr
}
func (f *reconcileFakeQ) GetDhcpScope(_ context.Context, _ uuid.UUID) (dbq.DhcpScope, error) {
	return f.scope, f.scopeErr
}
func (f *reconcileFakeQ) ListIPAddressesInSubnetForReconcile(_ context.Context, _ uuid.UUID) ([]dbq.DhcpReconcileIPRow, error) {
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
		scope: dbq.DhcpScope{
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
		scope: dbq.DhcpScope{
			ID: scopeID, IPFamily: 4, Prefix: "10.0.0.0/24",
			SubnetID: &subnetID,
			ReservationsJSON: json.RawMessage(`[{"mac":"aa:bb:cc:dd:ee:01","ip":"10.0.0.5"}]`),
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
		// Same IP, source=static → reservation collides with an
		// operator-allocated address.
		ipRows: []dbq.DhcpReconcileIPRow{
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

func TestReconcileDhcpScope_WireShapeMatchesPython(t *testing.T) {
	// Python's response (api/ipam.py:2299) keys: scope_id, subnet_id,
	// total, counts, entries. Regression guard against a struct-tag
	// or field-order change.
	scopeID := uuid.New()
	f := &reconcileFakeQ{
		scopeFabricID: uuid.New(),
		scope: dbq.DhcpScope{
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
