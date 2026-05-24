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
// site dims). Remaining: auth-handler retrofit (api_tokens, user
// roles), site-scope LIST filtering on the rest (alert_rules,
// maintenance_windows, bgp_peers, buildings/rooms/rows), IdP-mapping
// scope resolver. BGP policy resources (asns/prefix-lists/community-
// lists/route-maps + entries) are intentionally global — they have
// no scope FK and cannot be ABAC-scoped without a schema change.
//
// OIDC-mapping scope resolution (the cross-table code→UUID lookups
// from _resolve_mapping_scope in Python) is intentionally deferred —
// IdP roles fall back to global scope so the behavior is no-worse than
// today's "every cap is global" status quo.
package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// Scope mirrors security.scope.Scope. An empty Scope (zero value
// with IsGlobal=false and all sets empty) matches nothing; IsGlobal
// matches everything. Mixed scopes match any target listed in any
// non-empty dimension set.
type Scope struct {
	IsGlobal        bool
	RegionIDs       map[uuid.UUID]struct{}
	SiteIDs         map[uuid.UUID]struct{}
	SiteGroupIDs    map[uuid.UUID]struct{}
	Enclaves        map[string]struct{}
	Organizations   map[string]struct{}
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

// ScopedSiteFilter resolves the caller's scope for capCode into a
// concrete site_id set the handler should pass as scope_site_ids on
// site-rooted LIST/COUNT queries. The expansion walks all three site-
// reachable dimensions (direct SiteIDs + sites under any RegionID +
// sites in any SiteGroupID) via a single SQL query.
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
	s := FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil, false, nil
	}
	var directs, regions, groups []uuid.UUID
	if len(s.SiteIDs) > 0 {
		directs = make([]uuid.UUID, 0, len(s.SiteIDs))
		for id := range s.SiteIDs {
			directs = append(directs, id)
		}
	}
	if len(s.RegionIDs) > 0 {
		regions = make([]uuid.UUID, 0, len(s.RegionIDs))
		for id := range s.RegionIDs {
			regions = append(regions, id)
		}
	}
	if len(s.SiteGroupIDs) > 0 {
		groups = make([]uuid.UUID, 0, len(s.SiteGroupIDs))
		for id := range s.SiteGroupIDs {
			groups = append(groups, id)
		}
	}
	if directs == nil && regions == nil && groups == nil {
		return []uuid.UUID{}, true, nil
	}
	ids, err := q.ListSiteIDsForExpansion(ctx, dbq.ListSiteIDsForExpansionParams{
		DirectSiteIds: directs, RegionIds: regions, GroupIds: groups,
	})
	if err != nil {
		return nil, true, err
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids, true, nil
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
// region + site-group dimensions via DB lookups when needed; for
// site-scoped principals it's a pure set check.
type siteScopeQ interface {
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
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
	if s, ok := p.Scopes[code]; ok {
		return &s
	}
	// Held via wildcard but no exact-code scope row — pessimistically
	// return the matching-pattern's scope if available, else global.
	// For now: if the principal has a `*` entry, return its scope;
	// otherwise fall back to GlobalScope. PR 54 may want stricter
	// behavior (refuse on pattern mismatch); we ship the permissive
	// version first since today every cap is global anyway.
	if s, ok := p.Scopes["*"]; ok {
		return &s
	}
	g := GlobalScope()
	return &g
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
var ErrOutsideScope = errors.New("resource is outside your scope")

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
func resolveUserScopes(rows []dbq.ScopedCapabilityRow) map[string]Scope {
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
