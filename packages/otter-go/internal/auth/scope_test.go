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
	orgID    *uuid.UUID
	err      error
}

func (f *fakeSiteQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return f.regionID, f.err
}
func (f *fakeSiteQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return f.orgID, f.err
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

func TestSiteMatches_ViaOrganization(t *testing.T) {
	// PR 90 — principal scoped on an organizations.id UUID should
	// match every site whose sites.organization_id FK matches.
	org := uuid.New()
	s := Scope{OrganizationIDs: singletonUUID(org)}
	q := &fakeSiteQ{orgID: &org}
	ok, _ := s.SiteMatches(context.Background(), q, uuid.New())
	if !ok {
		t.Error("site under matching organization should pass")
	}
}

func TestSiteMatches_OrganizationNullOnSite(t *testing.T) {
	// Sites not yet mapped to an organization (PR 66 backfill leaves
	// unmatched rows with NULL organization_id) don't match an
	// organization-scoped principal — the dimension is "I belong to
	// org X," not "I have no org."
	org := uuid.New()
	s := Scope{OrganizationIDs: singletonUUID(org)}
	q := &fakeSiteQ{orgID: nil}
	ok, _ := s.SiteMatches(context.Background(), q, uuid.New())
	if ok {
		t.Error("site with NULL organization_id should not match org-scoped principal")
	}
}

func TestOrganizationMatches_DirectAndGlobal(t *testing.T) {
	target := uuid.New()
	// Direct match.
	s := Scope{OrganizationIDs: singletonUUID(target)}
	if !s.OrganizationMatches(target) {
		t.Error("direct match should pass")
	}
	if s.OrganizationMatches(uuid.New()) {
		t.Error("non-match should fail")
	}
	// Global bypasses dimension set entirely.
	g := GlobalScope()
	if !g.OrganizationMatches(target) {
		t.Error("global should match any org id")
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
	rows := []dbq.GetUserScopedCapabilitiesRow{
		{Code: "inventory:sites:read", ScopeType: "region", TargetID: ptr(r1.String())},
		{Code: "inventory:sites:read", ScopeType: "region", TargetID: ptr(r2.String())},
		{Code: "dns:zones:read", ScopeType: "", TargetID: nil}, // unrestricted
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
	rows := []dbq.GetUserScopedCapabilitiesRow{
		{Code: "x", ScopeType: "zone-not-a-thing", TargetID: ptr("anything")},
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

// ---- wildcard-evasion fix ----

// TestFindScope_WildcardKeyedScopeApplies is the regression for the
// "wildcard cap, scope keyed on the wildcard" evasion. Pre-fix:
// FindScope("audit:events:read") missed the exact code key, missed
// the bare `*` key, and fell through to GlobalScope — silently
// dropping the SiteA binding and serving fleet-wide audit data.
// Post-fix: the `audit:*:*` pattern matches the `audit:events:read`
// target via segmented wildcard rules, and the scope it carries is
// applied. Cap codes are 3-segment (module:resource:action) so a
// genuinely-wildcarded audit cap is `audit:*:*`, not `audit:*`.
func TestFindScope_WildcardKeyedScopeApplies(t *testing.T) {
	siteA := uuid.New()
	p := Principal{
		Capabilities: []string{"audit:*:*"},
		Scopes: map[string]Scope{
			"audit:*:*": {SiteIDs: singletonUUID(siteA)},
		},
	}
	s := FindScope(p, "audit:events:read")
	if s == nil {
		t.Fatal("FindScope returned nil for held cap")
	}
	if s.IsGlobal {
		t.Error("expected scope-restricted, got GlobalScope (wildcard-evasion regression)")
	}
	if _, ok := s.SiteIDs[siteA]; !ok {
		t.Errorf("expected SiteIDs to contain siteA from the audit:*:* binding, got %+v", s.SiteIDs)
	}
}

// TestFindScope_ExactWinsOverWildcard verifies precedence: when a
// principal has BOTH an exact-code scope binding and a wildcard
// binding that would also match, the exact one wins (more-specific
// scope row).
func TestFindScope_ExactWinsOverWildcard(t *testing.T) {
	exactSite, wildcardSite := uuid.New(), uuid.New()
	p := Principal{
		Capabilities: []string{"audit:events:read", "audit:*:*"},
		Scopes: map[string]Scope{
			"audit:events:read": {SiteIDs: singletonUUID(exactSite)},
			"audit:*:*":         {SiteIDs: singletonUUID(wildcardSite)},
		},
	}
	s := FindScope(p, "audit:events:read")
	if _, ok := s.SiteIDs[exactSite]; !ok {
		t.Errorf("expected exact scope binding to win, got %+v", s.SiteIDs)
	}
	if _, ok := s.SiteIDs[wildcardSite]; ok {
		t.Errorf("wildcard scope should not be unioned when exact match exists, got %+v", s.SiteIDs)
	}
}

// TestFindScope_MultipleMatchingPatternsUnion verifies that when two
// patterns match the same code (e.g. audit:*:* and audit:events:*
// both matching audit:events:read) and neither is the exact key, the
// resulting scope is the union — the caller is granted the broadest
// reach any pattern row authorizes.
func TestFindScope_MultipleMatchingPatternsUnion(t *testing.T) {
	siteA, siteB := uuid.New(), uuid.New()
	p := Principal{
		Capabilities: []string{"audit:*:*"},
		Scopes: map[string]Scope{
			"audit:*:*":      {SiteIDs: singletonUUID(siteA)},
			"audit:events:*": {SiteIDs: singletonUUID(siteB)},
		},
	}
	s := FindScope(p, "audit:events:read")
	if _, ok := s.SiteIDs[siteA]; !ok {
		t.Errorf("expected siteA from audit:*:* binding, got %+v", s.SiteIDs)
	}
	if _, ok := s.SiteIDs[siteB]; !ok {
		t.Errorf("expected siteB from audit:events:* binding, got %+v", s.SiteIDs)
	}
}

// TestFindScope_BareGlobalKeyShortCircuits keeps the legacy precedence:
// when p.Scopes carries a bare "*" key, it short-circuits before any
// segmented walk. Mirrors Python's find_matching_capability.
func TestFindScope_BareGlobalKeyShortCircuits(t *testing.T) {
	siteOther := uuid.New()
	p := Principal{
		Capabilities: []string{"*"},
		Scopes: map[string]Scope{
			"*":         {SiteIDs: singletonUUID(siteOther)},
			"audit:*:*": GlobalScope(), // would otherwise also match
		},
	}
	s := FindScope(p, "audit:events:read")
	if _, ok := s.SiteIDs[siteOther]; !ok {
		t.Errorf("bare * key should win, got %+v", s)
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

// ---- PR 62: ScopedSiteFilter ----

// expansionFake captures the params passed to ListSiteIDsForExpansion
// and returns a configurable result set.
type expansionFake struct {
	called  bool
	gotArg  dbq.ListSiteIDsForExpansionParams
	returns []uuid.UUID
	err     error
}

func (e *expansionFake) ListSiteIDsForExpansion(_ context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	e.called = true
	e.gotArg = arg
	return e.returns, e.err
}

func TestScopedSiteFilter_Global_NoCall(t *testing.T) {
	p := Principal{Capabilities: []string{"inventory:sites:read"}}
	e := &expansionFake{}
	ids, scoped, err := ScopedSiteFilter(context.Background(), e, p, "inventory:sites:read")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scoped {
		t.Errorf("global principal should not be scoped, got scoped=true")
	}
	if ids != nil {
		t.Errorf("global principal should return nil ids, got %v", ids)
	}
	if e.called {
		t.Errorf("expansion query should not have been called for global principal")
	}
}

func TestScopedSiteFilter_SiteOnly(t *testing.T) {
	siteA := uuid.New()
	p := Principal{
		Capabilities: []string{"inventory:sites:read"},
		Scopes: map[string]Scope{
			"inventory:sites:read": {SiteIDs: singletonUUID(siteA)},
		},
	}
	e := &expansionFake{returns: []uuid.UUID{siteA}}
	ids, scoped, err := ScopedSiteFilter(context.Background(), e, p, "inventory:sites:read")
	if err != nil || !scoped {
		t.Fatalf("expected scoped=true err=nil, got %v / %v", scoped, err)
	}
	if len(ids) != 1 || ids[0] != siteA {
		t.Errorf("want [%s], got %v", siteA, ids)
	}
	if !e.called {
		t.Errorf("expansion query should have been called")
	}
	if len(e.gotArg.DirectSiteIds) != 1 || e.gotArg.DirectSiteIds[0] != siteA {
		t.Errorf("DirectSiteIds: want [%s], got %v", siteA, e.gotArg.DirectSiteIds)
	}
	if e.gotArg.RegionIds != nil || e.gotArg.GroupIds != nil {
		t.Errorf("non-site dims should be nil, got regions=%v groups=%v",
			e.gotArg.RegionIds, e.gotArg.GroupIds)
	}
}

func TestScopedSiteFilter_NonSiteDim_EmptySet(t *testing.T) {
	// Principal is scoped on a dimension that can't reach sites
	// (fabric-only). Helper returns an empty allowed set so the
	// caller short-circuits without hitting the DB.
	p := Principal{
		Capabilities: []string{"inventory:sites:read"},
		Scopes: map[string]Scope{
			"inventory:sites:read": {FabricIDs: singletonUUID(uuid.New())},
		},
	}
	e := &expansionFake{}
	ids, scoped, err := ScopedSiteFilter(context.Background(), e, p, "inventory:sites:read")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !scoped {
		t.Errorf("non-global principal should be scoped, got scoped=false")
	}
	if len(ids) != 0 {
		t.Errorf("fabric-only principal should expand to empty set, got %v", ids)
	}
	if e.called {
		t.Errorf("expansion query should NOT be called when no site dim present")
	}
}

func TestScopedSiteFilter_RegionAndGroupCombined(t *testing.T) {
	region := uuid.New()
	group := uuid.New()
	p := Principal{
		Capabilities: []string{"inventory:sites:read"},
		Scopes: map[string]Scope{
			"inventory:sites:read": {
				RegionIDs:    singletonUUID(region),
				SiteGroupIDs: singletonUUID(group),
			},
		},
	}
	expanded := []uuid.UUID{uuid.New(), uuid.New()}
	e := &expansionFake{returns: expanded}
	ids, scoped, err := ScopedSiteFilter(context.Background(), e, p, "inventory:sites:read")
	if err != nil || !scoped {
		t.Fatalf("expected scoped=true err=nil, got %v / %v", scoped, err)
	}
	if len(ids) != 2 {
		t.Errorf("want 2 expanded sites, got %v", ids)
	}
	if len(e.gotArg.RegionIds) != 1 || e.gotArg.RegionIds[0] != region {
		t.Errorf("RegionIds: want [%s], got %v", region, e.gotArg.RegionIds)
	}
	if len(e.gotArg.GroupIds) != 1 || e.gotArg.GroupIds[0] != group {
		t.Errorf("GroupIds: want [%s], got %v", group, e.gotArg.GroupIds)
	}
	if e.gotArg.DirectSiteIds != nil {
		t.Errorf("DirectSiteIds should be nil, got %v", e.gotArg.DirectSiteIds)
	}
}

func TestResolveUserScopes_ClassificationDim(t *testing.T) {
	// PR 61 — the resolver now recognizes 'classification' as a scope
	// dimension. Verify a role_scopes row with scope_type='classification'
	// flows into Principal.Scopes[cap].Classifications.
	st := "classification"
	target := "unclassified"
	rows := []dbq.GetUserScopedCapabilitiesRow{
		{Code: "inventory:sites:create", ScopeType: st, TargetID: &target},
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

// ---- per-request memoization (PR-177 follow-up) ----

// scopeCountingExpander wraps a returned slice with a call counter
// so memoization tests can assert ListSiteIDsForExpansion is invoked
// at most once per (ctx, capCode).
type scopeCountingExpander struct {
	ids   []uuid.UUID
	calls int
}

func (s *scopeCountingExpander) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	s.calls++
	return s.ids, nil
}

func TestScopedSiteFilter_MemoizesPerRequest(t *testing.T) {
	siteA := uuid.New()
	p := Principal{
		Capabilities: []string{"audit:events:read"},
		Scopes: map[string]Scope{
			"audit:events:read": {SiteIDs: singletonUUID(siteA)},
		},
	}
	e := &scopeCountingExpander{ids: []uuid.UUID{siteA}}
	ctx := WithScopeFilterCache(context.Background())

	// First resolve runs the expansion.
	ids1, scoped1, err1 := ScopedSiteFilter(ctx, e, p, "audit:events:read")
	if err1 != nil || !scoped1 || len(ids1) != 1 || ids1[0] != siteA {
		t.Fatalf("first call: ids=%v scoped=%v err=%v", ids1, scoped1, err1)
	}
	if e.calls != 1 {
		t.Errorf("first call should invoke ListSiteIDsForExpansion once, got %d", e.calls)
	}

	// Second resolve on the same context reuses the cache.
	ids2, scoped2, err2 := ScopedSiteFilter(ctx, e, p, "audit:events:read")
	if err2 != nil || !scoped2 || len(ids2) != 1 || ids2[0] != siteA {
		t.Fatalf("second call: ids=%v scoped=%v err=%v", ids2, scoped2, err2)
	}
	if e.calls != 1 {
		t.Errorf("second call should hit cache, got expansion call count %d", e.calls)
	}
}

func TestScopedSiteFilter_GlobalCallerCached(t *testing.T) {
	// Global callers also memoize — the cache stores Scoped=false,
	// IDs=nil so the second call short-circuits at the cache layer
	// instead of re-running FindScope.
	p := Principal{Capabilities: []string{"*"}}
	e := &scopeCountingExpander{}
	ctx := WithScopeFilterCache(context.Background())

	for i := 0; i < 3; i++ {
		ids, scoped, err := ScopedSiteFilter(ctx, e, p, "audit:events:read")
		if err != nil || scoped || ids != nil {
			t.Fatalf("iter %d: expected (nil,false,nil), got (%v,%v,%v)", i, ids, scoped, err)
		}
	}
	if e.calls != 0 {
		t.Errorf("global caller should never hit ListSiteIDsForExpansion, got %d", e.calls)
	}
}

func TestScopedSiteFilter_NoCacheStillWorks(t *testing.T) {
	// Backward compatibility: callers that don't run through Verifying
	// (e.g. direct test invocations) still get correct results, just
	// without the memoization.
	siteA := uuid.New()
	p := Principal{
		Capabilities: []string{"audit:events:read"},
		Scopes: map[string]Scope{
			"audit:events:read": {SiteIDs: singletonUUID(siteA)},
		},
	}
	e := &scopeCountingExpander{ids: []uuid.UUID{siteA}}
	ctx := context.Background() // no cache attached

	ids1, _, _ := ScopedSiteFilter(ctx, e, p, "audit:events:read")
	ids2, _, _ := ScopedSiteFilter(ctx, e, p, "audit:events:read")
	if e.calls != 2 {
		t.Errorf("without cache, expected 2 expansion calls, got %d", e.calls)
	}
	if len(ids1) != 1 || len(ids2) != 1 {
		t.Errorf("result shape: ids1=%v ids2=%v", ids1, ids2)
	}
}
