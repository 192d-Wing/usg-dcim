package regions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// Global principal: no scope filter, ListRegions called with RegionIds=nil.
func TestList_GlobalPrincipal_NoRegionFilter(t *testing.T) {
	var lastList dbq.ListRegionsParams
	f := &fakeQuerier{
		list: func(_ context.Context, a dbq.ListRegionsParams) ([]dbq.Region, error) {
			lastList = a
			return nil, nil
		},
	}
	req := authtest.Request(http.MethodGet, "/regions", authtest.PrincipalWithCaps("*"), nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if lastList.RegionIds != nil {
		t.Errorf("global principal must not filter; got RegionIds=%v", lastList.RegionIds)
	}
}

// Scoped principal with direct RegionIDs only: ListRegions sees just
// those — exactly those, in any order. The cardinality check guards
// against future bugs where a wildcard scope row over-unions extra
// regions into the filter set.
func TestList_ScopedToDirectRegions(t *testing.T) {
	r1, r2 := uuid.New(), uuid.New()
	var lastList dbq.ListRegionsParams
	expansionCalled := false
	f := &fakeQuerier{
		list: func(_ context.Context, a dbq.ListRegionsParams) ([]dbq.Region, error) {
			lastList = a
			return nil, nil
		},
		expandSites: func(context.Context, dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
			expansionCalled = true
			return nil, nil
		},
	}
	scope := auth.Scope{RegionIDs: map[uuid.UUID]struct{}{r1: {}, r2: {}}}
	p := authtest.PrincipalWithScopes(
		[]string{"inventory:regions:read"},
		map[string]auth.Scope{"inventory:regions:read": scope},
	)
	req := authtest.Request(http.MethodGet, "/regions", p, nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(lastList.RegionIds) != 2 {
		t.Fatalf("expected exactly 2 region ids, got %d (%v)", len(lastList.RegionIds), lastList.RegionIds)
	}
	gotSet := map[uuid.UUID]struct{}{}
	for _, id := range lastList.RegionIds {
		gotSet[id] = struct{}{}
	}
	if _, ok := gotSet[r1]; !ok {
		t.Errorf("missing r1 in filter: %v", lastList.RegionIds)
	}
	if _, ok := gotSet[r2]; !ok {
		t.Errorf("missing r2 in filter: %v", lastList.RegionIds)
	}
	if expansionCalled {
		t.Error("ScopedSiteFilter must not run when only direct RegionIDs are set (fast-path skip)")
	}
}

// Scoped via SiteIDs: handler expands sites → regions and filters.
// siteB exists with its own region but is not in scope — the filter
// must include regionOfSiteA only, never regionOfSiteB.
func TestList_ScopedViaSiteReachableRegions(t *testing.T) {
	siteA, siteB := uuid.New(), uuid.New()
	regionOfSiteA, regionOfSiteB := uuid.New(), uuid.New()
	var lastList dbq.ListRegionsParams
	f := &fakeQuerier{
		list: func(_ context.Context, a dbq.ListRegionsParams) ([]dbq.Region, error) {
			lastList = a
			return nil, nil
		},
		expandSites: func(_ context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
			// Mirror the SQL: only return sites the caller's scope
			// dimensions list. siteB is intentionally excluded.
			gotDirect := map[uuid.UUID]struct{}{}
			for _, id := range arg.DirectSiteIds {
				gotDirect[id] = struct{}{}
			}
			if _, ok := gotDirect[siteA]; !ok {
				t.Errorf("siteA missing from expansion input: %+v", arg)
			}
			if _, ok := gotDirect[siteB]; ok {
				t.Errorf("siteB must NOT be in expansion input (out of scope); got %+v", arg)
			}
			return []uuid.UUID{siteA}, nil
		},
		regionsForSites: func(_ context.Context, sites []uuid.UUID) ([]uuid.UUID, error) {
			if len(sites) == 1 && sites[0] == siteA {
				return []uuid.UUID{regionOfSiteA}, nil
			}
			t.Errorf("regionsForSites should only receive [siteA]; got %v", sites)
			return nil, nil
		},
	}
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{siteA: {}}}
	p := authtest.PrincipalWithScopes(
		[]string{"inventory:regions:read"},
		map[string]auth.Scope{"inventory:regions:read": scope},
	)
	req := authtest.Request(http.MethodGet, "/regions", p, nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(lastList.RegionIds) != 1 || lastList.RegionIds[0] != regionOfSiteA {
		t.Errorf("expected only regionOfSiteA; got %v", lastList.RegionIds)
	}
	for _, id := range lastList.RegionIds {
		if id == regionOfSiteB {
			t.Errorf("regionOfSiteB leaked into the filter set: %v", lastList.RegionIds)
		}
	}
}

// Scoped principal with no site-reachable dimensions and no direct
// RegionIDs gets an empty page without hitting ListRegions or
// CountRegions — both queries must short-circuit, not just list.
func TestList_ScopedButReachesNothing_EmptyPage(t *testing.T) {
	listCalled, countCalled := false, false
	f := &fakeQuerier{
		list: func(_ context.Context, _ dbq.ListRegionsParams) ([]dbq.Region, error) {
			listCalled = true
			return nil, nil
		},
		count: func(_ context.Context, _ []uuid.UUID) (int64, error) {
			countCalled = true
			return 0, nil
		},
	}
	// Enclaves are not site-reachable and grant no region access.
	scope := auth.Scope{Enclaves: map[string]struct{}{"unclassified": {}}}
	p := authtest.PrincipalWithScopes(
		[]string{"inventory:regions:read"},
		map[string]auth.Scope{"inventory:regions:read": scope},
	)
	req := authtest.Request(http.MethodGet, "/regions", p, nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var body listResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Total != 0 || len(body.Items) != 0 {
		t.Errorf("expected empty page; got %+v", body)
	}
	if listCalled {
		t.Error("ListRegions must not run when scope reaches zero regions")
	}
	if countCalled {
		t.Error("CountRegions must not run when scope reaches zero regions")
	}
}

// Site-rooted scope whose expansion returns an empty site set (e.g.
// a SiteGroupIDs binding that points to a group with no sites). The
// empty expansion path must NOT call ListRegionIDsForSiteIDs.
func TestList_SiteExpansionReturnsEmpty_NoRollupCall(t *testing.T) {
	sg := uuid.New()
	regionRollupCalled := false
	f := &fakeQuerier{
		expandSites: func(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
			return []uuid.UUID{}, nil
		},
		regionsForSites: func(context.Context, []uuid.UUID) ([]uuid.UUID, error) {
			regionRollupCalled = true
			return nil, nil
		},
	}
	scope := auth.Scope{SiteGroupIDs: map[uuid.UUID]struct{}{sg: {}}}
	p := authtest.PrincipalWithScopes(
		[]string{"inventory:regions:read"},
		map[string]auth.Scope{"inventory:regions:read": scope},
	)
	req := authtest.Request(http.MethodGet, "/regions", p, nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if regionRollupCalled {
		t.Error("ListRegionIDsForSiteIDs must not run with an empty expansion result")
	}
}

// Get-by-id outside the in-scope region set → 403.
func TestGet_OutsideRegionScope_403(t *testing.T) {
	inScope, otherRegion := uuid.New(), uuid.New()
	f := &fakeQuerier{
		get: func(_ context.Context, id uuid.UUID) (dbq.Region, error) {
			if id == otherRegion {
				return dbq.Region{ID: otherRegion, Name: "secret", Code: "X"}, nil
			}
			return dbq.Region{}, pgx.ErrNoRows
		},
	}
	scope := auth.Scope{RegionIDs: map[uuid.UUID]struct{}{inScope: {}}}
	p := authtest.PrincipalWithScopes(
		[]string{"inventory:regions:read"},
		map[string]auth.Scope{"inventory:regions:read": scope},
	)
	req := authtest.Request(http.MethodGet, "/regions/"+otherRegion.String(), p, nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Get-by-id inside the in-scope region set → 200.
func TestGet_InsideRegionScope_200(t *testing.T) {
	inScope := uuid.New()
	f := &fakeQuerier{
		get: func(_ context.Context, id uuid.UUID) (dbq.Region, error) {
			return dbq.Region{ID: id, Name: "ok", Code: "OK"}, nil
		},
	}
	scope := auth.Scope{RegionIDs: map[uuid.UUID]struct{}{inScope: {}}}
	p := authtest.PrincipalWithScopes(
		[]string{"inventory:regions:read"},
		map[string]auth.Scope{"inventory:regions:read": scope},
	)
	req := authtest.Request(http.MethodGet, "/regions/"+inScope.String(), p, nil)
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}
