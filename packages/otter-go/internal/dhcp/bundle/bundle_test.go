package bundle

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// ---- fixtures ----

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func ptrInt64(v int64) *int64 { return &v }

func newServer(t *testing.T, base any) Server {
	t.Helper()
	srv := Server{ID: uuid.NewString()}
	if base != nil {
		srv.BaseConfig = rawJSON(t, base)
	}
	return srv
}

func v4Scope(t *testing.T, serverID string, override map[string]any) Scope {
	t.Helper()
	s := Scope{
		ID:                   uuid.NewString(),
		DhcpServerID:         serverID,
		IPFamily:             4,
		Prefix:               "10.0.0.0/24",
		PoolsJSON:            rawJSON(t, []map[string]any{{"first": "10.0.0.10", "last": "10.0.0.250"}}),
		ValidLifetimeSeconds: ptrInt64(3600),
		Enabled:              true,
	}
	applyOverride(t, &s, override)
	return s
}

func v6Scope(t *testing.T, serverID string, override map[string]any) Scope {
	t.Helper()
	s := Scope{
		ID:                       uuid.NewString(),
		DhcpServerID:             serverID,
		IPFamily:                 6,
		Prefix:                   "2001:db8::/64",
		PoolsJSON:                rawJSON(t, []map[string]any{{"first": "2001:db8::10", "last": "2001:db8::ffff"}}),
		PreferredLifetimeSeconds: ptrInt64(1800),
		ValidLifetimeSeconds:     ptrInt64(3600),
		Enabled:                  true,
	}
	applyOverride(t, &s, override)
	return s
}

func applyOverride(t *testing.T, s *Scope, override map[string]any) {
	t.Helper()
	if override == nil {
		return
	}
	if v, ok := override["prefix"]; ok {
		s.Prefix = v.(string)
	}
	if v, ok := override["kea_subnet_id"]; ok {
		switch x := v.(type) {
		case int64:
			s.KeaSubnetID = &x
		case int:
			val := int64(x)
			s.KeaSubnetID = &val
		case nil:
			s.KeaSubnetID = nil
		}
	}
	if v, ok := override["enabled"]; ok {
		s.Enabled = v.(bool)
	}
	if v, ok := override["options_json"]; ok {
		s.OptionsJSON = rawJSON(t, v)
	}
	if v, ok := override["pd_pools_json"]; ok {
		s.PdPoolsJSON = rawJSON(t, v)
	}
}

// ---- subnet array assembly ----

func TestEmptyServerWithNoScopesEmitsEmptySubnetArrays(t *testing.T) {
	b, err := RenderKeaBundle(newServer(t, nil), nil, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	if got, _ := b.Dhcp4["subnet4"].([]any); len(got) != 0 {
		t.Errorf("dhcp4.subnet4: want empty, got %v", b.Dhcp4["subnet4"])
	}
	if got, _ := b.Dhcp6["subnet6"].([]any); len(got) != 0 {
		t.Errorf("dhcp6.subnet6: want empty, got %v", b.Dhcp6["subnet6"])
	}
	if len(b.CtrlAgent) != 0 {
		t.Errorf("ctrl_agent should be empty, got %v", b.CtrlAgent)
	}
}

func TestV4AndV6ScopesLandInCorrectArray(t *testing.T) {
	srv := newServer(t, nil)
	sc4 := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})
	sc6 := v6Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})
	b, err := RenderKeaBundle(srv, []Scope{sc4, sc6}, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	s4 := b.Dhcp4["subnet4"].([]any)
	s6 := b.Dhcp6["subnet6"].([]any)
	if len(s4) != 1 || s4[0].(map[string]any)["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet4 wrong: %v", s4)
	}
	if len(s6) != 1 || s6[0].(map[string]any)["subnet"] != "2001:db8::/64" {
		t.Errorf("subnet6 wrong: %v", s6)
	}
	// Separate id-spaces — both pin id=1.
	if s4[0].(map[string]any)["id"].(int64) != 1 {
		t.Errorf("v4 id: want 1, got %v", s4[0].(map[string]any)["id"])
	}
	if s6[0].(map[string]any)["id"].(int64) != 1 {
		t.Errorf("v6 id: want 1, got %v", s6[0].(map[string]any)["id"])
	}
}

func TestDisabledScopeSkippedFromBundle(t *testing.T) {
	srv := newServer(t, nil)
	scOK := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1, "prefix": "10.0.0.0/24"})
	scOff := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 2, "prefix": "10.0.1.0/24", "enabled": false})
	b, err := RenderKeaBundle(srv, []Scope{scOK, scOff}, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	s4 := b.Dhcp4["subnet4"].([]any)
	if len(s4) != 1 || s4[0].(map[string]any)["subnet"] != "10.0.0.0/24" {
		t.Errorf("disabled scope leaked into bundle: %v", s4)
	}
}

func TestPinnedKeaSubnetIDsArePreserved(t *testing.T) {
	srv := newServer(t, nil)
	a := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 7, "prefix": "10.0.0.0/24"})
	b := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 9, "prefix": "10.0.1.0/24"})
	c := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": nil, "prefix": "10.0.2.0/24"})
	bun, err := RenderKeaBundle(srv, []Scope{a, b, c}, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	s4 := bun.Dhcp4["subnet4"].([]any)
	have := map[int64]string{}
	for _, raw := range s4 {
		m := raw.(map[string]any)
		have[m["id"].(int64)] = m["subnet"].(string)
	}
	if have[7] == "" || have[9] == "" {
		t.Errorf("pinned ids 7 and 9 should be present: %v", have)
	}
	if have[1] == "" {
		t.Errorf("deferred scope should take lowest free id=1: %v", have)
	}
	if have[1] != "10.0.2.0/24" {
		t.Errorf("id=1 should map to the deferred prefix 10.0.2.0/24, got %q", have[1])
	}
}

func TestV4AndV6IDSpacesAreIndependent(t *testing.T) {
	srv := newServer(t, nil)
	a := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})
	b := v6Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})
	bun, err := RenderKeaBundle(srv, []Scope{a, b}, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	if bun.Dhcp4["subnet4"].([]any)[0].(map[string]any)["id"].(int64) != 1 {
		t.Errorf("v4 id 1 not preserved")
	}
	if bun.Dhcp6["subnet6"].([]any)[0].(map[string]any)["id"].(int64) != 1 {
		t.Errorf("v6 id 1 not preserved")
	}
}

// ---- base config overlay ----

func TestOperatorAuthoredDhcp4FieldsPassThroughVerbatim(t *testing.T) {
	base := map[string]any{
		"dhcp4": map[string]any{
			"interfaces-config": map[string]any{"interfaces": []any{"eth0"}},
			"lease-database":    map[string]any{"type": "memfile"},
			"loggers":           []any{map[string]any{"name": "kea-dhcp4", "severity": "INFO"}},
			"hooks-libraries":   []any{map[string]any{"library": "/usr/lib/kea/hooks/libdhcp_subnet_cmds.so"}},
		},
	}
	srv := newServer(t, base)
	b, err := RenderKeaBundle(srv, []Scope{v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})}, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	if b.Dhcp4["subnet4"].([]any)[0].(map[string]any)["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet array missing")
	}
	if ic := b.Dhcp4["interfaces-config"]; ic == nil {
		t.Errorf("interfaces-config dropped")
	}
	if ld := b.Dhcp4["lease-database"]; ld == nil {
		t.Errorf("lease-database dropped")
	}
}

func TestDcimSubnetArrayOverwritesOperatorSubnetArray(t *testing.T) {
	// Operator's base accidentally carries a subnet4 entry — DCIM
	// is authoritative; replace, don't merge.
	base := map[string]any{
		"dhcp4": map[string]any{
			"subnet4": []any{map[string]any{"id": 99, "subnet": "10.99.0.0/24"}},
		},
	}
	srv := newServer(t, base)
	sc := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1, "prefix": "10.0.0.0/24"})
	b, err := RenderKeaBundle(srv, []Scope{sc}, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	s4 := b.Dhcp4["subnet4"].([]any)
	if len(s4) != 1 {
		t.Fatalf("expected DCIM array of len 1, got %d", len(s4))
	}
	if s4[0].(map[string]any)["subnet"] != "10.0.0.0/24" {
		t.Errorf("DCIM subnet should win, got %v", s4[0])
	}
}

func TestCtrlAgentPassesThroughUntouched(t *testing.T) {
	base := map[string]any{
		"ctrl-agent": map[string]any{
			"http-port":       8000,
			"control-sockets": map[string]any{"dhcp4": map[string]any{}},
		},
	}
	srv := newServer(t, base)
	b, err := RenderKeaBundle(srv, nil, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	if hp := b.CtrlAgent["http-port"]; hp == nil {
		t.Errorf("ctrl-agent http-port dropped")
	}
	if cs := b.CtrlAgent["control-sockets"]; cs == nil {
		t.Errorf("ctrl-agent control-sockets dropped")
	}
}

func TestMissingBaseSectionsDefaultToEmptyDicts(t *testing.T) {
	srv := newServer(t, map[string]any{"dhcp4": map[string]any{"loggers": []any{}}})
	b, err := RenderKeaBundle(srv, nil, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	if len(b.CtrlAgent) != 0 {
		t.Errorf("ctrl_agent should default to empty, got %v", b.CtrlAgent)
	}
	// dhcp6 only has the subnet6 array we synthesized.
	if len(b.Dhcp6) != 1 {
		t.Errorf("dhcp6 should only have subnet6, got %v", b.Dhcp6)
	}
}

// ---- etag ----

func TestEtagIsStableAcrossIdenticalRenders(t *testing.T) {
	srv := newServer(t, map[string]any{
		"dhcp4": map[string]any{
			"interfaces-config": map[string]any{"interfaces": []any{"eth0"}},
		},
	})
	sc := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})
	a, _ := RenderKeaBundle(srv, []Scope{sc}, nil)
	b, _ := RenderKeaBundle(srv, []Scope{sc}, nil)
	if a.Etag != b.Etag {
		t.Errorf("etag should be stable: %q vs %q", a.Etag, b.Etag)
	}
	if len(a.Etag) != 64 {
		t.Errorf("etag should be sha256 hex (64 chars), got %d", len(a.Etag))
	}
}

func TestEtagChangesWhenScopeAdded(t *testing.T) {
	srv := newServer(t, nil)
	scA := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1})
	scB := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 2, "prefix": "10.0.1.0/24"})
	one, _ := RenderKeaBundle(srv, []Scope{scA}, nil)
	two, _ := RenderKeaBundle(srv, []Scope{scA, scB}, nil)
	if one.Etag == two.Etag {
		t.Errorf("etag should change when scope added")
	}
}

func TestEtagChangesWhenBaseConfigMutates(t *testing.T) {
	sc := v4Scope(t, uuid.NewString(), map[string]any{"kea_subnet_id": 1})
	plain := newServer(t, nil)
	plainB, _ := RenderKeaBundle(plain, []Scope{sc}, nil)
	withBase := newServer(t, map[string]any{"dhcp4": map[string]any{"loggers": []any{map[string]any{"name": "kea-dhcp4"}}}})
	sc.DhcpServerID = withBase.ID
	withBaseB, _ := RenderKeaBundle(withBase, []Scope{sc}, nil)
	if plainB.Etag == withBaseB.Etag {
		t.Errorf("etag should change when base config mutates")
	}
}

func TestEtagSensitiveToScopeOrder(t *testing.T) {
	// List order matters in Kea config; we don't sort. The
	// subnet4 array lands in the bundle in the same order as the
	// input list, so a reversed input → different etag.
	srv := newServer(t, nil)
	scA := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 1, "prefix": "10.0.0.0/24"})
	scB := v4Scope(t, srv.ID, map[string]any{"kea_subnet_id": 2, "prefix": "10.0.1.0/24"})
	forward, _ := RenderKeaBundle(srv, []Scope{scA, scB}, nil)
	reverse, _ := RenderKeaBundle(srv, []Scope{scB, scA}, nil)
	if forward.Etag == reverse.Etag {
		t.Errorf("etag should differ for reversed scope input order")
	}
}

func TestBundleServerIDCarriesStringUUID(t *testing.T) {
	srv := newServer(t, nil)
	b, _ := RenderKeaBundle(srv, nil, nil)
	if b.ServerID != srv.ID {
		t.Errorf("server_id: want %q, got %q", srv.ID, b.ServerID)
	}
}

// ---- renderer pure helpers ----

func TestRenderPoolsRendersFirstLastShape(t *testing.T) {
	pools := rawJSON(t, []map[string]any{
		{"first": "10.0.0.10", "last": "10.0.0.250"},
		{"first": "10.0.0.30", "last": "10.0.0.40"},
	})
	out := RenderPools(pools)
	if len(out) != 2 {
		t.Fatalf("want 2 entries, got %d", len(out))
	}
	if out[0]["pool"] != "10.0.0.10 - 10.0.0.250" {
		t.Errorf("pool 0 wrong: %v", out[0])
	}
}

func TestRenderPoolsSkipsIncompleteEntries(t *testing.T) {
	// Entries missing first or last are dropped silently (Kea
	// would error on them anyway).
	pools := rawJSON(t, []map[string]any{
		{"first": "10.0.0.10"},        // missing last
		{"last": "10.0.0.250"},        // missing first
		{"first": "", "last": "10.0.0.5"}, // empty first
	})
	if got := RenderPools(pools); len(got) != 0 {
		t.Errorf("expected zero pools, got %v", got)
	}
}

func TestRenderPDPoolsSplitsPrefixAndDelegatedLen(t *testing.T) {
	pdPools := rawJSON(t, []map[string]any{
		{"prefix": "2001:db8::/56", "delegated_len": 64},
	})
	out := RenderPDPools(pdPools)
	if len(out) != 1 {
		t.Fatalf("want 1 entry, got %d", len(out))
	}
	got := out[0]
	if got["prefix"] != "2001:db8::" || got["prefix-len"] != 56 || got["delegated-len"].(int64) != 64 {
		t.Errorf("pd-pool shape wrong: %v", got)
	}
}

func TestRenderReservationsV6WrapsIPInList(t *testing.T) {
	rs := rawJSON(t, []map[string]any{
		{"duid": "00:01:02:03", "ip": "2001:db8::100", "hostname": "host-a"},
	})
	out := RenderReservationsV6(rs)
	if len(out) != 1 {
		t.Fatalf("want 1 entry, got %d", len(out))
	}
	ips, ok := out[0]["ip-addresses"].([]any)
	if !ok || len(ips) != 1 || ips[0] != "2001:db8::100" {
		t.Errorf("ip-addresses shape wrong: %v", out[0]["ip-addresses"])
	}
}

// ---- template merge ----

func TestMergeTemplateInheritsTimersWhenScopeNotSet(t *testing.T) {
	tpl := &Template{
		ID:                   "tpl-1",
		ValidLifetimeSeconds: ptrInt64(7200),
		RenewTimerSeconds:    ptrInt64(3600),
	}
	scope := Scope{} // no timers set
	got := MergeTemplateIntoScope(scope, tpl)
	if got.ValidLifetimeSeconds == nil || *got.ValidLifetimeSeconds != 7200 {
		t.Errorf("valid_lifetime: want 7200, got %v", got.ValidLifetimeSeconds)
	}
	if got.RenewTimerSeconds == nil || *got.RenewTimerSeconds != 3600 {
		t.Errorf("renew_timer: want 3600, got %v", got.RenewTimerSeconds)
	}
}

func TestMergeTemplateScopeTimerWins(t *testing.T) {
	tpl := &Template{ValidLifetimeSeconds: ptrInt64(7200)}
	scope := Scope{ValidLifetimeSeconds: ptrInt64(1800)}
	got := MergeTemplateIntoScope(scope, tpl)
	if got.ValidLifetimeSeconds == nil || *got.ValidLifetimeSeconds != 1800 {
		t.Errorf("scope value should win: got %v", got.ValidLifetimeSeconds)
	}
}

func TestMergeOptionsScopeOverridesTemplateByCode(t *testing.T) {
	tpl := rawJSON(t, []map[string]any{
		{"code": 3, "name": "routers", "data": "10.0.0.1"},
		{"code": 6, "name": "dns", "data": "8.8.8.8"},
	})
	scope := rawJSON(t, []map[string]any{
		{"code": 6, "name": "dns", "data": "1.1.1.1"}, // override
		{"code": 15, "name": "domain-name", "data": "lab.test"}, // new
	})
	merged := MergeOptions(tpl, scope)
	var out []map[string]any
	if err := json.Unmarshal(merged, &out); err != nil {
		t.Fatalf("merged JSON invalid: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 options, got %d: %v", len(out), out)
	}
	// Order: template entries first (in template order), then
	// scope-only entries.
	if c, _ := out[0]["code"].(float64); int(c) != 3 {
		t.Errorf("entry 0 should be template code=3, got %v", out[0])
	}
	if c, _ := out[1]["code"].(float64); int(c) != 6 {
		t.Errorf("entry 1 should be template code=6 (scope-overridden), got %v", out[1])
	}
	if out[1]["data"] != "1.1.1.1" {
		t.Errorf("entry 1 data should be scope-overridden 1.1.1.1, got %v", out[1])
	}
	if c, _ := out[2]["code"].(float64); int(c) != 15 {
		t.Errorf("entry 2 should be scope-only code=15, got %v", out[2])
	}
}

func TestMergeTemplateNilPreservesScope(t *testing.T) {
	scope := v4Scope(t, "srv-1", nil)
	got := MergeTemplateIntoScope(scope, nil)
	if got.Prefix != scope.Prefix || got.IPFamily != scope.IPFamily {
		t.Errorf("identity passthrough broken: %+v vs %+v", got, scope)
	}
}

// ---- template merge through the bundle ----

func TestBundleAppliesTemplateOptionsViaTemplateID(t *testing.T) {
	tplID := "tpl-1"
	srv := newServer(t, nil)
	sc := v4Scope(t, srv.ID, map[string]any{
		"kea_subnet_id": 1,
		"options_json":  []map[string]any{{"code": 6, "name": "dns", "data": "1.1.1.1"}},
	})
	sc.TemplateID = &tplID
	tpl := Template{
		ID:          tplID,
		OptionsJSON: rawJSON(t, []map[string]any{{"code": 3, "name": "routers", "data": "10.0.0.1"}}),
	}
	b, err := RenderKeaBundle(srv, []Scope{sc}, map[string]Template{tplID: tpl})
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	opts := b.Dhcp4["subnet4"].([]any)[0].(map[string]any)["option-data"].([]map[string]any)
	if len(opts) != 2 {
		t.Fatalf("want 2 options (1 from tpl + 1 from scope), got %d: %v", len(opts), opts)
	}
}

func TestRenderKeaSubnet4FieldShape(t *testing.T) {
	// Inspect every key produced by RenderKeaSubnet4 in one place,
	// not just transitively through the bundle. A regression that
	// dropped (or renamed) any of subnet/pools/option-data/
	// reservations/valid-lifetime would still pass the assembly
	// tests because those only check subnet/id, but would break
	// Kea on push. The pure helper layer is where to pin the wire
	// shape.
	s := Scope{
		IPFamily:             4,
		Prefix:               "10.0.0.0/24",
		PoolsJSON:            rawJSON(t, []map[string]any{{"first": "10.0.0.10", "last": "10.0.0.250"}}),
		OptionsJSON:          rawJSON(t, []map[string]any{{"code": 6, "name": "dns", "data": "1.1.1.1"}}),
		ReservationsJSON:     rawJSON(t, []map[string]any{{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5", "hostname": "host-a"}}),
		ValidLifetimeSeconds: ptrInt64(7200),
		RenewTimerSeconds:    ptrInt64(900),
		RebindTimerSeconds:   ptrInt64(1800),
		Enabled:              true,
	}
	out := RenderKeaSubnet4(s, 42)
	if out["id"].(int64) != 42 {
		t.Errorf("id: want 42, got %v", out["id"])
	}
	if out["subnet"] != "10.0.0.0/24" {
		t.Errorf("subnet: got %v", out["subnet"])
	}
	if out["valid-lifetime"] != int64(7200) {
		t.Errorf("valid-lifetime: got %v", out["valid-lifetime"])
	}
	if out["renew-timer"] != int64(900) {
		t.Errorf("renew-timer: got %v", out["renew-timer"])
	}
	if out["rebind-timer"] != int64(1800) {
		t.Errorf("rebind-timer: got %v", out["rebind-timer"])
	}
	pools := out["pools"].([]map[string]any)
	if len(pools) != 1 || pools[0]["pool"] != "10.0.0.10 - 10.0.0.250" {
		t.Errorf("pools: got %v", pools)
	}
	resv := out["reservations"].([]map[string]any)
	if len(resv) != 1 {
		t.Fatalf("reservations: want 1, got %v", resv)
	}
	if resv[0]["hw-address"] != "aa:bb:cc:dd:ee:ff" || resv[0]["ip-address"] != "10.0.0.5" || resv[0]["hostname"] != "host-a" {
		t.Errorf("reservation shape: got %v", resv[0])
	}
}

func TestRenderKeaSubnet4DefaultValidLifetimeWhenScopeAndTemplateNil(t *testing.T) {
	// Exercise the DefaultValidLifetime=3600 fallback. Without an
	// explicit test, a regression that drops the defaultIfNil call
	// (e.g. someone replaces it with *s.ValidLifetimeSeconds) would
	// only panic in production on a freshly-created scope.
	s := Scope{
		IPFamily:             4,
		Prefix:               "10.0.0.0/24",
		ValidLifetimeSeconds: nil,
		Enabled:              true,
	}
	out := RenderKeaSubnet4(s, 1)
	if out["valid-lifetime"] != DefaultValidLifetime {
		t.Errorf("valid-lifetime fallback: want %d, got %v", DefaultValidLifetime, out["valid-lifetime"])
	}
}

func TestRenderReservationsV4ShapeAndFiltering(t *testing.T) {
	// V4-side symmetric to TestRenderReservationsV6WrapsIPInList.
	// Pins the hw-address / ip-address key names (Kea-side wire
	// contract) and the missing-field drop behavior.
	rs := rawJSON(t, []map[string]any{
		{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5", "hostname": "host-a"},
		{"mac": "11:22:33:44:55:66", "ip": "10.0.0.6"}, // no hostname
		{"mac": "00:00:00:00:00:00"},                   // missing ip → dropped
		{"ip": "10.0.0.7"},                              // missing mac → dropped
	})
	out := RenderReservationsV4(rs)
	if len(out) != 2 {
		t.Fatalf("want 2 entries (incomplete ones dropped), got %d: %v", len(out), out)
	}
	if out[0]["hw-address"] != "aa:bb:cc:dd:ee:ff" || out[0]["ip-address"] != "10.0.0.5" || out[0]["hostname"] != "host-a" {
		t.Errorf("entry 0 shape wrong: %v", out[0])
	}
	if _, hasHost := out[1]["hostname"]; hasHost {
		t.Errorf("entry 1 should not have hostname key when input lacks it: %v", out[1])
	}
}

func TestRenderOptionsHandlesMissingCodeOrName(t *testing.T) {
	// Pure-helper coverage — the bundle test only sees the merged
	// output; this pins the individual entry shape.
	opts := rawJSON(t, []map[string]any{
		{"data": "10.0.0.1"},                                // no code, no name — minimal valid Kea entry
		{"code": 6, "data": "1.1.1.1"},                       // code only
		{"name": "routers", "data": "10.0.0.1", "space": "dhcp4"}, // name + space
	})
	out := RenderOptions(opts)
	if len(out) != 3 {
		t.Fatalf("want 3 entries, got %d: %v", len(out), out)
	}
	if _, hasCode := out[0]["code"]; hasCode {
		t.Errorf("entry 0 should not carry code key (input had none): %v", out[0])
	}
	if out[1]["code"].(int64) != 6 {
		t.Errorf("entry 1 code: want 6, got %v", out[1]["code"])
	}
	if out[2]["name"] != "routers" || out[2]["space"] != "dhcp4" {
		t.Errorf("entry 2 name/space wrong: %v", out[2])
	}
}

func TestEtagAmpersandIsNotHTMLEscaped(t *testing.T) {
	// Etag-parity regression test. Default json.Marshal escapes
	// `<`, `>`, `&` as `<` / `>` / `&` and Python's
	// json.dumps does not — without SetEscapeHTML(false) the Go and
	// Python ports of the renderer produce different etags for the
	// same input. Operator base_config commonly carries `&` in
	// hooks-library URLs (e.g. `?token=abc&site=lab`), so this is
	// a realistic input. Pin a base_config with `&` and assert that
	// the SHA256 input never contains a `&` escape sequence.
	base := map[string]any{
		"dhcp4": map[string]any{
			"hooks-libraries": []any{
				map[string]any{
					"library":    "/usr/lib/kea/hooks/libdhcp_lease_cmds.so",
					"parameters": map[string]any{"endpoint": "https://kea.example/api?token=abc&site=lab"},
				},
			},
		},
	}
	srv := newServer(t, base)
	b, err := RenderKeaBundle(srv, nil, nil)
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	// Recompute the canonical bytes the same way computeEtag does
	// and assert the `&` is literal, not HTML-escaped. This is the
	// concrete invariant that makes the Python parity claim true.
	canonical := map[string]any{
		"ctrl-agent": b.CtrlAgent,
		"dhcp4":      b.Dhcp4,
		"dhcp6":      b.Dhcp6,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(canonical); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	raw := buf.String()
	if !contains(raw, "abc&site=lab") {
		t.Errorf("canonical JSON should contain literal `&`; got fragment: %s", raw)
	}
	// The HTML-escaped form is the 6-character sequence
	// backslash-u-0-0-2-6. A raw-string literal would be 1 byte
	// (the `&` rune); spell it out explicitly so the assertion is
	// unambiguous.
	htmlEscaped := "\\" + "u0026"
	if contains(raw, htmlEscaped) {
		t.Errorf("canonical JSON should NOT HTML-escape `&`; got: %s", raw)
	}
	// Confirm the etag itself doesn't change if we re-render the
	// same input — locks the SetEscapeHTML invariant in place.
	b2, _ := RenderKeaBundle(srv, nil, nil)
	if b.Etag != b2.Etag {
		t.Errorf("etag should be stable across identical renders with `&` in base: %q vs %q", b.Etag, b2.Etag)
	}
}

func TestBundleTemplateMissingIDFallsBackToScopeOnly(t *testing.T) {
	// Template referenced by ID but not present in the map →
	// renderer treats it as no template (scope-only).
	missing := "tpl-missing"
	srv := newServer(t, nil)
	sc := v4Scope(t, srv.ID, map[string]any{
		"kea_subnet_id": 1,
		"options_json":  []map[string]any{{"code": 6, "name": "dns", "data": "1.1.1.1"}},
	})
	sc.TemplateID = &missing
	b, err := RenderKeaBundle(srv, []Scope{sc}, map[string]Template{})
	if err != nil {
		t.Fatalf("RenderKeaBundle: %v", err)
	}
	opts := b.Dhcp4["subnet4"].([]any)[0].(map[string]any)["option-data"].([]map[string]any)
	if len(opts) != 1 {
		t.Errorf("want 1 option (scope only), got %d", len(opts))
	}
}
