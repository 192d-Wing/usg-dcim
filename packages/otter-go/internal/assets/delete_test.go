// DELETE /assets/{id} — hard delete added in the UX-debt batch.
// Mirrors the fakeQ handler-test pattern: 404 unknown id, 409 on
// child assets or logged cables, success path runs detach-IPs →
// drop-alerts → delete in order, plus the capability + scope gates.
package assets

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

func TestDeleteAsset_NotFound(t *testing.T) {
	f := &fakeQ{} // default GetAsset → ErrNoRows
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/assets/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.deletedAssets) != 0 {
		t.Errorf("DeleteAsset called on a 404")
	}
}

func TestDeleteAsset_BadID(t *testing.T) {
	rec := authtest.ServeRequest(mount(&fakeQ{}), authtest.PrincipalWithCaps("*"), "DELETE", "/assets/not-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestDeleteAsset_ConflictWithChildren(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		asset:      &dbq.Asset{ID: id, SiteID: uuid.New(), Name: "chassis-1"},
		childCount: 2,
	}
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/assets/"+id.String(), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "2 child assets") {
		t.Errorf("409 body should name the child-asset count: %s", rec.Body.String())
	}
	if len(f.deletedAssets) != 0 || len(f.detachedIPs) != 0 || len(f.deletedAlerts) != 0 {
		t.Errorf("no destructive call may run when children exist")
	}
}

func TestDeleteAsset_ConflictWithCables(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		asset:      &dbq.Asset{ID: id, SiteID: uuid.New(), Name: "sw-1"},
		cableCount: 4,
	}
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/assets/"+id.String(), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "4 cables logged") {
		t.Errorf("409 body should name the cable count: %s", rec.Body.String())
	}
	if len(f.deletedAssets) != 0 || len(f.detachedIPs) != 0 || len(f.deletedAlerts) != 0 {
		t.Errorf("no destructive call may run when cables are logged")
	}
}

func TestDeleteAsset_SuccessRunsCleanupChain(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		asset: &dbq.Asset{ID: id, SiteID: uuid.New(), Name: "srv-1", LifecycleState: "decommissioned"},
	}
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/assets/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.detachedIPs) != 1 || f.detachedIPs[0] != id {
		t.Errorf("DetachIPAddressesFromAsset calls = %v, want [%s]", f.detachedIPs, id)
	}
	if len(f.deletedAlerts) != 1 || f.deletedAlerts[0] != id {
		t.Errorf("DeleteAlertsForAsset calls = %v, want [%s]", f.deletedAlerts, id)
	}
	if len(f.deletedAssets) != 1 || f.deletedAssets[0] != id {
		t.Errorf("DeleteAsset calls = %v, want [%s]", f.deletedAssets, id)
	}
}

func TestDeleteAsset_MissingCapability(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{asset: &dbq.Asset{ID: id, SiteID: uuid.New()}}
	// read+update (decommission's caps) but no delete — the gate refuses.
	p := authtest.PrincipalWithCaps("inventory:assets:read", "inventory:assets:update")
	rec := authtest.ServeRequest(mount(f), p, "DELETE", "/assets/"+id.String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if len(f.deletedAssets) != 0 {
		t.Errorf("DeleteAsset ran without the delete capability")
	}
}

func TestEnforceSite_DeleteAsset_Forbidden(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	id := uuid.New()
	f := &fakeQ{asset: &dbq.Asset{ID: id, SiteID: other}}
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)

	p := authtest.PrincipalWithScopes(
		[]string{"inventory:assets:delete"},
		map[string]auth.Scope{
			"inventory:assets:delete": {SiteIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	)
	rec := authtest.ServeRequest(r, p, "DELETE", "/assets/"+id.String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if len(f.deletedAssets) != 0 {
		t.Errorf("DeleteAsset ran for an out-of-scope site")
	}
}
