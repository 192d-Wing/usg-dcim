package ipam

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/bundle"
)

// authedGet is the bundle-test shorthand for "GET this path as the
// supplied principal." Local because the existing `do` helper in
// handler_test.go builds an UNAUTHED request and the bundle route is
// cap-gated.
func authedGet(t *testing.T, h http.Handler, p auth.Principal, target string, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := authtest.Request(http.MethodGet, target, p, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bundleOperator(t *testing.T) auth.Principal {
	t.Helper()
	return authtest.PrincipalWithCaps("ipam:dhcp-servers:bundle")
}

// ---- 404 ----

func TestGetDhcpServerBundle_NotFound(t *testing.T) {
	f := &fakeQ{} // no bundleServer set → GetDhcpServerBundleRow returns ErrNoRows
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+uuid.New().String()+"/bundle", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetDhcpServerBundle_NotFoundFromExplicitNoRows(t *testing.T) {
	// Differentiates the "no matching seed" path from an explicit
	// pgx.ErrNoRows fake-error path; both should land on 404 via
	// errors.Is.
	f := &fakeQ{bundleServerErr: pgx.ErrNoRows}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+uuid.New().String()+"/bundle", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetDhcpServerBundle_BadUUID_400(t *testing.T) {
	f := &fakeQ{}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/not-a-uuid/bundle", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- ABAC ----

func TestGetDhcpServerBundle_RequiresCap(t *testing.T) {
	// Authenticated but capless. The RequireCapability middleware
	// should 403 before the handler ever runs.
	serverID := uuid.New()
	fabricID := uuid.New()
	f := &fakeQ{bundleServer: dbq.DhcpServerBundleRow{ID: serverID, FabricID: fabricID}}
	rec := authedGet(t, mount(f), authtest.PrincipalWithCaps(), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetDhcpServerBundle_DeniesOutOfScopeFabric(t *testing.T) {
	// Principal has the cap but its FabricIDs scope set doesn't
	// include the target server's fabric — enforceFabric should 403.
	serverID := uuid.New()
	targetFabric := uuid.New()
	wrongFabric := uuid.New()
	p := authtest.PrincipalWithScopes(
		[]string{"ipam:dhcp-servers:bundle"},
		map[string]auth.Scope{
			"ipam:dhcp-servers:bundle": {
				FabricIDs: map[uuid.UUID]struct{}{wrongFabric: {}},
			},
		},
	)
	f := &fakeQ{bundleServer: dbq.DhcpServerBundleRow{ID: serverID, FabricID: targetFabric}}
	rec := authedGet(t, mount(f), p, "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---- Cache hit ----

func TestGetDhcpServerBundle_CacheHitReturnsRawJSON(t *testing.T) {
	// When bundle_cache_etag + bundle_cache_json are both set, the
	// handler returns the cached bytes verbatim — no re-encoding.
	// This preserves the etag-canonical key order so the body hash
	// matches the etag header the puller's seen.
	serverID := uuid.New()
	fabricID := uuid.New()
	cachedEtag := "deadbeef" + "cafebabe" // distinctive marker
	cachedJSON := json.RawMessage(`{"server_id":"abc","ctrl_agent":{},"dhcp4":{"subnet4":[]},"dhcp6":{"subnet6":[]},"etag":"deadbeefcafebabe"}`)
	etagPtr := cachedEtag
	f := &fakeQ{bundleServer: dbq.DhcpServerBundleRow{
		ID: serverID, FabricID: fabricID,
		BundleCacheEtag: &etagPtr,
		BundleCacheJSON: cachedJSON,
	}}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(cachedJSON) {
		t.Errorf("body should be raw cached JSON; got %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"`+cachedEtag+`"` {
		t.Errorf("ETag header: got %q, want %q", got, `"`+cachedEtag+`"`)
	}
	// The cache-hit path must NOT call into the scope/template
	// queries — that would be wasted DB work. Boolean flag catches
	// the case where the query was called with uuid.Nil too (which
	// the lastBundleScopesServerID check alone would miss).
	if f.bundleScopesCalled {
		t.Errorf("scope query should not be called on cache hit; got serverID=%s", f.lastBundleScopesServerID)
	}
}

func TestGetDhcpServerBundle_EmptyCacheEtag_FallsThroughToLiveRender(t *testing.T) {
	// Parity with Python: `if server.bundle_cache_etag and ...`
	// is falsy on an empty string. Go must do the same — without
	// the `*etag != ""` guard a row whose writer set the etag to
	// "" by mistake (partial write, sentinel) would serve a cache
	// hit with an empty ETag header.
	serverID := uuid.New()
	fabricID := uuid.New()
	emptyEtag := ""
	f := &fakeQ{bundleServer: dbq.DhcpServerBundleRow{
		ID: serverID, FabricID: fabricID,
		BundleCacheEtag: &emptyEtag,
		BundleCacheJSON: json.RawMessage(`{"server_id":"x"}`),
		BaseConfig:      json.RawMessage(`{}`),
	}}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.bundleScopesCalled {
		t.Errorf("empty etag should fall through to live render; scope query was not called")
	}
}

func TestGetDhcpServerBundle_CacheHit_IfNoneMatch_304(t *testing.T) {
	// 304 cache-hit short-circuit covers two invariants in one
	// place: empty body (so the puller doesn't waste bytes on a
	// no-change tick) AND the ETag header echo per RFC 9110
	// §15.4.5 (Python's `Response(status_code=304)` omitted it;
	// the Go port adds it so intermediaries can refresh their
	// validator).
	serverID := uuid.New()
	fabricID := uuid.New()
	etag := "stable-etag"
	etagPtr := etag
	f := &fakeQ{bundleServer: dbq.DhcpServerBundleRow{
		ID: serverID, FabricID: fabricID,
		BundleCacheEtag: &etagPtr,
		BundleCacheJSON: json.RawMessage(`{}`),
	}}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", `"`+etag+`"`)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("want 304, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 response should have empty body; got %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"`+etag+`"` {
		t.Errorf("304 should echo ETag header per RFC 9110; got %q", got)
	}
}

func TestGetDhcpServerBundle_CacheIncomplete_FallsThroughToLiveRender(t *testing.T) {
	// Etag set but JSON empty (mid-bootstrap) → live render. The
	// puller never sees a half-baked bundle.
	serverID := uuid.New()
	fabricID := uuid.New()
	etag := "partial-state"
	f := &fakeQ{
		bundleServer: dbq.DhcpServerBundleRow{
			ID: serverID, FabricID: fabricID,
			BundleCacheEtag: &etag,
			// BundleCacheJSON intentionally nil
		},
	}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Live render fired — scope query was called with the server's id.
	if f.lastBundleScopesServerID != serverID {
		t.Errorf("scope query should have been called for live render; lastBundleScopesServerID=%s, want %s",
			f.lastBundleScopesServerID, serverID)
	}
}

// ---- Live render ----

func TestGetDhcpServerBundle_LiveRender_ReturnsKeaBundle(t *testing.T) {
	serverID := uuid.New()
	fabricID := uuid.New()
	scopeID := uuid.New()
	tplID := uuid.New()
	vl := int32(3600)
	kid := int32(1)
	f := &fakeQ{
		bundleServer: dbq.DhcpServerBundleRow{
			ID: serverID, FabricID: fabricID,
			BaseConfig: json.RawMessage(`{"dhcp4":{"interfaces-config":{"interfaces":["eth0"]}}}`),
			// No cache columns.
		},
		bundleScopes: []dbq.DhcpScope{{
			ID: scopeID, DhcpServerID: serverID, IPFamily: 4,
			Prefix: "10.0.0.0/24", PoolsJSON: json.RawMessage(`[{"first":"10.0.0.10","last":"10.0.0.250"}]`),
			ValidLifetimeSeconds: &vl, KeaSubnetID: &kid, Enabled: true,
			TemplateID: &tplID,
		}},
		bundleTemplates: []dbq.DhcpScopeTemplate{{
			ID: tplID, FabricID: fabricID, IPFamily: 4,
			OptionsJSON: json.RawMessage(`[{"code":3,"name":"routers","data":"10.0.0.1"}]`),
		}},
	}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Template ID was forwarded to the bulk-load query.
	if len(f.lastBundleTemplateIDs) != 1 || f.lastBundleTemplateIDs[0] != tplID {
		t.Errorf("template bulk-load: got %v, want [%s]", f.lastBundleTemplateIDs, tplID)
	}
	var got bundle.KeaBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response not a KeaBundle: %v", err)
	}
	if got.ServerID != serverID.String() {
		t.Errorf("server_id: got %q, want %q", got.ServerID, serverID.String())
	}
	s4, ok := got.Dhcp4["subnet4"].([]any)
	if !ok || len(s4) != 1 {
		t.Fatalf("dhcp4.subnet4: want 1 entry, got %v", got.Dhcp4["subnet4"])
	}
	if s4[0].(map[string]any)["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet: got %v", s4[0])
	}
	// ETag header is set on live-render too.
	if rec.Header().Get("ETag") == "" {
		t.Errorf("ETag header should be set on live render")
	}
}

func TestGetDhcpServerBundle_LiveRender_IfNoneMatch_304(t *testing.T) {
	// Render once to capture the live etag, then re-issue with
	// If-None-Match set to that etag — should return 304 + empty body.
	serverID := uuid.New()
	fabricID := uuid.New()
	f := &fakeQ{
		bundleServer: dbq.DhcpServerBundleRow{
			ID: serverID, FabricID: fabricID,
			BaseConfig: json.RawMessage(`{}`),
		},
		bundleScopes: nil,
	}
	rec1 := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("first render: want 200, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	etag := rec1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("first render didn't emit ETag header")
	}
	rec2 := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", etag)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("second request with matching etag: want 304, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 response should have empty body; got %q", rec2.Body.String())
	}
}

func TestGetDhcpServerBundle_LiveRender_NoTemplates_SkipsBulkLoad(t *testing.T) {
	// Every scope has TemplateID == nil → ListDhcpScopeTemplatesByIDs
	// must NOT be called (saves a wasted DB round-trip on the common
	// no-template path).
	serverID := uuid.New()
	fabricID := uuid.New()
	kid := int32(1)
	f := &fakeQ{
		bundleServer: dbq.DhcpServerBundleRow{ID: serverID, FabricID: fabricID, BaseConfig: json.RawMessage(`{}`)},
		bundleScopes: []dbq.DhcpScope{{
			ID: uuid.New(), DhcpServerID: serverID, IPFamily: 4,
			Prefix: "10.0.0.0/24", PoolsJSON: json.RawMessage(`[]`),
			KeaSubnetID: &kid, Enabled: true,
			TemplateID:  nil,
		}},
	}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastBundleTemplateIDs != nil {
		t.Errorf("template bulk-load should be skipped when no scope has a template; got %v", f.lastBundleTemplateIDs)
	}
}

func TestGetDhcpServerBundle_LiveRender_DedupesTemplateIDs(t *testing.T) {
	// Multiple scopes sharing the same template — bulk-load receives
	// the unique set, not duplicates.
	serverID := uuid.New()
	fabricID := uuid.New()
	tplID := uuid.New()
	kid1, kid2 := int32(1), int32(2)
	f := &fakeQ{
		bundleServer: dbq.DhcpServerBundleRow{ID: serverID, FabricID: fabricID, BaseConfig: json.RawMessage(`{}`)},
		bundleScopes: []dbq.DhcpScope{
			{ID: uuid.New(), DhcpServerID: serverID, IPFamily: 4, Prefix: "10.0.0.0/24",
				PoolsJSON: json.RawMessage(`[]`), KeaSubnetID: &kid1, TemplateID: &tplID, Enabled: true},
			{ID: uuid.New(), DhcpServerID: serverID, IPFamily: 4, Prefix: "10.0.1.0/24",
				PoolsJSON: json.RawMessage(`[]`), KeaSubnetID: &kid2, TemplateID: &tplID, Enabled: true},
		},
	}
	rec := authedGet(t, mount(f), bundleOperator(t), "/ipam/dhcp/servers/"+serverID.String()+"/bundle", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.lastBundleTemplateIDs) != 1 {
		t.Errorf("template IDs should be de-duped; got %d entries: %v", len(f.lastBundleTemplateIDs), f.lastBundleTemplateIDs)
	}
}

// ---- stripQuotes coverage ----

func TestStripQuotesUnwrapsETagHeader(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`"abc"`, "abc"},
		{`abc`, "abc"},     // bare etag (some clients omit quotes)
		{`"`, `"`},          // single quote stays — too short to unwrap
		{`""`, ""},          // empty quoted etag
		{`"a"b"`, `a"b`},    // only the outer quotes get stripped
	}
	for _, tc := range cases {
		if got := stripQuotes(tc.in); got != tc.want {
			t.Errorf("stripQuotes(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}
