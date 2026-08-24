// ABAC scope. Port of packages/otter/src/dcim/security/scope.py
// (Scope dataclass, scope_for_user, fabric_matches_scope,
// site_matches_scope, enforce_*, scope_filtered_*).
//
// A Principal's per-capability Scope bounds *where* the cap applies:
// by region, site, site-group, enclave, organization, or fabric. An
// empty Scope means "global" (unbounded); a Scope with at least one
// dimension constrained allows only matching targets. Scopes union
// across multiple role assignments per cap.
//
// PR 53 ships the foundation: Scope struct + resolver + matchers +
// enforcers + filters, plus a wire-up in the Verifying middleware to
// populate Principal.Scopes from user_roles + role_scopes. Per-route
// retrofits: PR 54 (IPAM 1-hop + inventory), PR 55 (IPAM 2-hop:
// subnet/address/vni/vtep/vtep-membership), PR 56 (scope-filtered
// IPAM LIST queries), PR 57 (DNS fabric-rooted mutations), PR 58
// (BGP peers site-scope + anycast bindings fabric-scope), PR 59
// (alert_rules + maintenance_windows site-scope with the nullable-
// site "enterprise default" rule), PR 60 (scope-filtered DNS LIST
// queries — zones, records, servers, anycast-groups, forwarders,
// catalog-zones, blocklists + entries, views, health-checks,
// anycast-bindings), PR 61 (enclave + classification mutation
// enforcement on sites + fabrics with nullable-tag = global semantic),
// PR 62 (scope-filtered site-rooted LIST queries — sites, racks,
// assets — via DB-backed expansion of region + site_group + direct
// site dims), PR 63 (site-scope LISTs for buildings + alerts +
// alert_rules + maintenance_windows + bgp_peers; rules and windows
// preserve enterprise-default visibility by also matching NULL
// site_scope_id / site_id). Remaining: auth-handler retrofit
// (api_tokens, user roles), rooms/rows LIST filtering (2-hop via
// buildings), IdP-mapping scope resolver. BGP policy resources
// (asns/prefix-lists/community-lists/route-maps + entries) are
// intentionally global — they have no scope FK and cannot be
// ABAC-scoped without a schema change.
//
// OIDC-mapping scope resolution (the cross-table code→UUID lookups
// from _resolve_mapping_scope in Python) is intentionally deferred —
// IdP roles fall back to global scope so the behavior is no-worse than
// today's "every cap is global" status quo.
package auth

import (
	"context"
	"strings"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Scope mirrors security.scope.Scope. An empty Scope (zero value
// with IsGlobal=false and all sets empty) matches nothing; IsGlobal
// matches everything. Mixed scopes match any target listed in any
// non-empty dimension set.
type Scope struct {
	IsGlobal     bool
	RegionIDs    map[uuid.UUID]struct{}
	SiteIDs      map[uuid.UUID]struct{}
	SiteGroupIDs map[uuid.UUID]struct{}
	Enclaves     map[string]struct{}
	// Organizations is the legacy free-form string dimension keyed on
	// sites.organization. PR 69 introduces OrganizationIDs (UUID set
	// keyed on the new sites.organization_id FK). Both are recorded;
	// neither is enforced in otter-go today (the consumer lives in
	// Python's site_matches_scope) — kept here for structural parity
	// so the Go service is ready to enforce when the FK pivot lands.
	Organizations   map[string]struct{}
	OrganizationIDs map[uuid.UUID]struct{}
	Classifications map[string]struct{}
	FabricIDs       map[uuid.UUID]struct{}
}

// GlobalScope is the unrestricted constructor — every matcher returns
// true. Used as the resolver's fallback when an assignment has no
// RoleScope rows attached.
func GlobalScope() Scope {
	return Scope{IsGlobal: true}
}

// Union returns a Scope that matches anything either scope matched.
// Mirrors Python Scope.union.
func (s Scope) Union(other Scope) Scope {
	out := Scope{
		IsGlobal:        s.IsGlobal || other.IsGlobal,
		RegionIDs:       mergeUUIDSet(s.RegionIDs, other.RegionIDs),
		SiteIDs:         mergeUUIDSet(s.SiteIDs, other.SiteIDs),
		SiteGroupIDs:    mergeUUIDSet(s.SiteGroupIDs, other.SiteGroupIDs),
		Enclaves:        mergeStrSet(s.Enclaves, other.Enclaves),
		Organizations:   mergeStrSet(s.Organizations, other.Organizations),
		OrganizationIDs: mergeUUIDSet(s.OrganizationIDs, other.OrganizationIDs),
		Classifications: mergeStrSet(s.Classifications, other.Classifications),
		FabricIDs:       mergeUUIDSet(s.FabricIDs, other.FabricIDs),
	}
	return out
}

// FabricMatches reports whether fabric_id is reachable under s. Pure:
// no DB query. Fabric is a leaf dimension with no hierarchy expansion
// (unlike Site, which can be reached via region or site-group).
func (s Scope) FabricMatches(fabricID uuid.UUID) bool {
	if s.IsGlobal {
		return true
	}
	_, ok := s.FabricIDs[fabricID]
	return ok
}

// EnclaveMatches reports whether the given enclave is reachable under s.
// Pure string set check.
func (s Scope) EnclaveMatches(enclave string) bool {
	if s.IsGlobal {
		return true
	}
	_, ok := s.Enclaves[enclave]
	return ok
}

// ClassificationMatches reports whether the given classification tag is
// reachable under s. Pure string set check.
func (s Scope) ClassificationMatches(classification string) bool {
	if s.IsGlobal {
		return true
	}
	_, ok := s.Classifications[classification]
	return ok
}

// OrganizationMatches reports whether the given organizations.id UUID
// is reachable under s. Pure set check; the resolver populated
// OrganizationIDs from scope bindings whose target_id parsed as UUID
// (PR 69). The legacy string-keyed Organizations dimension is
// intentionally not consulted here — string bindings predate the FK
// pivot and apply only to sites.organization, which the site filter
// no longer keys on (PR 90).
func (s Scope) OrganizationMatches(orgID uuid.UUID) bool {
	if s.IsGlobal {
		return true
	}
	_, ok := s.OrganizationIDs[orgID]
	return ok
}

// FabricIDsInScope returns the fabric set the caller should constrain
// their list query to. nil = no filter (global scope); empty set =
// nothing in scope; non-nil non-empty = restrict via WHERE
// fabric_id = ANY(set).
func (s Scope) FabricIDsInScope() map[uuid.UUID]struct{} {
	if s.IsGlobal {
		return nil
	}
	return s.FabricIDs
}

// siteExpansionQ is the slim interface ScopedSiteFilter needs: a
// single expansion query that maps the caller's (direct sites, regions,
// groups) dimensions to the concrete site_id set they can see. Each
// site-rooted package's Querier interface satisfies this when it
// embeds ListSiteIDsForExpansion.
type siteExpansionQ interface {
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

// regionExpansionQ widens siteExpansionQ with the region-rollup query.
// ScopedRegionFilter expands the caller's site-reachable scope to a
// region set (union of directly-granted RegionIDs + regions whose sites
// the caller can see).
type regionExpansionQ interface {
	siteExpansionQ
	ListRegionIDsForSiteIDs(ctx context.Context, siteIDs []uuid.UUID) ([]uuid.UUID, error)
}

// scopeFilterCacheKey is the context key under which a request can
// memoize ScopedSiteFilter results. Set by the verify middleware
// (lazy: only allocated when the first ScopedSiteFilter call fires
// during the request); read on every subsequent call so a handler
// that resolves the same (principal, capCode) twice — e.g. /audit
// page-load fanning /audit/log + /audit/actions — only pays one
// expansion DB round-trip.
type scopeFilterCacheKey struct{}

// ScopedFilterResult is the memoized result of one ScopedSiteFilter
// call. IDs may be nil (global), and Scoped tracks the second return.
type ScopedFilterResult struct {
	IDs    []uuid.UUID
	Scoped bool
}

// scopeFilterCache lives on the request context (when present) and
// memoizes per-(capCode) expansions. Single goroutine per request, so
// a plain map is fine.
type scopeFilterCache map[string]ScopedFilterResult

// WithScopeFilterCache attaches a memoization map to ctx. Returns ctx
// unchanged when the cache already exists. Called by Verifying at the
// start of every request so handlers don't have to.
func WithScopeFilterCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(scopeFilterCacheKey{}).(scopeFilterCache); ok {
		return ctx
	}
	return context.WithValue(ctx, scopeFilterCacheKey{}, scopeFilterCache{})
}

// ScopedSiteFilter resolves the caller's scope for capCode into a
// concrete site_id set the handler should pass as scope_site_ids on
// site-rooted LIST/COUNT queries. The expansion walks all three site-
// reachable dimensions (direct SiteIDs + sites under any RegionID +
// sites in any SiteGroupID) via a single SQL query.
//
// Results are memoized on the request context (see
// WithScopeFilterCache) so two handlers in the same request that ask
// for the same capCode share one expansion call.
//
// Returns:
//   - (nil, false, nil)            — principal is global; pass nil to
//     skip the filter on the LIST query.
//   - ([]uuid.UUID{...}, true, nil) — scoped; pass the slice. Empty
//     slice means "scope can't reach any site" — caller should
//     short-circuit to an empty page.
//   - (nil, true, err)             — DB error during expansion.
//
// Region/site-group expansion is DB-backed (mirrors how SiteMatches
// resolves a single target). A scoped principal with only Enclaves /
// Organizations / FabricIDs / Classifications dimensions and no site-
// reachable dim gets back an empty allowed set — those dimensions
// can't expand into a site list.
func ScopedSiteFilter(ctx context.Context, q siteExpansionQ, p Principal, capCode string) ([]uuid.UUID, bool, error) {
	cache, _ := ctx.Value(scopeFilterCacheKey{}).(scopeFilterCache)
	// Per-request memoization (see WithScopeFilterCache). When two
	// handlers in the same request resolve the same (capCode), the
	// second call returns the cached result instead of re-running
	// FindScope + ListSiteIDsForExpansion.
	if cache != nil {
		if cached, ok := cache[capCode]; ok {
			return cached.IDs, cached.Scoped, nil
		}
	}
	ids, scoped, err := computeScopedSiteFilter(ctx, q, p, capCode)
	if err == nil && cache != nil {
		cache[capCode] = ScopedFilterResult{IDs: ids, Scoped: scoped}
	}
	return ids, scoped, err
}

// computeScopedSiteFilter does the actual work of ScopedSiteFilter
// (FindScope + dimension extraction + the expansion DB query). Split
// out so the cache-read/cache-write wrapping above stays a flat 4
// lines and so the cognitive-complexity linter is happy.
func computeScopedSiteFilter(ctx context.Context, q siteExpansionQ, p Principal, capCode string) ([]uuid.UUID, bool, error) {
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil, false, nil
	}
	directs := uuidKeys(s.SiteIDs)
	regions := uuidKeys(s.RegionIDs)
	groups := uuidKeys(s.SiteGroupIDs)
	// PR 90 — organization_id is a site-reachable dimension now. A
	// principal scoped only on an organizations.id UUID sees every
	// site whose sites.organization_id FK matches.
	orgs := uuidKeys(s.OrganizationIDs)
	if directs == nil && regions == nil && groups == nil && orgs == nil {
		// Non-site dimensions (Enclaves, Classifications, FabricIDs)
		// can't expand into a site list; the caller is scoped but
		// reaches zero sites. Short-circuit without a DB call.
		return []uuid.UUID{}, true, nil
	}
	ids, err := q.ListSiteIDsForExpansion(ctx, dbq.ListSiteIDsForExpansionParams{
		DirectSiteIds: directs, RegionIds: regions, GroupIds: groups,
		OrganizationIds: orgs,
	})
	if err != nil {
		return nil, true, err
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids, true, nil
}

// uuidKeys returns nil when the set is empty (so the caller can drop
// the dimension from the SQL params) or a freshly-allocated slice of
// the map's keys otherwise. Order is not specified.
func uuidKeys(m map[uuid.UUID]struct{}) []uuid.UUID {
	if len(m) == 0 {
		return nil
	}
	out := make([]uuid.UUID, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	return out
}

// ScopedRegionFilter resolves the caller's scope for capCode and turns
// it into the (regionIDs, scoped) pair that the regions LIST/COUNT
// handlers pass as their region_ids SQL parameter. Mirrors Python's
// list_regions filter: "show regions directly granted plus regions
// containing at least one in-scope site."
//
// Returns:
//   - (nil, false, nil)            — principal is global; pass nil to
//     skip the filter.
//   - ([]uuid.UUID{...}, true, nil) — scoped; pass the slice. Empty
//     slice means the principal can reach zero regions — caller
//     short-circuits to an empty page.
//   - (nil, true, err)             — DB error during expansion.
//
// Direct RegionIDs grant region visibility on their own. Site-rooted
// dimensions (SiteIDs, SiteGroupIDs, OrganizationIDs) expand to sites
// via ListSiteIDsForExpansion, then to regions via
// ListRegionIDsForSiteIDs. The union is deduplicated.
func ScopedRegionFilter(ctx context.Context, q regionExpansionQ, p Principal, capCode string) ([]uuid.UUID, bool, error) {
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil, false, nil
	}
	out := map[uuid.UUID]struct{}{}
	for id := range s.RegionIDs {
		out[id] = struct{}{}
	}
	// Skip the site-rooted expansion when the principal has no site-
	// reachable dims — otherwise we'd pay ListSiteIDsForExpansion +
	// ListRegionIDsForSiteIDs just to re-derive RegionIDs we already
	// have. Non-site-reachable dims (Enclaves, Classifications,
	// FabricIDs) can't expand into a region set; for those a principal
	// with only RegionIDs gets exactly RegionIDs.
	if len(s.SiteIDs) > 0 || len(s.SiteGroupIDs) > 0 || len(s.OrganizationIDs) > 0 {
		siteIDs, scoped, err := ScopedSiteFilter(ctx, q, p, capCode)
		if err != nil {
			return nil, true, err
		}
		if scoped && len(siteIDs) > 0 {
			regionIDs, err := q.ListRegionIDsForSiteIDs(ctx, siteIDs)
			if err != nil {
				return nil, true, err
			}
			for _, id := range regionIDs {
				out[id] = struct{}{}
			}
		}
	}
	if len(out) == 0 {
		return []uuid.UUID{}, true, nil
	}
	ids := make([]uuid.UUID, 0, len(out))
	for id := range out {
		ids = append(ids, id)
	}
	return ids, true, nil
}

// EnforceRegionScope refuses with ErrOutsideScope if the principal has
// capCode but regionID isn't reachable. Mirrors EnforceSiteScope for
// region-rooted reads.
func EnforceRegionScope(ctx context.Context, q regionExpansionQ, p Principal, regionID uuid.UUID, capCode string) error {
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil
	}
	// Fast path: directly-granted region needs no DB lookup. Avoids
	// paying ScopedSiteFilter + ListRegionIDsForSiteIDs (and the
	// resulting 500-on-DB-error) for the most common scoped-access
	// shape.
	if _, ok := s.RegionIDs[regionID]; ok {
		return nil
	}
	ids, scoped, err := ScopedRegionFilter(ctx, q, p, capCode)
	if err != nil {
		return err
	}
	if !scoped {
		return nil
	}
	for _, id := range ids {
		if id == regionID {
			return nil
		}
	}
	return ErrOutsideScope
}

// ScopedFabricFilter resolves the caller's scope for capCode and turns
// it into the (slice, scoped) pair that LIST/COUNT handlers should pass
// as their scope_fabric_ids SQL parameter:
//
//   - (nil, false)        — principal is global for this cap; pass nil
//     to skip the filter.
//   - (ids, true) with ids — principal is fabric-scoped; pass ids so
//     the query restricts to fabric_id = ANY(ids).
//   - ([]uuid.UUID{}, true) — principal is non-global but has no fabric
//     dimension (e.g. region-only scope). Caller should short-circuit
//     to an empty page without hitting the DB — fabric-rooted resources
//     don't expand from region/site/group scopes.
//
// Region/site/site-group dimensions of Scope are deliberately ignored
// here: fabrics are not site-rooted, so those dimensions can't expand
// into a fabric set. A region-only principal therefore sees nothing
// under any fabric-rooted /ipam/* LIST endpoint, which matches the
// EnforceFabricScope behavior on the same caps.
func ScopedFabricFilter(p Principal, capCode string) (ids []uuid.UUID, scoped bool) {
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil, false
	}
	out := make([]uuid.UUID, 0, len(s.FabricIDs))
	for id := range s.FabricIDs {
		out = append(out, id)
	}
	return out, true
}

// SiteMatches reports whether site_id is reachable under s. Walks the
// region + site-group + organization dimensions via DB lookups when
// needed; for site-scoped principals it's a pure set check.
type siteScopeQ interface {
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
	// PR 90 — organization_id lookup for the org-scope dimension.
	// Pointer return: NULL means the site hasn't been mapped to an
	// organization yet (post-PR-66 backfill leaves unmatched rows
	// with NULL); the matcher reads it as "no org dim match."
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
}

func (s Scope) SiteMatches(ctx context.Context, q siteScopeQ, siteID uuid.UUID) (bool, error) {
	if s.IsGlobal {
		return true, nil
	}
	if _, ok := s.SiteIDs[siteID]; ok {
		return true, nil
	}
	if len(s.RegionIDs) > 0 {
		rid, err := q.GetSiteRegionID(ctx, siteID)
		if err == nil {
			if _, ok := s.RegionIDs[rid]; ok {
				return true, nil
			}
		}
	}
	if len(s.SiteGroupIDs) > 0 {
		groups, err := q.ListSiteGroupIDsForSite(ctx, siteID)
		if err == nil {
			for _, g := range groups {
				if _, ok := s.SiteGroupIDs[g]; ok {
					return true, nil
				}
			}
		}
	}
	// PR 90 — organization scope dim. Skip the DB lookup when the
	// principal has no OrganizationIDs (steady-state for most
	// scoped principals) to keep the hot path cheap.
	if len(s.OrganizationIDs) > 0 {
		orgID, err := q.GetSiteOrganizationID(ctx, siteID)
		if err == nil && orgID != nil {
			if _, ok := s.OrganizationIDs[*orgID]; ok {
				return true, nil
			}
		}
	}
	return false, nil
}

// FindScope returns the scope a principal holds for the matching cap
// code, using the same wildcard rules as HasCapability. Returns nil
// when the cap isn't held at all. A held cap with no scope info comes
// back as GlobalScope.
//
// The resolver wires Principal.Scopes parallel to Capabilities so a
// principal can carry both: HasCapability (existing wildcard check)
// stays the gate; FindScope only needs to be called by handlers that
// also need ABAC enforcement.
func FindScope(p Principal, code string) *Scope {
	if !HasCapability(p.Capabilities, code) {
		return nil
	}
	if p.Scopes == nil {
		g := GlobalScope()
		return &g
	}
	// Lookup order, mirroring Python find_matching_capability:
	//   1. Exact code match wins — most-specific scope binding.
	//   2. Bare global `*` short-circuits.
	//   3. Walk every pattern key and union the scopes of all keys
	//      whose pattern grants `code` under HasCapability's segmented
	//      wildcard rules. This is the fix for the wildcard-evasion
	//      bug: a principal granted `audit:*` whose ONLY scope row is
	//      keyed on `audit:*` (rather than the resolved leaf code) used
	//      to fall through to GlobalScope here, silently dropping the
	//      site binding and serving fleet-wide data. Now the
	//      `audit:*` scope is correctly applied to
	//      FindScope(p, "audit:events:read").
	//   4. If nothing matches at the scope layer but HasCapability
	//      returned true (cap held via some other path), preserve the
	//      legacy permissive default of GlobalScope — matches Python's
	//      "held but unscoped → unrestricted" semantic.
	if s, ok := p.Scopes[code]; ok {
		return &s
	}
	if s, ok := p.Scopes["*"]; ok {
		return &s
	}
	target := strings.Split(code, ":")
	var matched *Scope
	for pattern, scope := range p.Scopes {
		if !patternMatches(pattern, target) {
			continue
		}
		if matched == nil {
			s := scope // copy: don't return loop-var address
			matched = &s
			continue
		}
		u := matched.Union(scope)
		matched = &u
	}
	if matched != nil {
		return matched
	}
	g := GlobalScope()
	return &g
}

// patternMatches reports whether `pattern` grants the cap code whose
// `:`-split is `target`. Same segmented wildcard rules as
// HasCapability: equal segment counts, and each `pattern` segment
// must be `*` or equal to the matching `target` segment.
func patternMatches(pattern string, target []string) bool {
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, ":")
	if len(parts) != len(target) {
		return false
	}
	for i, p := range parts {
		if p != "*" && p != target[i] {
			return false
		}
	}
	return true
}

// EnforceFabricScope refuses with ErrOutsideScope if the principal
// has cap_code but fabric_id is outside their scope. fabricID==nil
// (passing uuid.Nil) is treated as no resource to check — caller
// short-circuits before calling for resources that lack a fabric.
func EnforceFabricScope(p Principal, fabricID uuid.UUID, capCode string) error {
	if fabricID == uuid.Nil {
		return nil
	}
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil
	}
	if !s.FabricMatches(fabricID) {
		return ErrOutsideScope
	}
	return nil
}

// EnforceSiteScope is the site equivalent, DB-backed because site
// scope can be satisfied by region or site-group expansion.
func EnforceSiteScope(ctx context.Context, q siteScopeQ, p Principal, siteID uuid.UUID, capCode string) error {
	if siteID == uuid.Nil {
		return nil
	}
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil
	}
	ok, err := s.SiteMatches(ctx, q, siteID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrOutsideScope
	}
	return nil
}

// EnforceEnclave refuses with ErrOutsideScope if the principal has
// capCode but the target's enclave is outside their scope. PR 61
// semantic for the nil case: when enclave is nil/empty (e.g. an asset
// or site with no enclave tag yet), a scoped principal cannot mutate
// — only global principals can touch un-tagged resources. This
// matches the DoD-context posture: unlabeled resources are treated as
// "classification unknown" and gate-kept to global.
func EnforceEnclave(p Principal, enclave *string, capCode string) error {
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil
	}
	if enclave == nil || *enclave == "" {
		return ErrOutsideScope
	}
	if !s.EnclaveMatches(*enclave) {
		return ErrOutsideScope
	}
	return nil
}

// EnforceClassification is the classification-dimension twin of
// EnforceEnclave. Same nil-handling semantic: unclassified-tagged
// (i.e. classification IS NULL) means "tag missing, gate to global."
// Sites and fabrics carry classification today; assets do not (they
// inherit from their site).
func EnforceClassification(p Principal, classification *string, capCode string) error {
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil
	}
	if classification == nil || *classification == "" {
		return ErrOutsideScope
	}
	if !s.ClassificationMatches(*classification) {
		return ErrOutsideScope
	}
	return nil
}

// ErrOutsideScope is the canonical error the handlers map to 403.
// Mirrors Python's ForbiddenError("resource is outside your scope").
// ErrOutsideScope aliases the canonical sentinel in httpx so
// errors.Is(err, auth.ErrOutsideScope) and errors.Is(err,
// httpx.ErrOutsideScope) both match the same value. httpx is the
// owner because Mapped needs to map it to 403 without importing auth.
var ErrOutsideScope = httpx.ErrOutsideScope

// ---- Resolver ----

// resolveUserScopes builds the cap_code → Scope map for a user by
// walking GetUserScopedCapabilities and folding each (code, scope_type,
// target_id) row into the corresponding dimension set. Assignments
// with no role_scopes rows arrive with NULL scope_type/target_id and
// are recorded as IsGlobal=true.
//
// The resolver is intentionally tolerant: an unknown ScopeType string
// is silently dropped (the OIDC-mapping path may later contribute
// dimensions we don't yet enumerate). Production behavior matches
// Python — unknown == ignored.
func resolveUserScopes(rows []dbq.GetUserScopedCapabilitiesRow) map[string]Scope {
	if len(rows) == 0 {
		return nil
	}
	out := map[string]Scope{}
	for _, r := range rows {
		s := out[r.Code]
		add := scopeRowToDimension(r.ScopeType, r.TargetID)
		out[r.Code] = s.Union(add)
	}
	return out
}

// scopeRowToDimension turns a single (scope_type, target_id) pair
// into the corresponding Scope. Empty/NULL scope_type means the
// assignment is unrestricted — returns GlobalScope.
func scopeRowToDimension(scopeType, targetID *string) Scope {
	if scopeType == nil || *scopeType == "" || *scopeType == "global" {
		return GlobalScope()
	}
	if targetID == nil || *targetID == "" {
		return GlobalScope() // dimension declared but no target → permissive
	}
	switch *scopeType {
	case "region":
		if id, err := uuid.Parse(*targetID); err == nil {
			return Scope{RegionIDs: singletonUUID(id)}
		}
	case "site":
		if id, err := uuid.Parse(*targetID); err == nil {
			return Scope{SiteIDs: singletonUUID(id)}
		}
	case "site_group":
		if id, err := uuid.Parse(*targetID); err == nil {
			return Scope{SiteGroupIDs: singletonUUID(id)}
		}
	case "enclave":
		return Scope{Enclaves: singletonStr(*targetID)}
	case "organization":
		// PR 69 — target parses as UUID → FK binding; else legacy
		// string (matched against sites.organization). Mirrors
		// Python _scope_from_assignment.
		if id, err := uuid.Parse(*targetID); err == nil {
			return Scope{OrganizationIDs: singletonUUID(id)}
		}
		return Scope{Organizations: singletonStr(*targetID)}
	case "classification":
		return Scope{Classifications: singletonStr(*targetID)}
	case "fabric":
		if id, err := uuid.Parse(*targetID); err == nil {
			return Scope{FabricIDs: singletonUUID(id)}
		}
	}
	return Scope{} // unknown dimension → matches nothing (fail-closed)
}

// ---- tiny set helpers ----

func mergeUUIDSet(a, b map[uuid.UUID]struct{}) map[uuid.UUID]struct{} {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[uuid.UUID]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

func mergeStrSet(a, b map[string]struct{}) map[string]struct{} {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

func singletonUUID(id uuid.UUID) map[uuid.UUID]struct{} {
	return map[uuid.UUID]struct{}{id: {}}
}

func singletonStr(s string) map[string]struct{} {
	return map[string]struct{}{s: {}}
}
