package regiondeploy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// happyConfig returns a config blob that passes every site.* / bgp.*
// check. Tests that want to exercise a single failure path edit one
// key on top of this.
func happyConfig() map[string]any {
	return map[string]any{
		"pod_cidr_v6": "fd00:site:42:1000::/56",
		"svc_cidr_v6": "fd00:site:42:2000::/112",
		"lb_pool_v6":  "fd00:site:42:3000::/120",
		"bgp_peers":   []any{map[string]any{"asn": 65001, "peer": "fd00::1"}},
	}
}

func happyNodes() []dbq.RegionDeploymentNode {
	return []dbq.RegionDeploymentNode{
		{Hostname: "n01", Mac: "aa:bb:cc:dd:ee:01", Role: "control_plane"},
		{Hostname: "n02", Mac: "aa:bb:cc:dd:ee:02", Role: "worker"},
	}
}

func TestPreflight_AllPass_ReadyTrue(t *testing.T) {
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: happyConfig()})
	if !out.Ready {
		t.Fatalf("expected ready=true; got %+v", out)
	}
	if len(out.Checks) != 7 {
		t.Fatalf("expected 7 checks (Python parity), got %d", len(out.Checks))
	}
	for _, c := range out.Checks {
		if !c.Passed {
			t.Errorf("check %q should pass; hint=%v", c.Key, c.FixHint)
		}
		if c.FixHint != nil {
			t.Errorf("passing check %q must not carry fix_hint; got %q", c.Key, *c.FixHint)
		}
	}
}

func TestPreflight_CheckKeyOrderMatchesPython(t *testing.T) {
	// finch persists "which checks were dismissed" by key — reordering
	// or renaming a key invalidates saved wizard state across the
	// Python→Go cutover. Pin the order explicitly.
	want := []string{
		"nodes.distinct_macs",
		"nodes.distinct_hostnames",
		"nodes.has_control_plane",
		"site.has_v6_pod_prefix",
		"site.has_v6_svc_prefix",
		"site.has_v6_lb_pool",
		"bgp.peers_configured",
	}
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: happyConfig()})
	for i, c := range out.Checks {
		if c.Key != want[i] {
			t.Errorf("checks[%d].key = %q, want %q", i, c.Key, want[i])
		}
	}
}

func TestPreflight_DuplicateMAC_NamesBothHostnames(t *testing.T) {
	nodes := []dbq.RegionDeploymentNode{
		{Hostname: "n01", Mac: "aa:bb:cc:dd:ee:01", Role: "control_plane"},
		{Hostname: "n02", Mac: "aa:bb:cc:dd:ee:01", Role: "worker"},
	}
	out := runPreflight(preflightContext{Nodes: nodes, Config: happyConfig()})
	if out.Ready {
		t.Fatalf("expected ready=false")
	}
	c := findCheck(t, out, "nodes.distinct_macs")
	if c.Passed || c.FixHint == nil {
		t.Fatalf("expected failed + hint; got %+v", c)
	}
	if !bytes.Contains([]byte(*c.FixHint), []byte("n01")) || !bytes.Contains([]byte(*c.FixHint), []byte("n02")) {
		t.Errorf("hint should name both hostnames; got %q", *c.FixHint)
	}
}

func TestPreflight_MAC_CaseInsensitiveCollision(t *testing.T) {
	// Python's _check_distinct_macs does .lower() on the MAC strings
	// before comparing — confirm the Go path matches.
	nodes := []dbq.RegionDeploymentNode{
		{Hostname: "n01", Mac: "AA:BB:CC:DD:EE:01", Role: "control_plane"},
		{Hostname: "n02", Mac: "aa:bb:cc:dd:ee:01", Role: "worker"},
	}
	out := runPreflight(preflightContext{Nodes: nodes, Config: happyConfig()})
	c := findCheck(t, out, "nodes.distinct_macs")
	if c.Passed {
		t.Errorf("AA: and aa: should collide; got passed=true")
	}
}

func TestPreflight_DuplicateHostname(t *testing.T) {
	nodes := []dbq.RegionDeploymentNode{
		{Hostname: "n01", Mac: "aa:bb:cc:dd:ee:01", Role: "control_plane"},
		{Hostname: "n01", Mac: "aa:bb:cc:dd:ee:02", Role: "worker"},
	}
	out := runPreflight(preflightContext{Nodes: nodes, Config: happyConfig()})
	c := findCheck(t, out, "nodes.distinct_hostnames")
	if c.Passed {
		t.Errorf("duplicate hostname should fail; got passed=true")
	}
}

func TestPreflight_NoControlPlane(t *testing.T) {
	nodes := []dbq.RegionDeploymentNode{
		{Hostname: "n01", Mac: "aa:bb:cc:dd:ee:01", Role: "worker"},
		{Hostname: "n02", Mac: "aa:bb:cc:dd:ee:02", Role: "edge"},
	}
	out := runPreflight(preflightContext{Nodes: nodes, Config: happyConfig()})
	c := findCheck(t, out, "nodes.has_control_plane")
	if c.Passed || c.FixHint == nil {
		t.Fatalf("expected failed with hint; got %+v", c)
	}
}

func TestPreflight_MissingV6PodPrefix(t *testing.T) {
	cfg := happyConfig()
	delete(cfg, "pod_cidr_v6")
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: cfg})
	c := findCheck(t, out, "site.has_v6_pod_prefix")
	if c.Passed {
		t.Errorf("missing pod_cidr_v6 should fail; got passed=true")
	}
}

func TestPreflight_EmptyStringConfigKeyFails(t *testing.T) {
	// Python's `if not value:` treats "" as falsy; Go must match.
	cfg := happyConfig()
	cfg["svc_cidr_v6"] = ""
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: cfg})
	c := findCheck(t, out, "site.has_v6_svc_prefix")
	if c.Passed {
		t.Errorf("empty-string svc_cidr_v6 should fail; got passed=true")
	}
}

func TestPreflight_MissingLBPool(t *testing.T) {
	cfg := happyConfig()
	delete(cfg, "lb_pool_v6")
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: cfg})
	c := findCheck(t, out, "site.has_v6_lb_pool")
	if c.Passed {
		t.Errorf("missing lb_pool_v6 should fail")
	}
}

func TestPreflight_EmptyBGPPeersList(t *testing.T) {
	cfg := happyConfig()
	cfg["bgp_peers"] = []any{}
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: cfg})
	c := findCheck(t, out, "bgp.peers_configured")
	if c.Passed {
		t.Errorf("empty peers list should fail")
	}
}

func TestPreflight_NilConfig_AllSiteAndBGPFail(t *testing.T) {
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: nil})
	if out.Ready {
		t.Fatalf("nil config must not be ready")
	}
	// node checks still pass; site.* + bgp.* must all fail
	for _, key := range []string{"site.has_v6_pod_prefix", "site.has_v6_svc_prefix", "site.has_v6_lb_pool", "bgp.peers_configured"} {
		c := findCheck(t, out, key)
		if c.Passed {
			t.Errorf("nil config: %s should fail", key)
		}
	}
}

func TestPreflight_FixHint_JSONNullForPassing(t *testing.T) {
	// Python emits `"fix_hint": null` for a passing check; Go's *string
	// `omitempty` would drop the field instead — confirm the JSON tag
	// is bare so the key is always present for the finch wizard's
	// field-by-key access pattern.
	out := runPreflight(preflightContext{Nodes: happyNodes(), Config: happyConfig()})
	b, _ := json.Marshal(out.Checks[0])
	if !bytes.Contains(b, []byte(`"fix_hint":null`)) {
		t.Errorf("fix_hint must serialize as null when passing; got %s", b)
	}
}

func findCheck(t *testing.T, out preflightResponse, key string) preflightOutcome {
	t.Helper()
	for _, c := range out.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("check %q not found", key)
	return preflightOutcome{}
}

// ─── HTTP handler integration ───────────────────────────────────────────

func TestPreflight_HTTP_OK_AllPass(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	cfg, _ := json.Marshal(happyConfig())
	f := &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending", Config: cfg},
		nodes:  happyNodes(),
	}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String()+"/preflight")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var body preflightResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ready {
		t.Errorf("expected ready=true on happy path; got %+v", body)
	}
	if len(body.Checks) != 7 {
		t.Errorf("expected 7 checks, got %d", len(body.Checks))
	}
}

func TestPreflight_HTTP_NotFound(t *testing.T) {
	f := &fakeQ{getErr: pgx.ErrNoRows}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+uuid.New().String()+"/preflight")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPreflight_HTTP_BadID_400(t *testing.T) {
	rec := doReq(t, mount(&fakeQ{}), wildcardP(), "/region-deployments/not-a-uuid/preflight")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPreflight_HTTP_OutOfScope_403(t *testing.T) {
	id, sid, otherSite := uuid.New(), uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"}}
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{otherSite: {}}}
	p := authtest.PrincipalWithScopes([]string{capRead}, map[string]auth.Scope{capRead: scope})
	rec := doReq(t, mount(f), p, "/region-deployments/"+id.String()+"/preflight")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPreflight_HTTP_NullConfigJSONBdecodesToNil(t *testing.T) {
	// A deployment row whose config column is literally `null::jsonb`
	// (shouldn't happen post-create-port but defends against legacy
	// rows) must not panic — decodeConfig short-circuits the literal
	// "null" 4-byte form.
	id, sid := uuid.New(), uuid.New()
	f := &fakeQ{getRow: dbq.RegionDeployment{
		ID: id, SiteID: sid, Status: "pending", Config: json.RawMessage("null"),
	}, nodes: happyNodes()}
	rec := doReq(t, mount(f), wildcardP(), "/region-deployments/"+id.String()+"/preflight")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var body preflightResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ready {
		t.Errorf("null config must not be ready")
	}
}
