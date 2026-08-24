// DELETE /assets/{id} — hard delete added in the UX-debt batch.
// Refused requests (404/400/409-children/409-cables/403) are table-
// driven and must never reach the destructive calls; success runs the
// detach-IPs → drop-alerts → delete chain; site scope is enforced.
package assets

import (
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

// existingAssetQ returns a fakeQ that serves an asset for GetAsset and
// feeds the given dependent counts to the delete handler's 409 guards.
func existingAssetQ(children, cables int64) *fakeQ {
	return &fakeQ{
		asset:      &dbq.Asset{ID: uuid.New(), SiteID: uuid.New(), Name: "srv-1"},
		childCount: children,
		cableCount: cables,
	}
}

func doDelete(t *testing.T, f *fakeQ, p auth.Principal, id string) *httptest.ResponseRecorder {
	t.Helper()
	return authtest.ServeRequest(mount(f), p, "DELETE", "/assets/"+id, nil)
}

// destructiveCalls counts every write the delete path can make, so
// refusal cases can assert none of them ran.
func destructiveCalls(f *fakeQ) int {
	return len(f.detachedIPs) + len(f.deletedAlerts) + len(f.deletedAssets)
}

func TestDeleteAsset_Refusals(t *testing.T) {
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
		{"child assets", existingAssetQ(2, 0), admin, uuid.New().String(),
			http.StatusConflict, "2 child assets"},
		{"logged cables", existingAssetQ(0, 4), admin, uuid.New().String(),
			http.StatusConflict, "4 cables logged"},
		// read+update (decommission's caps) but no delete — the gate refuses.
		{"missing capability", existingAssetQ(0, 0),
			authtest.PrincipalWithCaps("inventory:assets:read", "inventory:assets:update"),
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
			if n := destructiveCalls(tc.q); n != 0 {
				t.Errorf("%d destructive calls ran on a refused request", n)
			}
		})
	}
}

func TestDeleteAsset_SuccessRunsCleanupChain(t *testing.T) {
	id := uuid.New()
	// Deleting a decommissioned asset must work — decommission only
	// flips lifecycle_state, it is not a delete precondition.
	f := &fakeQ{
		asset: &dbq.Asset{ID: id, SiteID: uuid.New(), Name: "srv-1", LifecycleState: "decommissioned"},
	}
	rec := doDelete(t, f, authtest.PrincipalWithCaps("*"), id.String())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204 (body=%s)", rec.Code, rec.Body.String())
	}
	for _, c := range []struct {
		name string
		got  []uuid.UUID
	}{
		{"DetachIPAddressesFromAsset", f.detachedIPs},
		{"DeleteAlertsForAsset", f.deletedAlerts},
		{"DeleteAsset", f.deletedAssets},
	} {
		if len(c.got) != 1 || c.got[0] != id {
			t.Errorf("%s calls = %v, want [%s]", c.name, c.got, id)
		}
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
	if n := destructiveCalls(f); n != 0 {
		t.Errorf("%d destructive calls ran for an out-of-scope site", n)
	}
}
