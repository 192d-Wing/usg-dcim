package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// fakeBundleQ implements bundleQuerier directly so the bundle
// integration tests can configure each loader's return without
// going through the full DNS Querier surface.
type fakeBundleQ struct {
	server         dbq.DnsServer
	serverErr      error
	zones          []dbq.DnsZone
	zonesErr       error
	records        []dbq.DnsRecordForBundle
	recordsErr     error
	unhealthyIDs   []uuid.UUID
	unhealthyErr   error
}

func (f *fakeBundleQ) GetDnsServer(_ context.Context, _ uuid.UUID) (dbq.DnsServer, error) {
	return f.server, f.serverErr
}
func (f *fakeBundleQ) ListDnsZonesByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsZone, error) {
	return f.zones, f.zonesErr
}
func (f *fakeBundleQ) ListDnsRecordsByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsRecordForBundle, error) {
	return f.records, f.recordsErr
}
func (f *fakeBundleQ) ListUnhealthyEnabledHealthChecksByFabric(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return f.unhealthyIDs, f.unhealthyErr
}
func (f *fakeBundleQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, pgx.ErrNoRows
}
func (f *fakeBundleQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.AuthDnsServerForCatalog, error) {
	return nil, nil
}
func (f *fakeBundleQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return nil, nil
}
func (f *fakeBundleQ) ListDnsViewsByFabric(_ context.Context, _ uuid.UUID) ([]dbq.DnsView, error) {
	return nil, nil
}
func (f *fakeBundleQ) ListApexZoneNamesByFabric(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *fakeBundleQ) GetSameSiteAuthUnicastIP(_ context.Context, _ uuid.UUID) (string, error) {
	return "", pgx.ErrNoRows
}
func (f *fakeBundleQ) ListDnsForwardersForBundle(_ context.Context, _ uuid.UUID) ([]dbq.DnsForwarderRow, error) {
	return nil, nil
}
func (f *fakeBundleQ) ListEnabledBlocklistsWithPatternsByFabric(_ context.Context, _ uuid.UUID) ([]dbq.BlocklistForBundleRow, error) {
	return nil, nil
}
func (f *fakeBundleQ) GetFabricForRecursiveBundle(_ context.Context, _ uuid.UUID) (dbq.FabricForRecursiveBundle, error) {
	return dbq.FabricForRecursiveBundle{}, pgx.ErrNoRows
}
func (f *fakeBundleQ) GetSystemSetting(_ context.Context, _ string) (dbq.SystemSetting, error) {
	return dbq.SystemSetting{}, pgx.ErrNoRows
}

func mkBundleZoneRow(id uuid.UUID, name string, ts time.Time) dbq.DnsZone {
	return dbq.DnsZone{
		ID: id, Name: name, Kind: "apex",
		SoaMname: "ns1", SoaRname: "hostmaster",
		SoaRefresh: 900, SoaRetry: 900, SoaExpire: 1800, SoaMinimum: 60,
		DefaultTTL: 60,
		UpdatedAt:  ts,
	}
}

// ===== AssembleAuthBundle =====

func TestAssembleAuthBundle_EmitsOneZoneFilePerZone(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone1 := mkBundleZoneRow(uuid.New(), "a.example.", ts)
	zone2 := mkBundleZoneRow(uuid.New(), "b.example.", ts)
	in := AuthBundleInput{
		Server: dbq.DnsServer{Role: "auth"},
		Zones:  []dbq.DnsZone{zone1, zone2},
		RecordsByZone: map[uuid.UUID][]dbq.DnsRecordForBundle{},
	}
	out, err := AssembleAuthBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Zones) != 2 {
		t.Fatalf("expected 2 zone files; got %d (%v)", len(out.Zones), out.Zones)
	}
	if _, ok := out.Zones["a.example..zone"]; !ok {
		t.Errorf("missing a.example..zone in output; keys=%v", mapKeys(out.Zones))
	}
	if _, ok := out.Zones["b.example..zone"]; !ok {
		t.Errorf("missing b.example..zone; keys=%v", mapKeys(out.Zones))
	}
	if out.Engine != "coredns" {
		t.Errorf("engine: got %q want coredns", out.Engine)
	}
	if out.Corefile == "" {
		t.Error("corefile should not be empty for an auth bundle with zones")
	}
	if len(out.Etag) != 32 {
		t.Errorf("etag must be 32 hex chars; got %d (%q)", len(out.Etag), out.Etag)
	}
}

func TestAssembleAuthBundle_UnhealthyRecordFiltered(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone := mkBundleZoneRow(uuid.New(), "z.example.", ts)
	hcID := uuid.New()
	in := AuthBundleInput{
		Server: dbq.DnsServer{Role: "auth"},
		Zones:  []dbq.DnsZone{zone},
		RecordsByZone: map[uuid.UUID][]dbq.DnsRecordForBundle{
			zone.ID: {
				{Name: "ok", Type: "A", Data: []byte(`{"target":"10.0.0.1"}`)},
				{Name: "sick", Type: "A", Data: []byte(`{"target":"10.0.0.2"}`), HealthCheckID: &hcID},
			},
		},
		UnhealthyCheckIDs: map[uuid.UUID]struct{}{hcID: {}},
	}
	out, _ := AssembleAuthBundle(in)
	zoneText := out.Zones["z.example..zone"]
	if !strings.Contains(zoneText, "10.0.0.1") {
		t.Error("healthy record missing from zone file")
	}
	if strings.Contains(zoneText, "10.0.0.2") {
		t.Error("unhealthy record leaked into zone file")
	}
}

func TestAssembleAuthBundle_ExtraLinesAppended(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone := mkBundleZoneRow(uuid.New(), "z.example.", ts)
	in := AuthBundleInput{
		Server: dbq.DnsServer{Role: "auth"},
		Zones:  []dbq.DnsZone{zone},
		ExtraLinesByZone: map[uuid.UUID][]string{
			zone.ID: {"@\tIN\tCDS\t12345 13 2 ABCDEF"},
		},
	}
	out, _ := AssembleAuthBundle(in)
	zoneText := out.Zones["z.example..zone"]
	if !strings.Contains(zoneText, "; --- DS (for DCIM-owned children)") {
		t.Error("extras comment marker missing")
	}
	if !strings.Contains(zoneText, "ABCDEF") {
		t.Error("extras line not appended")
	}
}

func TestAssembleAuthBundle_DeterministicEtag(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone := mkBundleZoneRow(uuid.New(), "z.example.", ts)
	in := AuthBundleInput{
		Server: dbq.DnsServer{Role: "auth"},
		Zones:  []dbq.DnsZone{zone},
	}
	out1, _ := AssembleAuthBundle(in)
	out2, _ := AssembleAuthBundle(in)
	if out1.Etag != out2.Etag {
		t.Errorf("etag flapped: %s vs %s", out1.Etag, out2.Etag)
	}
}

func TestAssembleAuthBundle_RoleInZonesDir(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone := mkBundleZoneRow(uuid.New(), "z.example.", ts)
	in := AuthBundleInput{Server: dbq.DnsServer{Role: "auth"}, Zones: []dbq.DnsZone{zone}}
	out, _ := AssembleAuthBundle(in)
	if !strings.Contains(out.Corefile, "/var/lib/dcim-dns/auth/zones") {
		t.Errorf("zones_dir not threaded with role; corefile=%q", out.Corefile)
	}
}

// ===== HTTP endpoint =====

func mountBundle(q bundleQuerier) http.Handler {
	r := chi.NewRouter()
	// Mount the bundle handler directly. The handler isn't yet
	// wired into Handler.Mount because the cutover blocks on the
	// catalog/extras/split-horizon helpers — but exposing it here
	// lets us test the pure HTTP behavior in isolation.
	h := &Handler{}
	r.Get("/dns/servers/{id}/bundle", func(w http.ResponseWriter, r *http.Request) {
		h.bundleHandlerWith(w, r, q)
	})
	return r
}

func doBundle(t *testing.T, h http.Handler, path, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"*"}})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBundleHandler_ServerNotFound(t *testing.T) {
	q := &fakeBundleQ{serverErr: pgx.ErrNoRows}
	rec := doBundle(t, mountBundle(q), "/dns/servers/"+uuid.New().String()+"/bundle", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404 dns server not found", rec.Code)
	}
}

// PR 35 wired the recursive path. Bundle returns 200 with the
// recursive Corefile + RPZ zones (when on Hickory) instead of the
// 501 that PR 30 documented.
func TestBundleHandler_RecursiveReturnsBundle(t *testing.T) {
	q := &recursiveOKFakeQ{role: "recursive"}
	rec := doBundle(t, mountBundle(q), "/dns/servers/"+uuid.New().String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s, want 200 with recursive bundle", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("ETag header not set on recursive 200")
	}
	var out BundleResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body decode: %v", err)
	}
	if out.Engine != "coredns" {
		t.Errorf("default engine should be coredns; got %q", out.Engine)
	}
	if !strings.Contains(out.Corefile, ".:53 {") {
		t.Errorf("recursive Corefile missing catchall block; got %q", out.Corefile)
	}
}

// recursiveOKFakeQ wraps fakeBundleQ but returns a populated
// FabricForRecursiveBundle so the loader doesn't trip on ErrNoRows.
type recursiveOKFakeQ struct {
	fakeBundleQ
	role string
}

func (f *recursiveOKFakeQ) GetDnsServer(_ context.Context, id uuid.UUID) (dbq.DnsServer, error) {
	return dbq.DnsServer{ID: id, Role: f.role, FabricID: uuid.New()}, nil
}
func (f *recursiveOKFakeQ) GetFabricForRecursiveBundle(_ context.Context, id uuid.UUID) (dbq.FabricForRecursiveBundle, error) {
	return dbq.FabricForRecursiveBundle{ID: id, RecursiveEngine: "coredns"}, nil
}

func TestBundleHandler_AuthOK_SetsEtagHeader(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone := mkBundleZoneRow(uuid.New(), "z.example.", ts)
	q := &fakeBundleQ{
		server: dbq.DnsServer{Role: "auth"},
		zones:  []dbq.DnsZone{zone},
	}
	rec := doBundle(t, mountBundle(q), "/dns/servers/"+uuid.New().String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Error("ETag header not set on 200")
	}
	if !strings.HasPrefix(etag, `"`) || !strings.HasSuffix(etag, `"`) {
		t.Errorf("ETag should be quoted per RFC 9110; got %q", etag)
	}
	// Body decodes as BundleResult.
	var out BundleResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body decode: %v", err)
	}
	if out.Engine != "coredns" {
		t.Errorf("engine: got %q", out.Engine)
	}
	if out.Etag == "" {
		t.Error("bundle.etag empty")
	}
}

// If-None-Match matching the current etag → 304 with no body but
// the ETag header still set (RFC 9110 §15.4.5).
func TestBundleHandler_IfNoneMatch304(t *testing.T) {
	ts := time.Unix(1700000000, 0).UTC()
	zone := mkBundleZoneRow(uuid.New(), "z.example.", ts)
	q := &fakeBundleQ{
		server: dbq.DnsServer{Role: "auth"},
		zones:  []dbq.DnsZone{zone},
	}
	// First call to learn the etag.
	first := doBundle(t, mountBundle(q), "/dns/servers/"+uuid.New().String()+"/bundle", "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first call didn't set ETag")
	}
	// Second call with If-None-Match → 304.
	rec := doBundle(t, mountBundle(q), "/dns/servers/"+uuid.New().String()+"/bundle", etag)
	if rec.Code != http.StatusNotModified {
		t.Errorf("got %d, want 304", rec.Code)
	}
	if rec.Header().Get("ETag") != etag {
		t.Errorf("304 must still echo ETag header; got %q want %q", rec.Header().Get("ETag"), etag)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 must have empty body; got %d bytes", rec.Body.Len())
	}
}

func TestEtagMatches(t *testing.T) {
	cases := []struct {
		header, etag string
		want         bool
	}{
		{`"abc"`, "abc", true},
		{"abc", "abc", true},
		{`"abc"`, "xyz", false},
		{"abc", "xyz", false},
		{``, "abc", false},
	}
	for _, c := range cases {
		if got := etagMatches(c.header, c.etag); got != c.want {
			t.Errorf("etagMatches(%q, %q): got %v want %v", c.header, c.etag, got, c.want)
		}
	}
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
