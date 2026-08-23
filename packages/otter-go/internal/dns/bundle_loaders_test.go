package dns

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ===== loadCatalogForBundle =====

type catalogFakeQ struct {
	cat        dbq.DnsCatalogZone
	catErr     error
	servers    []dbq.ListEnabledAuthDnsServersByFabricRow
	serversErr error
}

func (f *catalogFakeQ) GetEnabledDnsCatalogZoneByFabric(_ context.Context, _ uuid.UUID) (dbq.DnsCatalogZone, error) {
	return f.cat, f.catErr
}
func (f *catalogFakeQ) ListEnabledAuthDnsServersByFabric(_ context.Context, _ uuid.UUID) ([]dbq.ListEnabledAuthDnsServersByFabricRow, error) {
	return f.servers, f.serversErr
}

func TestLoadCatalogForBundle_NoCatalog(t *testing.T) {
	q := &catalogFakeQ{catErr: pgx.ErrNoRows}
	name, members, primaries, err := loadCatalogForBundle(context.Background(), q, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "" || members != nil || primaries != nil {
		t.Errorf("expected empty result; got name=%q members=%v primaries=%v", name, members, primaries)
	}
}

func TestLoadCatalogForBundle_ServerCIDRStripped(t *testing.T) {
	q := &catalogFakeQ{
		cat: dbq.DnsCatalogZone{Name: "catalog.example."},
		servers: []dbq.ListEnabledAuthDnsServersByFabricRow{
			{UnicastIP: "10.0.0.1/32"},
		},
	}
	name, _, primaries, err := loadCatalogForBundle(context.Background(), q, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if name != "catalog.example." {
		t.Errorf("name: got %q", name)
	}
	if len(primaries) != 1 || primaries[0] != "10.0.0.1" {
		t.Errorf("CIDR not stripped from unicast_ip; got %v", primaries)
	}
}

func TestLoadCatalogForBundle_NoServersEmptyPrimaries(t *testing.T) {
	q := &catalogFakeQ{cat: dbq.DnsCatalogZone{Name: "c.example."}}
	_, _, primaries, err := loadCatalogForBundle(context.Background(), q, uuid.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if primaries != nil {
		t.Errorf("expected nil primaries when no auth servers; got %v", primaries)
	}
}

// ===== loadDnssecArtifacts =====

type dnssecFakeQ struct {
	keys    []dbq.DnsKey
	keysErr error
}

func (f *dnssecFakeQ) ListDnsKeysByZoneIDs(_ context.Context, _ []uuid.UUID) ([]dbq.DnsKey, error) {
	return f.keys, f.keysErr
}

func generateEd25519PEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestLoadDnssecArtifacts_NoSignedZones(t *testing.T) {
	zones := []dbq.DnsZone{
		{ID: uuid.New(), Name: "u.example.", Signed: false},
	}
	out, err := loadDnssecArtifacts(context.Background(), &dnssecFakeQ{}, zones, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.KeyFiles) != 0 || len(out.DnssecKeysByZone) != 0 || len(out.Nsec3ParamsByZone) != 0 {
		t.Errorf("expected empty artifacts; got %+v", out)
	}
}

func TestLoadDnssecArtifacts_SignedZoneEmitsKeyFiles(t *testing.T) {
	zoneID := uuid.New()
	pem := generateEd25519PEM(t)
	zones := []dbq.DnsZone{{ID: zoneID, Name: "z.example.", Signed: true}}
	keys := []dbq.DnsKey{
		{ZoneID: &zoneID, Role: "ksk", Algorithm: "ed25519", KeyTag: 12345, PublicKeyB64: "AAAA", PrivatePem: pem},
	}
	out, err := loadDnssecArtifacts(context.Background(), &dnssecFakeQ{keys: keys}, zones, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 2 files per key (.key + .private).
	if len(out.KeyFiles) != 2 {
		t.Errorf("expected 2 key files; got %d (%v)", len(out.KeyFiles), out.KeyFiles)
	}
	// Basename appears in DnssecKeysByZone (stripped of .key suffix).
	if got := out.DnssecKeysByZone["z.example."]; len(got) != 1 {
		t.Errorf("expected 1 basename; got %v", got)
	}
}

func TestLoadDnssecArtifacts_NSEC3PopulatedFromZoneColumns(t *testing.T) {
	zoneID := uuid.New()
	salt := "abcd"
	pem := generateEd25519PEM(t)
	zones := []dbq.DnsZone{{
		ID: zoneID, Name: "z.example.", Signed: true,
		Nsec3Salt: &salt, Nsec3Iterations: 5, Nsec3OptOut: true,
	}}
	keys := []dbq.DnsKey{
		{ZoneID: &zoneID, Role: "ksk", Algorithm: "ed25519", KeyTag: 1, PublicKeyB64: "AAAA", PrivatePem: pem},
	}
	out, _ := loadDnssecArtifacts(context.Background(), &dnssecFakeQ{keys: keys}, zones, nil)
	got, ok := out.Nsec3ParamsByZone["z.example."]
	if !ok {
		t.Fatal("Nsec3ParamsByZone missing entry")
	}
	if got.Salt != "abcd" || got.Iterations != 5 || !got.OptOut {
		t.Errorf("nsec3 params wrong: %+v", got)
	}
}

func TestLoadDnssecArtifacts_DecryptHookCalled(t *testing.T) {
	zoneID := uuid.New()
	pem := generateEd25519PEM(t)
	zones := []dbq.DnsZone{{ID: zoneID, Name: "z.example.", Signed: true}}
	keys := []dbq.DnsKey{
		{ZoneID: &zoneID, Role: "ksk", Algorithm: "ed25519", KeyTag: 1, PublicKeyB64: "AAAA", PrivatePem: "enc:" + pem},
	}
	decryptCalled := false
	decrypt := func(s string) string {
		decryptCalled = true
		return strings.TrimPrefix(s, "enc:")
	}
	_, err := loadDnssecArtifacts(context.Background(), &dnssecFakeQ{keys: keys}, zones, decrypt)
	if err != nil {
		t.Fatal(err)
	}
	if !decryptCalled {
		t.Error("decryptPEM hook not invoked")
	}
}

// ===== loadZoneExtraLines =====

func TestLoadZoneExtraLines_NoSignedZones(t *testing.T) {
	zones := []dbq.DnsZone{{Name: "u.example."}}
	out, err := loadZoneExtraLines(context.Background(), &dnssecFakeQ{}, zones)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("expected no extras; got %v", out)
	}
}

func TestLoadZoneExtraLines_CdsForSignedPublishCds(t *testing.T) {
	zoneID := uuid.New()
	zones := []dbq.DnsZone{
		{ID: zoneID, Name: "z.example.", Signed: true, PublishCds: true},
	}
	keys := []dbq.DnsKey{
		{ZoneID: &zoneID, Role: "ksk", Algorithm: "ed25519", KeyTag: 1, PublicKeyB64: "AAAA"},
	}
	out, _ := loadZoneExtraLines(context.Background(), &dnssecFakeQ{keys: keys}, zones)
	got := out[zoneID]
	if len(got) != 2 {
		t.Fatalf("expected 2 lines (CDNSKEY + CDS); got %v", got)
	}
	if !strings.Contains(got[0], "CDNSKEY") || !strings.Contains(got[1], "CDS") {
		t.Errorf("CDNSKEY/CDS shape wrong: %v", got)
	}
}

func TestLoadZoneExtraLines_SkipsWhenPublishCdsFalse(t *testing.T) {
	zoneID := uuid.New()
	zones := []dbq.DnsZone{
		{ID: zoneID, Name: "z.example.", Signed: true, PublishCds: false},
	}
	keys := []dbq.DnsKey{
		{ZoneID: &zoneID, Role: "ksk", Algorithm: "ed25519", KeyTag: 1, PublicKeyB64: "AAAA"},
	}
	out, _ := loadZoneExtraLines(context.Background(), &dnssecFakeQ{keys: keys}, zones)
	if len(out) != 0 {
		t.Errorf("PublishCds=false should suppress extras; got %v", out)
	}
}

func TestLoadZoneExtraLines_ChildDSAppendedToParent(t *testing.T) {
	parentID := uuid.New()
	childID := uuid.New()
	now := time.Unix(1700000000, 0).UTC()
	zones := []dbq.DnsZone{
		{ID: parentID, Name: "example.", Signed: false, UpdatedAt: now},
		{ID: childID, Name: "site.example.", Signed: true, PublishCds: false, UpdatedAt: now},
	}
	keys := []dbq.DnsKey{
		{ZoneID: &childID, Role: "ksk", Algorithm: "ed25519", KeyTag: 1, PublicKeyB64: "AAAA"},
	}
	out, _ := loadZoneExtraLines(context.Background(), &dnssecFakeQ{keys: keys}, zones)
	got := out[parentID]
	if len(got) != 1 {
		t.Fatalf("expected 1 DS RR in parent extras; got %v", got)
	}
	if !strings.Contains(got[0], "IN DS") {
		t.Errorf("DS RR shape wrong: %q", got[0])
	}
}

func TestLoadZoneExtraLines_NoChildIfParentMissing(t *testing.T) {
	childID := uuid.New()
	// No parent zone in the slice — child has no in-set parent.
	zones := []dbq.DnsZone{
		{ID: childID, Name: "site.example.", Signed: true},
	}
	keys := []dbq.DnsKey{
		{ZoneID: &childID, Role: "ksk", Algorithm: "ed25519", KeyTag: 1, PublicKeyB64: "AAAA"},
	}
	out, _ := loadZoneExtraLines(context.Background(), &dnssecFakeQ{keys: keys}, zones)
	if len(out) != 0 {
		t.Errorf("orphan child should not produce extras; got %v", out)
	}
}

// ===== findDirectParent =====

func TestFindDirectParent_SingleLabelDifference(t *testing.T) {
	parent := dbq.DnsZone{ID: uuid.New(), Name: "example.com."}
	child := dbq.DnsZone{ID: uuid.New(), Name: "site.example.com.", Signed: true}
	byName := map[string]dbq.DnsZone{normalizeZoneName(parent.Name): parent}
	got, ok := findDirectParent(child, byName)
	if !ok || got.ID != parent.ID {
		t.Errorf("expected direct parent match; got ok=%v parent.ID=%v", ok, got.ID)
	}
}

func TestFindDirectParent_MultiLabelGapRejected(t *testing.T) {
	parent := dbq.DnsZone{ID: uuid.New(), Name: "example.com."}
	child := dbq.DnsZone{ID: uuid.New(), Name: "a.b.example.com.", Signed: true}
	byName := map[string]dbq.DnsZone{normalizeZoneName(parent.Name): parent}
	_, ok := findDirectParent(child, byName)
	if ok {
		t.Error("multi-label nesting should be rejected (intermediate b.example.com missing)")
	}
}

func TestFindDirectParent_UnsignedChildRejected(t *testing.T) {
	parent := dbq.DnsZone{ID: uuid.New(), Name: "example.com."}
	child := dbq.DnsZone{ID: uuid.New(), Name: "site.example.com.", Signed: false}
	byName := map[string]dbq.DnsZone{normalizeZoneName(parent.Name): parent}
	_, ok := findDirectParent(child, byName)
	if ok {
		t.Error("unsigned child has nothing to delegate; must be rejected")
	}
}
