package bgp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// Each BGP GET route requires its matching routing:X:read capability
// after PR #205. A principal holding a different routing:*:read cap
// must NOT be able to enumerate the resource.
func TestBGP_GETsRequireMatchingReadCap(t *testing.T) {
	cases := []struct {
		path   string
		cap    string
		others []string
	}{
		{"/bgp/asns", "routing:asns:read", []string{"routing:prefix-lists:read"}},
		{"/bgp/prefix-lists", "routing:prefix-lists:read", []string{"routing:asns:read"}},
		{"/bgp/prefix-list-entries", "routing:prefix-list-entries:read", []string{"routing:prefix-lists:read"}},
		{"/bgp/community-lists", "routing:community-lists:read", []string{"routing:asns:read"}},
		{"/bgp/community-list-entries", "routing:community-list-entries:read", []string{"routing:community-lists:read"}},
		{"/bgp/route-maps", "routing:route-maps:read", []string{"routing:asns:read"}},
		{"/bgp/route-map-entries", "routing:route-map-entries:read", []string{"routing:route-maps:read"}},
	}
	for _, tc := range cases {
		t.Run(tc.path+"_AllowedWithCap", func(t *testing.T) {
			r := chi.NewRouter()
			(&Handler{Q: &fakeQ{}}).Mount(r)
			req := authtest.Request(http.MethodGet, tc.path, authtest.PrincipalWithCaps(tc.cap), nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("with %s: got %d body=%s", tc.cap, rec.Code, rec.Body.String())
			}
		})
		t.Run(tc.path+"_DeniedWithOtherCap", func(t *testing.T) {
			r := chi.NewRouter()
			(&Handler{Q: &fakeQ{}}).Mount(r)
			req := authtest.Request(http.MethodGet, tc.path, authtest.PrincipalWithCaps(tc.others...), nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("with %v (no matching read cap): expected 403, got %d", tc.others, rec.Code)
			}
		})
	}
}
