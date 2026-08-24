// Tests for POST /api/v1/ipam/supernets/{id}/move — the LIR landing
// → operational fabric relocation endpoint. Each scenario stubs the
// IPAM Querier methods used by move.go and asserts the right guard
// fires.
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
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// moveFakeQ embeds the existing fakeQ and overrides the three move-
// specific methods. Each field controls one method's behavior; tests
// configure them per scenario.
type moveFakeQ struct {
	fakeQ
	supernet       *dbq.GetSupernetForMoveRow
	getSupernetErr error
	vrf            *dbq.GetVrfForMoveRow
	getVrfErr      error
	moveResult     dbq.MoveSupernetRow
	moveErr        error
	movedCalls     []dbq.MoveSupernetParams
}

func (m *moveFakeQ) GetSupernetForMove(_ context.Context, _ uuid.UUID) (dbq.GetSupernetForMoveRow, error) {
	if m.getSupernetErr != nil {
		return dbq.GetSupernetForMoveRow{}, m.getSupernetErr
	}
	if m.supernet == nil {
		return dbq.GetSupernetForMoveRow{}, pgx.ErrNoRows
	}
	return *m.supernet, nil
}

func (m *moveFakeQ) GetVrfForMove(_ context.Context, _ uuid.UUID) (dbq.GetVrfForMoveRow, error) {
	if m.getVrfErr != nil {
		return dbq.GetVrfForMoveRow{}, m.getVrfErr
	}
	if m.vrf == nil {
		return dbq.GetVrfForMoveRow{}, pgx.ErrNoRows
	}
	return *m.vrf, nil
}

func (m *moveFakeQ) MoveSupernet(_ context.Context, a dbq.MoveSupernetParams) (dbq.MoveSupernetRow, error) {
	m.movedCalls = append(m.movedCalls, a)
	if m.moveErr != nil {
		return dbq.MoveSupernetRow{}, m.moveErr
	}
	return m.moveResult, nil
}

// ---- harness ----

func mountMove(q Querier) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)
	return r
}

func sendMove(t *testing.T, h http.Handler, id uuid.UUID, body any, p auth.Principal) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest("POST", "/ipam/supernets/"+id.String()+"/move", &buf)
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func globalPrincipal() auth.Principal {
	return auth.Principal{Capabilities: []string{"*"}}
}

func orgScopedPrincipal(orgIDs ...uuid.UUID) auth.Principal {
	set := make(map[uuid.UUID]struct{}, len(orgIDs))
	for _, id := range orgIDs {
		set[id] = struct{}{}
	}
	return auth.Principal{
		Capabilities: []string{"*"},
		Scopes: map[string]auth.Scope{
			"*": {OrganizationIDs: set},
		},
	}
}

func fabricScopedPrincipal(fabricIDs ...uuid.UUID) auth.Principal {
	set := make(map[uuid.UUID]struct{}, len(fabricIDs))
	for _, id := range fabricIDs {
		set[id] = struct{}{}
	}
	return auth.Principal{
		Capabilities: []string{"*"},
		Scopes: map[string]auth.Scope{
			"*": {FabricIDs: set},
		},
	}
}

// happySupernet returns a SupernetForMoveRow that satisfies every
// source-side guard: in landing fabric, tenant-owned, real prefix.
func happySupernet(orgID uuid.UUID) *dbq.GetSupernetForMoveRow {
	owner := orgID
	return &dbq.GetSupernetForMoveRow{
		ID:                  uuid.New(),
		CurrentFabricID:     uuid.New(),
		CurrentVrfID:        uuid.New(),
		OwnerOrganizationID: &owner,
		Prefix:              "10.0.0.0/24",
		CurrentFabricIsSystem: true,
	}
}

// ---- guards ----

func TestMove_OK(t *testing.T) {
	orgID := uuid.New()
	targetFabric := uuid.New()
	targetVrf := uuid.New()
	src := happySupernet(orgID)
	q := &moveFakeQ{
		supernet:   src,
		vrf:        &dbq.GetVrfForMoveRow{ID: targetVrf, FabricID: targetFabric},
		moveResult: dbq.MoveSupernetRow{ID: src.ID, FabricID: targetFabric, VrfID: targetVrf},
	}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": targetFabric.String(), "vrf_id": targetVrf.String(),
	}, globalPrincipal())
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(q.movedCalls) != 1 {
		t.Fatalf("expected 1 MoveSupernet call, got %d", len(q.movedCalls))
	}
	c := q.movedCalls[0]
	if c.ID != src.ID || c.TargetFabricID != targetFabric || c.TargetVrfID != targetVrf {
		t.Errorf("move params mismatch: %+v", c)
	}
	if c.ExpectedCurrentFabricID != src.CurrentFabricID {
		t.Errorf("expected_current_fabric not propagated: %s vs %s",
			c.ExpectedCurrentFabricID, src.CurrentFabricID)
	}
}

func TestMove_SupernetNotFound(t *testing.T) {
	rec := sendMove(t, mountMove(&moveFakeQ{getSupernetErr: pgx.ErrNoRows}),
		uuid.New(), map[string]any{
			"fabric_id": uuid.NewString(), "vrf_id": uuid.NewString(),
		}, globalPrincipal())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_NotInLandingFabricIs409(t *testing.T) {
	orgID := uuid.New()
	src := happySupernet(orgID)
	src.CurrentFabricIsSystem = false
	q := &moveFakeQ{supernet: src}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": uuid.NewString(), "vrf_id": uuid.NewString(),
	}, globalPrincipal())
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
	if len(q.movedCalls) != 0 {
		t.Errorf("should not have called MoveSupernet")
	}
}

func TestMove_NoOwnerIs409(t *testing.T) {
	src := happySupernet(uuid.New())
	src.OwnerOrganizationID = nil
	q := &moveFakeQ{supernet: src}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": uuid.NewString(), "vrf_id": uuid.NewString(),
	}, globalPrincipal())
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_OrgOutOfScopeIs403(t *testing.T) {
	orgID := uuid.New()
	src := happySupernet(orgID)
	q := &moveFakeQ{supernet: src}
	// Principal scoped to a different org.
	p := orgScopedPrincipal(uuid.New())
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": uuid.NewString(), "vrf_id": uuid.NewString(),
	}, p)
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_MissingBodyFieldsIs400(t *testing.T) {
	src := happySupernet(uuid.New())
	q := &moveFakeQ{supernet: src}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": "", "vrf_id": "",
	}, globalPrincipal())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_BadFabricUUIDIs400(t *testing.T) {
	src := happySupernet(uuid.New())
	q := &moveFakeQ{supernet: src}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": "not-a-uuid", "vrf_id": uuid.NewString(),
	}, globalPrincipal())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_BadVrfUUIDIs400(t *testing.T) {
	src := happySupernet(uuid.New())
	q := &moveFakeQ{supernet: src}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": uuid.NewString(), "vrf_id": "not-a-uuid",
	}, globalPrincipal())
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_TargetVrfNotFoundIs404(t *testing.T) {
	src := happySupernet(uuid.New())
	q := &moveFakeQ{
		supernet:  src,
		getVrfErr: pgx.ErrNoRows,
	}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": uuid.NewString(), "vrf_id": uuid.NewString(),
	}, globalPrincipal())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_VrfInWrongFabricIs422(t *testing.T) {
	src := happySupernet(uuid.New())
	targetFabric := uuid.New()
	wrongFabric := uuid.New()
	q := &moveFakeQ{
		supernet: src,
		vrf:      &dbq.GetVrfForMoveRow{ID: uuid.New(), FabricID: wrongFabric},
	}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": targetFabric.String(), "vrf_id": uuid.NewString(),
	}, globalPrincipal())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_TargetFabricOutOfScopeIs403(t *testing.T) {
	orgID := uuid.New()
	src := happySupernet(orgID)
	targetFabric := uuid.New()
	q := &moveFakeQ{
		supernet: src,
		vrf:      &dbq.GetVrfForMoveRow{ID: uuid.New(), FabricID: targetFabric},
	}
	// Principal scoped to a different fabric.
	p := fabricScopedPrincipal(uuid.New())
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": targetFabric.String(), "vrf_id": uuid.NewString(),
	}, p)
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_RaceOrChildSubnetsIs409(t *testing.T) {
	// MoveSupernet returns ErrNoRows when the WHERE matched zero rows —
	// either a racer flipped fabric_id, or a child subnet appeared.
	// The handler maps that to 409 with a re-fetch hint.
	src := happySupernet(uuid.New())
	targetFabric := uuid.New()
	q := &moveFakeQ{
		supernet: src,
		vrf:      &dbq.GetVrfForMoveRow{ID: uuid.New(), FabricID: targetFabric},
		moveErr:  pgx.ErrNoRows,
	}
	rec := sendMove(t, mountMove(q), src.ID, map[string]any{
		"fabric_id": targetFabric.String(), "vrf_id": uuid.NewString(),
	}, globalPrincipal())
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d", rec.Code)
	}
}

func TestMove_BadIDInURLIs400(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &moveFakeQ{}}).Mount(r)
	req := httptest.NewRequest("POST", "/ipam/supernets/not-a-uuid/move", nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), globalPrincipal()))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
