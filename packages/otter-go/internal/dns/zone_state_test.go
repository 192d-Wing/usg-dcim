// PR 70 — handler tests for zone freeze/unfreeze + NSEC3 toggles.
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
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// fakeZoneStateQ embeds fakeQ and lets the NSEC3 tests stub
// GetDnsZone to return a specific signed/frozen state. The
// frozen-toggle endpoints don't need GetDnsZone (they use
// GetDnsZoneFabricID + SetDnsZoneFrozen), so the fakeQ default is
// fine for those.
type fakeZoneStateQ struct {
	fakeQ
	zone        dbq.DnsZone
	zoneErr     error
	fabricIDErr error
}

func (f *fakeZoneStateQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return f.zone, f.zoneErr
}

func (f *fakeZoneStateQ) GetDnsZoneFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, f.fabricIDErr
}

func mountZS(f *fakeZoneStateQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// authed POSTs to `path` with a wildcard principal so capability
// + scope checks always pass.
func authed(t *testing.T, h http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ---- freeze / unfreeze ----

func TestFreezeZone_FlipsFlag(t *testing.T) {
	id := uuid.New()
	rec := authed(t, mountZS(&fakeZoneStateQ{}), "POST", "/dns/zones/"+id.String()+"/freeze", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var z dbq.DnsZone
	_ = json.NewDecoder(rec.Body).Decode(&z)
	if !z.Frozen {
		t.Errorf("Frozen = false, want true")
	}
}

func TestUnfreezeZone_FlipsFlag(t *testing.T) {
	id := uuid.New()
	rec := authed(t, mountZS(&fakeZoneStateQ{}), "POST", "/dns/zones/"+id.String()+"/unfreeze", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var z dbq.DnsZone
	_ = json.NewDecoder(rec.Body).Decode(&z)
	if z.Frozen {
		t.Errorf("Frozen = true, want false")
	}
}

func TestFreezeZone_BadUUIDIs400(t *testing.T) {
	rec := authed(t, mountZS(&fakeZoneStateQ{}), "POST", "/dns/zones/not-a-uuid/freeze", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFreezeZone_NotFoundIs404(t *testing.T) {
	f := &fakeZoneStateQ{fabricIDErr: pgx.ErrNoRows}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+uuid.New().String()+"/freeze", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFreezeZone_RequiresUpdateCapability(t *testing.T) {
	id := uuid.New()
	req := httptest.NewRequest("POST", "/dns/zones/"+id.String()+"/freeze", nil)
	p := auth.Principal{Capabilities: []string{"dns:zones:read"}} // wrong cap
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountZS(&fakeZoneStateQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- NSEC3 set ----

func TestSetZoneNsec3_HappyPath(t *testing.T) {
	id := uuid.New()
	salt := "abcd"
	body, _ := json.Marshal(map[string]any{"salt": salt, "iterations": 1, "opt_out": false})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var z dbq.DnsZone
	_ = json.NewDecoder(rec.Body).Decode(&z)
	if z.Nsec3Salt == nil || *z.Nsec3Salt != salt {
		t.Errorf("Salt = %v, want %q", z.Nsec3Salt, salt)
	}
	if z.Nsec3Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", z.Nsec3Iterations)
	}
}

func TestSetZoneNsec3_UnsignedZoneIs422(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"iterations": 1})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: false}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestSetZoneNsec3_FrozenZoneIs422(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"iterations": 1})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true, Frozen: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestSetZoneNsec3_RejectsIterationsOver150(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"iterations": 151})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (iterations cap is 150)", rec.Code)
	}
}

func TestSetZoneNsec3_RejectsNegativeIterations(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"iterations": -1})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestSetZoneNsec3_RejectsNonHexSalt(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"salt": "not-hex", "iterations": 1})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for non-hex salt", rec.Code)
	}
}

func TestSetZoneNsec3_RejectsOddLengthSalt(t *testing.T) {
	// Odd-length hex isn't valid byte data — the regex enforces
	// {2,64} so 3-char salts fail.
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"salt": "abc", "iterations": 1})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for odd-length salt", rec.Code)
	}
}

func TestSetZoneNsec3_EmptySaltNormalizesToNull(t *testing.T) {
	// Empty string means "let the renderer pick" → stored as NULL.
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"salt": "", "iterations": 1})
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true}}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var z dbq.DnsZone
	_ = json.NewDecoder(rec.Body).Decode(&z)
	if z.Nsec3Salt != nil {
		t.Errorf("Salt = %v, want nil for empty-string input", z.Nsec3Salt)
	}
}

func TestSetZoneNsec3_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"iterations": 1})
	f := &fakeZoneStateQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountZS(f), "POST", "/dns/zones/"+id.String()+"/nsec3", body)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// ---- NSEC3 clear ----

func TestClearZoneNsec3_ResetsParams(t *testing.T) {
	id := uuid.New()
	prev := "abcd"
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true, Nsec3Salt: &prev, Nsec3Iterations: 5}}
	rec := authed(t, mountZS(f), "DELETE", "/dns/zones/"+id.String()+"/nsec3", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var z dbq.DnsZone
	_ = json.NewDecoder(rec.Body).Decode(&z)
	if z.Nsec3Salt != nil {
		t.Errorf("Salt = %v, want nil after clear", z.Nsec3Salt)
	}
	if z.Nsec3Iterations != 0 {
		t.Errorf("Iterations = %d, want 0", z.Nsec3Iterations)
	}
}

func TestClearZoneNsec3_FrozenZoneIs422(t *testing.T) {
	id := uuid.New()
	f := &fakeZoneStateQ{zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Signed: true, Frozen: true}}
	rec := authed(t, mountZS(f), "DELETE", "/dns/zones/"+id.String()+"/nsec3", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestClearZoneNsec3_NotFoundIs404(t *testing.T) {
	id := uuid.New()
	f := &fakeZoneStateQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountZS(f), "DELETE", "/dns/zones/"+id.String()+"/nsec3", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
