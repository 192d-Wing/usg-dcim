// PR 81 — handler tests for rotate-key, disable-dnssec, delete key.
package dns

import (
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

type fakeLifecycleQ struct {
	fakeQ
	zone        dbq.DnsZone
	zoneErr     error
	activeByRole map[string][]dbq.DnsKey
	keyByID     map[uuid.UUID]dbq.DnsKey
	keyByIDErr  error
	allKeys     []dbq.DnsKey
	gotRetires  []uuid.UUID
	gotCreates  []dbq.CreateDnsKeyParams
	gotSigned   *bool
	gotTouches  int
	gotDeletes  []uuid.UUID
}

func (f *fakeLifecycleQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return f.zone, f.zoneErr
}
func (f *fakeLifecycleQ) ListDnsBlocklistPatternsByID(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *fakeLifecycleQ) GetDnsCatalogZone(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, nil
}
func (f *fakeLifecycleQ) ListDnsKeyTagsByCatalog(_ context.Context, _ uuid.UUID) ([]int32, error) {
	return nil, nil
}
func (f *fakeLifecycleQ) DeleteDnsKeysByCatalog(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeLifecycleQ) SetDnsCatalogZoneSigned(_ context.Context, _ dbq.SetDnsCatalogZoneSignedParams) error {
	return nil
}

func (f *fakeLifecycleQ) ListActiveDnsKeysForZoneAndRole(_ context.Context, a dbq.ListActiveDnsKeysForZoneAndRoleParams) ([]dbq.DnsKey, error) {
	return f.activeByRole[a.Role], nil
}

func (f *fakeLifecycleQ) CreateDnsKey(_ context.Context, a dbq.CreateDnsKeyParams) (dbq.DnsKey, error) {
	f.gotCreates = append(f.gotCreates, a)
	return dbq.DnsKey{ID: uuid.New(), ZoneID: a.ZoneID, Role: a.Role,
		Algorithm: a.Algorithm, KeyTag: a.KeyTag}, nil
}

func (f *fakeLifecycleQ) RetireDnsKey(_ context.Context, id uuid.UUID) (int64, error) {
	f.gotRetires = append(f.gotRetires, id)
	return 1, nil
}

func (f *fakeLifecycleQ) TouchDnsZone(_ context.Context, _ uuid.UUID) (int64, error) {
	f.gotTouches++
	return 1, nil
}

func (f *fakeLifecycleQ) ListDnsKeysByZone(_ context.Context, _ uuid.UUID) ([]dbq.DnsKey, error) {
	return f.allKeys, nil
}

func (f *fakeLifecycleQ) SetDnsZoneSigned(_ context.Context, a dbq.SetDnsZoneSignedParams) (int64, error) {
	signed := a.Signed
	f.gotSigned = &signed
	return 1, nil
}

func (f *fakeLifecycleQ) DeleteAllDnsKeysForZone(_ context.Context, _ uuid.UUID) ([]dbq.DnsKey, error) {
	return f.allKeys, nil
}

func (f *fakeLifecycleQ) GetDnsKey(_ context.Context, id uuid.UUID) (dbq.DnsKey, error) {
	if f.keyByIDErr != nil {
		return dbq.DnsKey{}, f.keyByIDErr
	}
	if f.keyByID != nil {
		if k, ok := f.keyByID[id]; ok {
			return k, nil
		}
	}
	return dbq.DnsKey{}, pgx.ErrNoRows
}

func (f *fakeLifecycleQ) DeleteDnsKey(_ context.Context, id uuid.UUID) (int64, error) {
	f.gotDeletes = append(f.gotDeletes, id)
	return 1, nil
}

func mountLifecycle(f *fakeLifecycleQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// ---- rotate-key ----

func TestRotateKey_HappyPath(t *testing.T) {
	id := uuid.New()
	prevKey := dbq.DnsKey{ID: uuid.New(), Role: "ksk", Algorithm: "ecdsap256sha256"}
	f := &fakeLifecycleQ{
		zone:         dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true},
		activeByRole: map[string][]dbq.DnsKey{"ksk": {prevKey}},
	}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/ksk", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.gotCreates) != 1 {
		t.Errorf("want 1 new key, got %d creates", len(f.gotCreates))
	}
	if f.gotCreates[0].Role != "ksk" {
		t.Errorf("new key role = %q, want ksk", f.gotCreates[0].Role)
	}
	if f.gotCreates[0].Algorithm != "ecdsap256sha256" {
		t.Errorf("algorithm = %q, should inherit from prior key", f.gotCreates[0].Algorithm)
	}
	if len(f.gotRetires) != 1 || f.gotRetires[0] != prevKey.ID {
		t.Errorf("retires = %v, want [%s]", f.gotRetires, prevKey.ID)
	}
	if f.gotTouches != 1 {
		t.Errorf("want 1 zone touch, got %d", f.gotTouches)
	}
}

func TestRotateKey_FirstRotationUsesDefaultAlgorithm(t *testing.T) {
	// No active keys yet → algorithm falls back to ECDSAP256.
	id := uuid.New()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true},
	}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/zsk", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotCreates[0].Algorithm != "ecdsap256sha256" {
		t.Errorf("algorithm = %q, want ecdsap256sha256 default", f.gotCreates[0].Algorithm)
	}
}

func TestRotateKey_InheritsExistingAlgorithm(t *testing.T) {
	id := uuid.New()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true},
		activeByRole: map[string][]dbq.DnsKey{
			"ksk": {{ID: uuid.New(), Algorithm: "ed25519"}},
		},
	}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/ksk", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotCreates[0].Algorithm != "ed25519" {
		t.Errorf("algorithm = %q, want ed25519 (inherited)", f.gotCreates[0].Algorithm)
	}
}

func TestRotateKey_UnsignedZoneIs422(t *testing.T) {
	id := uuid.New()
	f := &fakeLifecycleQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: false}}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/ksk", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestRotateKey_FrozenZoneIs422(t *testing.T) {
	id := uuid.New()
	f := &fakeLifecycleQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true, Frozen: true}}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/ksk", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestRotateKey_BadRoleIs400(t *testing.T) {
	id := uuid.New()
	rec := authed(t, mountLifecycle(&fakeLifecycleQ{}),
		"POST", "/dns/zones/"+id.String()+"/rotate-key/weird", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRotateKey_NotFoundIs404(t *testing.T) {
	f := &fakeLifecycleQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountLifecycle(f),
		"POST", "/dns/zones/"+uuid.New().String()+"/rotate-key/ksk", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRotateKey_RetiresMultipleActiveKeys(t *testing.T) {
	// During a key-rollover overlap there can be more than one
	// active key of the same role. All should be retired.
	id := uuid.New()
	k1 := uuid.New()
	k2 := uuid.New()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true},
		activeByRole: map[string][]dbq.DnsKey{
			"zsk": {{ID: k1, Algorithm: "ecdsap256sha256"}, {ID: k2, Algorithm: "ecdsap256sha256"}},
		},
	}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/zsk", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(f.gotRetires) != 2 {
		t.Errorf("retires = %v, want both old keys", f.gotRetires)
	}
}

// ---- disable-dnssec ----

func TestDisableDnssec_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeLifecycleQ{
		zone:    dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true},
		allKeys: []dbq.DnsKey{{KeyTag: 1}, {KeyTag: 2}},
	}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/disable-dnssec", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if f.gotSigned == nil || *f.gotSigned {
		t.Errorf("signed should be false: %v", f.gotSigned)
	}
	if f.gotTouches != 1 {
		t.Errorf("want 1 touch, got %d", f.gotTouches)
	}
}

func TestDisableDnssec_IdempotentOnUnsignedZone(t *testing.T) {
	// signed=false → short-circuit to 204 without touching DB.
	id := uuid.New()
	f := &fakeLifecycleQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: false}}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/disable-dnssec", nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d", rec.Code)
	}
	if f.gotSigned != nil {
		t.Errorf("should not have called SetDnsZoneSigned on already-unsigned zone")
	}
}

func TestDisableDnssec_FrozenZoneIs422(t *testing.T) {
	id := uuid.New()
	f := &fakeLifecycleQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true, Frozen: true}}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/disable-dnssec", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestDisableDnssec_NotFoundIs404(t *testing.T) {
	f := &fakeLifecycleQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountLifecycle(f),
		"POST", "/dns/zones/"+uuid.New().String()+"/disable-dnssec", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// ---- DELETE /keys/{id} ----

func TestDeleteDnsKey_HappyPath(t *testing.T) {
	keyID := uuid.New()
	zoneID := uuid.New()
	now := time.Now().UTC()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New()},
		keyByID: map[uuid.UUID]dbq.DnsKey{
			keyID: {ID: keyID, ZoneID: &zoneID, RetiredAt: &now},
		},
	}
	rec := authed(t, mountLifecycle(f), "DELETE", "/dns/keys/"+keyID.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.gotDeletes) != 1 || f.gotDeletes[0] != keyID {
		t.Errorf("deletes = %v", f.gotDeletes)
	}
}

func TestDeleteDnsKey_ActiveKeyIs422(t *testing.T) {
	// retired_at IS NULL → active → refused.
	keyID := uuid.New()
	zoneID := uuid.New()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New()},
		keyByID: map[uuid.UUID]dbq.DnsKey{
			keyID: {ID: keyID, ZoneID: &zoneID, RetiredAt: nil},
		},
	}
	rec := authed(t, mountLifecycle(f), "DELETE", "/dns/keys/"+keyID.String(), nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (active key)", rec.Code)
	}
}

func TestDeleteDnsKey_FrozenZoneIs422(t *testing.T) {
	keyID := uuid.New()
	zoneID := uuid.New()
	now := time.Now().UTC()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: zoneID, FabricID: uuid.New(), Frozen: true},
		keyByID: map[uuid.UUID]dbq.DnsKey{
			keyID: {ID: keyID, ZoneID: &zoneID, RetiredAt: &now},
		},
	}
	rec := authed(t, mountLifecycle(f), "DELETE", "/dns/keys/"+keyID.String(), nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (frozen)", rec.Code)
	}
}

func TestDeleteDnsKey_NotFoundIs404(t *testing.T) {
	rec := authed(t, mountLifecycle(&fakeLifecycleQ{}),
		"DELETE", "/dns/keys/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteDnsKey_BadUUIDIs400(t *testing.T) {
	rec := authed(t, mountLifecycle(&fakeLifecycleQ{}),
		"DELETE", "/dns/keys/not-a-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDeleteDnsKey_RequiresDeleteCap(t *testing.T) {
	keyID := uuid.New()
	req := httptest.NewRequest("DELETE", "/dns/keys/"+keyID.String(), nil)
	p := auth.Principal{Capabilities: []string{"dns:keys:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountLifecycle(&fakeLifecycleQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// Sanity: rotate response shape includes the full roster.

func TestRotateKey_ResponseIncludesFullRoster(t *testing.T) {
	id := uuid.New()
	newID := uuid.New()
	f := &fakeLifecycleQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true},
		allKeys: []dbq.DnsKey{
			{ID: newID, Role: "ksk", KeyTag: 100},
			{ID: uuid.New(), Role: "ksk", KeyTag: 99, RetiredAt: ptrTime(time.Now())},
		},
	}
	rec := authed(t, mountLifecycle(f), "POST", "/dns/zones/"+id.String()+"/rotate-key/ksk", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []dbq.DnsKey
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 2 {
		t.Errorf("response should include both active and retired keys, got %d", len(out))
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
