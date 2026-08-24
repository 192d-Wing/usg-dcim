// DELETE /racks/{id} — hard delete added in the UX-debt batch.
// Mirrors the fakeQ handler-test pattern: refused requests (404/400/
// 409/403) are table-driven and must never reach DeleteRack; success
// is 204 with exactly one DeleteRack call; site scope is enforced.
package racks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// existingRackQ returns a fakeQ whose GetRack always succeeds and that
// reports assetCount mounted assets to the 409 guard.
func existingRackQ(assetCount int64) *fakeQ {
	return &fakeQ{
		get: func(_ context.Context, gid uuid.UUID) (dbq.Rack, error) {
			return dbq.Rack{ID: gid, SiteID: uuid.New(), UHeight: 42}, nil
		},
		assetCount: assetCount,
	}
}

func doDelete(t *testing.T, f *fakeQ, p auth.Principal, id string) *httptest.ResponseRecorder {
	t.Helper()
	return authtest.ServeRequest(mount(f), p, "DELETE", "/racks/"+id, nil)
}

func TestDeleteRack_Refusals(t *testing.T) {
	admin := authtest.PrincipalWithCaps("*")
	cases := []struct {
		name     string
		q        *fakeQ
		p        auth.Principal
		id       string
		wantCode int
		wantBody string
	}{
		{"unknown id", &fakeQ{}, admin, uuid.New().String(), http.StatusNotFound, ""},
		{"bad id", &fakeQ{}, admin, "not-uuid", http.StatusBadRequest, ""},
		{"mounted assets", existingRackQ(3), admin, uuid.New().String(),
			http.StatusConflict, "3 mounted assets"},
		// read+update but no delete — the route gate must refuse.
		{"missing capability", existingRackQ(0),
			authtest.PrincipalWithCaps("inventory:racks:read", "inventory:racks:update"),
			uuid.New().String(), http.StatusForbidden, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doDelete(t, tc.q, tc.p, tc.id)
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d, want %d (body=%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantBody != "" && !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body %q should contain %q", rec.Body.String(), tc.wantBody)
			}
			if len(tc.q.deleted) != 0 {
				t.Errorf("DeleteRack ran on a refused request")
			}
		})
	}
}

func TestDeleteRack_Success(t *testing.T) {
	id := uuid.New()
	f := existingRackQ(0)
	rec := doDelete(t, f, authtest.PrincipalWithCaps("*"), id.String())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.deleted) != 1 || f.deleted[0] != id {
		t.Errorf("DeleteRack calls = %v, want exactly [%s]", f.deleted, id)
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
