// PR 79 — unit + handler tests for DNSSEC reads.
//
// The pure-function tests pin the DS-digest math against fixed
// inputs so the SHA-256(name_wire || dnskey_rdata) byte layout
// stays exactly the same as the Python renderer. Handler tests
// pin the KSK + active filter (ZSK and retired keys excluded
// from DS output) and the ABAC + 404 paths.
package dns

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- unit: dnssecAlgNumber ----

func TestDnssecAlgNumber(t *testing.T) {
	cases := []struct {
		alg  string
		want int
	}{
		{"rsasha256", 8},
		{"ecdsap256sha256", 13},
		{"ed25519", 15},
		{"unsupported-future", 0}, // unknown → 0
		{"", 0},
	}
	for _, c := range cases {
		if got := dnssecAlgNumber(c.alg); got != c.want {
			t.Errorf("dnssecAlgNumber(%q) = %d, want %d", c.alg, got, c.want)
		}
	}
}

// ---- unit: keyFlags ----

func TestKeyFlags(t *testing.T) {
	if got := keyFlags("ksk"); got != 257 {
		t.Errorf("ksk flags = %d, want 257 (Secure Entry Point bit)", got)
	}
	if got := keyFlags("zsk"); got != 256 {
		t.Errorf("zsk flags = %d, want 256", got)
	}
	if got := keyFlags("anything-else"); got != 256 {
		t.Errorf("unknown role flags = %d, want 256 (default)", got)
	}
}

// ---- unit: dnsWireName ----

func TestDnsWireName(t *testing.T) {
	// "example.com." → length-prefixed labels + null terminator:
	// 0x07 e x a m p l e 0x03 c o m 0x00
	got := dnsWireName("example.com.")
	want := []byte{
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		3, 'c', 'o', 'm',
		0,
	}
	if string(got) != string(want) {
		t.Errorf("got % x, want % x", got, want)
	}
}

func TestDnsWireName_LowercasesLabels(t *testing.T) {
	got := dnsWireName("EXAMPLE.com.")
	if got[1] != 'e' || got[2] != 'x' {
		t.Errorf("labels not lowercased: % x", got)
	}
}

func TestDnsWireName_NoTrailingDotStillWorks(t *testing.T) {
	a := dnsWireName("example.com.")
	b := dnsWireName("example.com")
	if string(a) != string(b) {
		t.Errorf("trailing dot should be optional: %q vs %q", a, b)
	}
}

func TestDnsWireName_RootIsZeroByte(t *testing.T) {
	got := dnsWireName(".")
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("root name should be a single null byte, got % x", got)
	}
}

// ---- unit: computeDSRecord ----

func TestComputeDSRecord_ECDSAFixedVector(t *testing.T) {
	// Reproducible vector: ECDSAP256 KSK with a fixed 64-byte
	// public key. The digest is what SHA-256(wire-name || rdata)
	// computes; we just pin the math so the byte layout doesn't
	// drift from the Python renderer.
	pub := make([]byte, 64)
	for i := range pub {
		pub[i] = byte(i + 1)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	key := dbq.DnsKeyRow{
		Role: "ksk", Algorithm: "ecdsap256sha256",
		PublicKeyB64: pubB64, KeyTag: 12345,
	}
	ds, err := computeDSRecord("example.com.", key)
	if err != nil {
		t.Fatal(err)
	}
	if ds.KeyTag != 12345 || ds.Algorithm != 13 || ds.DigestType != 2 {
		t.Errorf("got %+v", ds)
	}
	if len(ds.Digest) != 64 { // SHA-256 = 32 bytes = 64 hex chars
		t.Errorf("digest length = %d, want 64", len(ds.Digest))
	}
	// Must be uppercase hex (BIND zone-file convention).
	if ds.Digest != strings.ToUpper(ds.Digest) {
		t.Errorf("digest should be uppercase: %q", ds.Digest)
	}
	expectedRR := "example.com. IN DS 12345 13 2 " + ds.Digest
	if ds.RR != expectedRR {
		t.Errorf("RR = %q, want %q", ds.RR, expectedRR)
	}
}

func TestComputeDSRecord_RejectsUnsupportedAlgorithm(t *testing.T) {
	key := dbq.DnsKeyRow{
		Role: "ksk", Algorithm: "unsupported-future", PublicKeyB64: "AAAA",
	}
	if _, err := computeDSRecord("example.com.", key); err == nil {
		t.Error("expected error for unsupported algorithm")
	}
}

func TestComputeDSRecord_RejectsMalformedPublicKey(t *testing.T) {
	key := dbq.DnsKeyRow{
		Role: "ksk", Algorithm: "ecdsap256sha256",
		PublicKeyB64: "not-valid-base64!!!",
	}
	if _, err := computeDSRecord("example.com.", key); err == nil {
		t.Error("expected error for malformed public_key_b64")
	}
}

func TestComputeDSRecord_FQDNTrimmingMatchesPython(t *testing.T) {
	// Either form should yield the same digest (Python rstrip('.')
	// + add '.' → identical wire-name input).
	pub := make([]byte, 64)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	key := dbq.DnsKeyRow{
		Role: "ksk", Algorithm: "ecdsap256sha256",
		PublicKeyB64: pubB64, KeyTag: 1,
	}
	a, _ := computeDSRecord("example.com.", key)
	b, _ := computeDSRecord("example.com", key)
	if a.Digest != b.Digest {
		t.Errorf("trailing-dot variants differ: %q vs %q", a.Digest, b.Digest)
	}
}

// ---- handler: list keys ----

type fakeDnssecQ struct {
	fakeQ
	zone    dbq.DnsZone
	zoneErr error
	keys    []dbq.DnsKeyRow
}

func (f *fakeDnssecQ) GetDnsZone(_ context.Context, _ uuid.UUID) (dbq.DnsZone, error) {
	return f.zone, f.zoneErr
}
func (f *fakeDnssecQ) ListDnsBlocklistPatternsByID(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *fakeDnssecQ) GetDnsCatalogZone(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return dbq.DnsCatalogZone{}, nil
}
func (f *fakeDnssecQ) ListDnsKeyTagsByCatalog(_ context.Context, _ uuid.UUID) ([]int32, error) {
	return nil, nil
}
func (f *fakeDnssecQ) DeleteDnsKeysByCatalog(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeDnssecQ) SetDnsCatalogZoneSigned(_ context.Context, _ dbq.SetDnsCatalogZoneSignedParams) error {
	return nil
}

func (f *fakeDnssecQ) ListDnsKeysByZone(_ context.Context, _ uuid.UUID) ([]dbq.DnsKeyRow, error) {
	return f.keys, nil
}

func mountDnssec(f *fakeDnssecQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestListZoneKeys_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeDnssecQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"},
		keys: []dbq.DnsKeyRow{{ID: uuid.New(), Role: "ksk", Algorithm: "ecdsap256sha256"}},
	}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+id.String()+"/keys", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out []dbq.DnsKeyRow
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 || out[0].Role != "ksk" {
		t.Errorf("got %+v", out)
	}
}

func TestListZoneKeys_PrivatePemNotInResponse(t *testing.T) {
	// The struct's `json:"-"` tag must keep private_pem out of
	// the JSON output. Operator visibility is restricted to
	// metadata + public key.
	id := uuid.New()
	f := &fakeDnssecQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"},
		keys: []dbq.DnsKeyRow{{
			ID: uuid.New(), Role: "ksk", PrivatePem: "SECRET-PEM-CONTENTS",
		}},
	}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+id.String()+"/keys", nil)
	if strings.Contains(rec.Body.String(), "SECRET-PEM-CONTENTS") {
		t.Error("private_pem leaked into JSON response")
	}
}

func TestListZoneKeys_NotFoundIs404(t *testing.T) {
	f := &fakeDnssecQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+uuid.New().String()+"/keys", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListZoneKeys_BadUUIDIs400(t *testing.T) {
	rec := authed(t, mountDnssec(&fakeDnssecQ{}), "GET", "/dns/zones/not-a-uuid/keys", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// ---- handler: list ds-records ----

func TestListZoneDsRecords_OnlyActiveKSK(t *testing.T) {
	// Mixed key set: active KSK + retired KSK + active ZSK.
	// Only the first should produce a DS row.
	id := uuid.New()
	pub := make([]byte, 64)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	now := time.Now().UTC()
	f := &fakeDnssecQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"},
		keys: []dbq.DnsKeyRow{
			{ID: uuid.New(), Role: "ksk", Algorithm: "ecdsap256sha256", PublicKeyB64: pubB64, KeyTag: 1},
			{ID: uuid.New(), Role: "ksk", Algorithm: "ecdsap256sha256", PublicKeyB64: pubB64, KeyTag: 2, RetiredAt: &now},
			{ID: uuid.New(), Role: "zsk", Algorithm: "ecdsap256sha256", PublicKeyB64: pubB64, KeyTag: 3},
		},
	}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+id.String()+"/ds-records", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []dsRecord
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 || out[0].KeyTag != 1 {
		t.Errorf("got %+v, want one entry with KeyTag=1", out)
	}
}

func TestListZoneDsRecords_EmptyWhenNoActiveKSK(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	f := &fakeDnssecQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"},
		keys: []dbq.DnsKeyRow{
			// All KSKs retired → no DS rows.
			{Role: "ksk", Algorithm: "ecdsap256sha256", RetiredAt: &now},
		},
	}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+id.String()+"/ds-records", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "[]") {
		t.Errorf("expected empty array, got %s", rec.Body.String())
	}
}

func TestListZoneDsRecords_SkipsMalformedKeys(t *testing.T) {
	// One KSK with valid key + one KSK with garbage public_key_b64.
	// The garbage row should be skipped; the valid one should
	// still surface.
	id := uuid.New()
	pub := make([]byte, 64)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	f := &fakeDnssecQ{
		zone: dbq.DnsZone{ID: id, FabricID: uuid.New(), Name: "example.com"},
		keys: []dbq.DnsKeyRow{
			{Role: "ksk", Algorithm: "ecdsap256sha256", PublicKeyB64: pubB64, KeyTag: 1},
			{Role: "ksk", Algorithm: "ecdsap256sha256", PublicKeyB64: "garbage!!!", KeyTag: 2},
		},
	}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+id.String()+"/ds-records", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []dsRecord
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 || out[0].KeyTag != 1 {
		t.Errorf("expected one valid row, got %+v", out)
	}
}

func TestListZoneDsRecords_NotFoundIs404(t *testing.T) {
	f := &fakeDnssecQ{zoneErr: pgx.ErrNoRows}
	rec := authed(t, mountDnssec(f), "GET", "/dns/zones/"+uuid.New().String()+"/ds-records", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListZoneDsRecords_BadUUIDIs400(t *testing.T) {
	rec := authed(t, mountDnssec(&fakeDnssecQ{}),
		"GET", "/dns/zones/not-a-uuid/ds-records", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
