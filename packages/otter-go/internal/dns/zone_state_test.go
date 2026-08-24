// PR 70 — handler tests for zone freeze/unfreeze + NSEC3 toggles.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	zoneRecords []dbq.ListAllRecordsInZoneRow
}

func (f *fakeZoneStateQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return f.zone, f.zoneErr
}

func (f *fakeZoneStateQ) GetDnsZoneFabricID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, f.fabricIDErr
}

func (f *fakeZoneStateQ) ListAllRecordsInZone(_ context.Context, _ uuid.UUID) ([]dbq.ListAllRecordsInZoneRow, error) {
	return f.zoneRecords, nil
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

// ---- PR 71: preview ----

func TestPreviewZone_ReturnsBindTextAndRecordCount(t *testing.T) {
	id := uuid.New()
	rec1, _ := json.Marshal(map[string]any{"target": "10.0.0.1"})
	f := &fakeZoneStateQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com", DefaultTTL: 60,
			SoaMname: "ns1", SoaRname: "hostmaster", SoaRefresh: 900, SoaRetry: 900, SoaExpire: 1800, SoaMinimum: 60},
		zoneRecords: []dbq.ListAllRecordsInZoneRow{
			{ID: uuid.New(), Name: "www", Type: "A", Data: rec1},
		},
	}
	rec := authed(t, mountZS(f), "GET", "/dns/zones/"+id.String()+"/preview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ZoneID      string `json:"zone_id"`
		Name        string `json:"name"`
		Text        string `json:"text"`
		RecordCount int    `json:"record_count"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.ZoneID != id.String() {
		t.Errorf("zone_id = %q", resp.ZoneID)
	}
	if resp.Name != "example.com" {
		t.Errorf("name = %q", resp.Name)
	}
	if resp.RecordCount != 1 {
		t.Errorf("record_count = %d, want 1", resp.RecordCount)
	}
	if !strings.Contains(resp.Text, "$ORIGIN example.com.") {
		t.Errorf("text missing $ORIGIN: %q", resp.Text)
	}
	if !strings.Contains(resp.Text, "www\t60\tIN\tA\t10.0.0.1") {
		t.Errorf("text missing record line: %q", resp.Text)
	}
}

func TestPreviewZone_NotFoundIs404(t *testing.T) {
	f := &fakeZoneStateQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountZS(f), "GET", "/dns/zones/"+uuid.New().String()+"/preview", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPreviewZone_BadRecordIs422(t *testing.T) {
	// A record with an unknown type slipped into the DB — the
	// renderer surfaces it instead of silently dropping. Helps
	// operators catch a bad migration or hand-INSERT.
	id := uuid.New()
	f := &fakeZoneStateQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com", DefaultTTL: 60},
		zoneRecords: []dbq.ListAllRecordsInZoneRow{
			{ID: uuid.New(), Name: "x", Type: "DNSKEY", Data: json.RawMessage(`{}`)},
		},
	}
	rec := authed(t, mountZS(f), "GET", "/dns/zones/"+id.String()+"/preview", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestPreviewZone_BadUUIDIs400(t *testing.T) {
	rec := authed(t, mountZS(&fakeZoneStateQ{}), "GET", "/dns/zones/not-a-uuid/preview", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
