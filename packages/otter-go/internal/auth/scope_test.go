package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func ptr(s string) *string { return &s }

func TestScopeUnion(t *testing.T) {
	r := uuid.New()
	s := uuid.New()
	a := Scope{RegionIDs: singletonUUID(r)}
	b := Scope{SiteIDs: singletonUUID(s)}
	out := a.Union(b)
	if _, ok := out.RegionIDs[r]; !ok {
		t.Error("union should contain region")
	}
	if _, ok := out.SiteIDs[s]; !ok {
		t.Error("union should contain site")
	}
	if out.IsGlobal {
		t.Error("union of two non-global should not be global")
	}
	if !a.Union(GlobalScope()).IsGlobal {
		t.Error("union with global → global")
	}
}

func TestFabricMatches(t *testing.T) {
	f := uuid.New()
	s := Scope{FabricIDs: singletonUUID(f)}
	if !s.FabricMatches(f) {
		t.Error("matching fabric should pass")
	}
	if s.FabricMatches(uuid.New()) {
		t.Error("non-matching fabric should fail")
	}
	if !GlobalScope().FabricMatches(uuid.New()) {
		t.Error("global should match anything")
	}
}

type fakeSiteQ struct {
	regionID uuid.UUID
	groups   []uuid.UUID
	err      error
}

func (f *fakeSiteQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.regionID, f.err
}
func (f *fakeSiteQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return f.groups, f.err
}

func TestSiteMatches_DirectSite(t *testing.T) {
	site := uuid.New()
	s := Scope{SiteIDs: singletonUUID(site)}
	ok, _ := s.SiteMatches(context.Background(), &fakeSiteQ{}, site)
	if !ok {
		t.Error("direct site match")
	}
}

func TestSiteMatches_ViaRegion(t *testing.T) {
	region := uuid.New()
	s := Scope{RegionIDs: singletonUUID(region)}
	q := &fakeSiteQ{regionID: region}
	ok, _ := s.SiteMatches(context.Background(), q, uuid.New())
	if !ok {
		t.Error("site under matching region should pass")
	}
}

func TestSiteMatches_ViaGroup(t *testing.T) {
	g1, g2 := uuid.New(), uuid.New()
	s := Scope{SiteGroupIDs: singletonUUID(g1)}
	q := &fakeSiteQ{groups: []uuid.UUID{g2, g1}}
	ok, _ := s.SiteMatches(context.Background(), q, uuid.New())
	if !ok {
		t.Error("site in matching group should pass")
	}
}

func TestSiteMatches_NoMatch(t *testing.T) {
	s := Scope{SiteIDs: singletonUUID(uuid.New())}
	q := &fakeSiteQ{regionID: uuid.New(), groups: []uuid.UUID{uuid.New()}}
	ok, _ := s.SiteMatches(context.Background(), q, uuid.New())
	if ok {
		t.Error("unrelated site should fail")
	}
}

func TestEnforceFabricScope_GlobalAllows(t *testing.T) {
	p := Principal{Capabilities: []string{"ipam:fabrics:read"}}
	if err := EnforceFabricScope(p, uuid.New(), "ipam:fabrics:read"); err != nil {
		t.Errorf("global principal should pass: %v", err)
	}
}

func TestEnforceFabricScope_ScopedRefuses(t *testing.T) {
	allowed := uuid.New()
	p := Principal{
		Capabilities: []string{"ipam:fabrics:read"},
		Scopes:       map[string]Scope{"ipam:fabrics:read": {FabricIDs: singletonUUID(allowed)}},
	}
	if err := EnforceFabricScope(p, uuid.New(), "ipam:fabrics:read"); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("expected ErrOutsideScope, got %v", err)
	}
	if err := EnforceFabricScope(p, allowed, "ipam:fabrics:read"); err != nil {
		t.Errorf("in-scope fabric should pass: %v", err)
	}
}

func TestEnforceFabricScope_NoCap(t *testing.T) {
	p := Principal{Capabilities: []string{"dns:zones:read"}}
	// FindScope returns nil when cap not held → enforce treats it as
	// no-restriction (the require_capability gate catches missing cap
	// at the middleware layer).
	if err := EnforceFabricScope(p, uuid.New(), "ipam:fabrics:read"); err != nil {
		t.Errorf("got %v", err)
	}
}

func TestResolveUserScopes_AggregatesPerCap(t *testing.T) {
	r1 := uuid.New()
	r2 := uuid.New()
	rows := []dbq.ScopedCapabilityRow{
		{Code: "inventory:sites:read", ScopeType: ptr("region"), TargetID: ptr(r1.String())},
		{Code: "inventory:sites:read", ScopeType: ptr("region"), TargetID: ptr(r2.String())},
		{Code: "dns:zones:read", ScopeType: nil, TargetID: nil}, // unrestricted
	}
	out := resolveUserScopes(rows)
	sites := out["inventory:sites:read"]
	if len(sites.RegionIDs) != 2 {
		t.Errorf("expected 2 region ids, got %d", len(sites.RegionIDs))
	}
	dns := out["dns:zones:read"]
	if !dns.IsGlobal {
		t.Error("unrestricted row should produce IsGlobal=true")
	}
}

func TestResolveUserScopes_UnknownDimensionFailsClosed(t *testing.T) {
	rows := []dbq.ScopedCapabilityRow{
		{Code: "x", ScopeType: ptr("zone-not-a-thing"), TargetID: ptr("anything")},
	}
	out := resolveUserScopes(rows)
	s := out["x"]
	// Unknown dimension → matches nothing. Not global, all sets empty.
	if s.IsGlobal {
		t.Error("unknown dimension should not be global")
	}
	if len(s.RegionIDs)+len(s.SiteIDs)+len(s.FabricIDs)+len(s.SiteGroupIDs) != 0 {
		t.Error("unknown dimension should not populate any set")
	}
}

func TestFindScope_WildcardFallsBackToGlobal(t *testing.T) {
	p := Principal{Capabilities: []string{"*"}}
	s := FindScope(p, "ipam:fabrics:read")
	if s == nil || !s.IsGlobal {
		t.Errorf("wildcard cap with no scope info should default to global, got %+v", s)
	}
}

// ---- PR 61: enclave + classification ----

func TestEnclaveMatches(t *testing.T) {
	s := Scope{Enclaves: singletonStr("siprnet")}
	if !s.EnclaveMatches("siprnet") {
		t.Error("siprnet should match")
	}
	if s.EnclaveMatches("niprnet") {
		t.Error("niprnet should not match")
	}
	global := GlobalScope()
	if !global.EnclaveMatches("anything") {
		t.Error("global should match any enclave")
	}
}

func TestClassificationMatches(t *testing.T) {
	s := Scope{Classifications: singletonStr("unclassified")}
	if !s.ClassificationMatches("unclassified") {
		t.Error("unclassified should match")
	}
	if s.ClassificationMatches("secret") {
		t.Error("secret should not match")
	}
}

func TestEnforceEnclave_GlobalAllowsAny(t *testing.T) {
	p := Principal{Capabilities: []string{"inventory:sites:create"}}
	tag := "anything"
	if err := EnforceEnclave(p, &tag, "inventory:sites:create"); err != nil {
		t.Errorf("global should pass: %v", err)
	}
	if err := EnforceEnclave(p, nil, "inventory:sites:create"); err != nil {
		t.Errorf("global should pass even with nil enclave: %v", err)
	}
}

func TestEnforceEnclave_ScopedRefuses(t *testing.T) {
	p := Principal{
		Capabilities: []string{"inventory:sites:create"},
		Scopes: map[string]Scope{
			"inventory:sites:create": {Enclaves: singletonStr("niprnet")},
		},
	}
	allowed := "niprnet"
	if err := EnforceEnclave(p, &allowed, "inventory:sites:create"); err != nil {
		t.Errorf("in-scope enclave should pass: %v", err)
	}
	denied := "siprnet"
	if err := EnforceEnclave(p, &denied, "inventory:sites:create"); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("out-of-scope enclave should be refused: %v", err)
	}
}

func TestEnforceEnclave_ScopedRefusesNilEnclave(t *testing.T) {
	// PR 61 semantic: nil/empty enclave gates to global. A scoped
	// principal cannot mutate an un-tagged resource.
	p := Principal{
		Capabilities: []string{"inventory:sites:create"},
		Scopes: map[string]Scope{
			"inventory:sites:create": {Enclaves: singletonStr("niprnet")},
		},
	}
	if err := EnforceEnclave(p, nil, "inventory:sites:create"); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("scoped principal should be refused on nil enclave: %v", err)
	}
	empty := ""
	if err := EnforceEnclave(p, &empty, "inventory:sites:create"); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("scoped principal should be refused on empty enclave: %v", err)
	}
}

func TestEnforceClassification_ScopedRefuses(t *testing.T) {
	p := Principal{
		Capabilities: []string{"ipam:fabrics:create"},
		Scopes: map[string]Scope{
			"ipam:fabrics:create": {Classifications: singletonStr("unclassified")},
		},
	}
	allowed := "unclassified"
	if err := EnforceClassification(p, &allowed, "ipam:fabrics:create"); err != nil {
		t.Errorf("in-scope classification should pass: %v", err)
	}
	denied := "secret"
	if err := EnforceClassification(p, &denied, "ipam:fabrics:create"); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("out-of-scope classification should be refused: %v", err)
	}
	if err := EnforceClassification(p, nil, "ipam:fabrics:create"); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("scoped principal should be refused on nil classification: %v", err)
	}
}

func TestResolveUserScopes_ClassificationDim(t *testing.T) {
	// PR 61 — the resolver now recognizes 'classification' as a scope
	// dimension. Verify a role_scopes row with scope_type='classification'
	// flows into Principal.Scopes[cap].Classifications.
	st := "classification"
	target := "unclassified"
	rows := []dbq.ScopedCapabilityRow{
		{Code: "inventory:sites:create", ScopeType: &st, TargetID: &target},
	}
	out := resolveUserScopes(rows)
	scope, ok := out["inventory:sites:create"]
	if !ok {
		t.Fatal("no scope for cap")
	}
	if _, has := scope.Classifications["unclassified"]; !has {
		t.Errorf("classification dim not populated, got %+v", scope.Classifications)
	}
}
