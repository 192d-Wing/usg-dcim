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
