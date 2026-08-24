// DELETE /racks/{id} — hard delete added in the UX-debt batch.
// Mirrors the fakeQ handler-test pattern: 404 unknown id, 409 when
// assets are still mounted, 204 + DeleteRack call on success, plus
// the capability + site-scope gates.
package racks

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

func TestDeleteRack_NotFound(t *testing.T) {
	f := &fakeQ{} // default GetRack → ErrNoRows
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/racks/"+uuid.New().String(), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.deleted) != 0 {
		t.Errorf("DeleteRack called %d times on a 404", len(f.deleted))
	}
}

func TestDeleteRack_BadID(t *testing.T) {
	rec := authtest.ServeRequest(mount(&fakeQ{}), authtest.PrincipalWithCaps("*"), "DELETE", "/racks/not-uuid", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestDeleteRack_ConflictWhenAssetsMounted(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		get: func(_ context.Context, gid uuid.UUID) (dbq.Rack, error) {
			return dbq.Rack{ID: gid, SiteID: uuid.New()}, nil
		},
		assetCount: 3,
	}
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/racks/"+id.String(), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "3 mounted assets") {
		t.Errorf("409 body should name the mounted-asset count: %s", rec.Body.String())
	}
	if len(f.deleted) != 0 {
		t.Errorf("DeleteRack must not run when assets are mounted")
	}
}

func TestDeleteRack_Success(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		get: func(_ context.Context, gid uuid.UUID) (dbq.Rack, error) {
			return dbq.Rack{ID: gid, SiteID: uuid.New()}, nil
		},
	}
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"), "DELETE", "/racks/"+id.String(), nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.deleted) != 1 || f.deleted[0] != id {
		t.Errorf("DeleteRack calls = %v, want exactly [%s]", f.deleted, id)
	}
}

func TestDeleteRack_MissingCapability(t *testing.T) {
	id := uuid.New()
	f := &fakeQ{
		get: func(_ context.Context, gid uuid.UUID) (dbq.Rack, error) {
			return dbq.Rack{ID: gid, SiteID: uuid.New()}, nil
		},
	}
	// read+update but no delete — the route gate must refuse.
	p := authtest.PrincipalWithCaps("inventory:racks:read", "inventory:racks:update")
	rec := authtest.ServeRequest(mount(f), p, "DELETE", "/racks/"+id.String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if len(f.deleted) != 0 {
		t.Errorf("DeleteRack ran without the delete capability")
	}
}

func TestEnforceSite_DeleteRack_Forbidden(t *testing.T) {
	owned := uuid.New()
	other := uuid.New()
	q := &scopedFakeQ{siteID: other} // GetRack reports the out-of-scope site
	r := chi.NewRouter()
	(&Handler{Q: q}).Mount(r)

	p := authtest.PrincipalWithScopes(
		[]string{"inventory:racks:delete"},
		map[string]auth.Scope{
			"inventory:racks:delete": {SiteIDs: map[uuid.UUID]struct{}{owned: {}}},
		},
	)
	rec := authtest.ServeRequest(r, p, "DELETE", "/racks/"+uuid.New().String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if len(q.deleted) != 0 {
		t.Errorf("DeleteRack ran for an out-of-scope site")
	}
}
