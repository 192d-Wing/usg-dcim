package dns

import (
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

// ===== viewExpr =====

func TestViewExpr_Empty(t *testing.T) {
	if got := viewExpr(nil); got != "false" {
		t.Errorf("empty cidrs → want %q got %q", "false", got)
	}
}

func TestViewExpr_DropsEmptyEntries(t *testing.T) {
	got := viewExpr([]string{"", "10.0.0.0/24", ""})
	want := "incidr(client_ip, '10.0.0.0/24')"
	if got != want {
		t.Errorf("want %q got %q", want, got)
	}
}

func TestViewExpr_ORsMultiple(t *testing.T) {
	got := viewExpr([]string{"10.0.0.0/8", "2001:db8::/32"})
	want := "incidr(client_ip, '10.0.0.0/8') || incidr(client_ip, '2001:db8::/32')"
	if got != want {
		t.Errorf("want %q got %q", want, got)
	}
}

// ===== zoneViewFilename =====

func TestZoneViewFilename_NilDefault(t *testing.T) {
	if got := zoneViewFilename("example.com.", nil); got != "example.com..zone" {
		t.Errorf("got %q", got)
	}
}

func TestZoneViewFilename_PerView(t *testing.T) {
	v := "internal"
	if got := zoneViewFilename("example.com.", &v); got != "example.com..view-internal.zone" {
		t.Errorf("got %q", got)
	}
}

// ===== renderAxfrAclBlock =====

func TestRenderAxfrAclBlock_NilEmpty(t *testing.T) {
	if got := renderAxfrAclBlock(nil); got != "" {
		t.Errorf("nil ACL should emit nothing; got %q", got)
	}
	if got := renderAxfrAclBlock([]string{}); got != "" {
		t.Errorf("empty ACL should emit nothing; got %q", got)
	}
}

func TestRenderAxfrAclBlock_Golden(t *testing.T) {
	got := renderAxfrAclBlock([]string{"10.0.0.0/8", "192.168.0.0/16"})
	want := "    acl {\n" +
		"        allow type AXFR net 10.0.0.0/8 192.168.0.0/16\n" +
		"        block type AXFR\n" +
		"    }\n" +
		"    transfer {\n" +
		"        to *\n" +
		"    }\n"
	if got != want {
		t.Errorf("AXFR ACL golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// ===== renderSigningBlock =====

// Zone without keys → empty block. Without this, the renderer
// would emit a malformed dnssec block referencing no key files.
func TestRenderSigningBlock_NoKeysDirReturnsEmpty(t *testing.T) {
	if got := renderSigningBlock("z.example.", "/zones", "", []string{"ksk.key"}, nil); got != "" {
		t.Errorf("no keys_base should emit nothing; got %q", got)
	}
}

func TestRenderSigningBlock_NoKeyBasenamesReturnsEmpty(t *testing.T) {
	if got := renderSigningBlock("z.example.", "/zones", "/keys", nil, nil); got != "" {
		t.Errorf("no key basenames should emit nothing; got %q", got)
	}
}

// Signed zone, NSEC mode → `dnssec { key file ... }` block.
func TestRenderSigningBlock_DnssecNsecGolden(t *testing.T) {
	got := renderSigningBlock("z.example.", "/zones", "/keys",
		[]string{"Kz.example.+013+12345.key", "Kz.example.+013+54321.key"}, nil)
	want := "    dnssec {\n" +
		"        key file /keys/Kz.example.+013+12345.key\n" +
		"        key file /keys/Kz.example.+013+54321.key\n" +
		"    }\n"
	if got != want {
		t.Errorf("NSEC dnssec block golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// Signed + NSEC3 → `nsec3sign { ... }` block with salt+iterations+optout.
func TestRenderSigningBlock_Nsec3Golden(t *testing.T) {
	params := &Nsec3Params{Salt: "abcd", Iterations: 5, OptOut: true}
	got := renderSigningBlock("z.example.", "/zones", "/keys",
		[]string{"Kz.example.+013+12345.key"}, params)
	want := "    nsec3sign {\n" +
		"        key file /keys/Kz.example.+013+12345.key\n" +
		"        zone file /zones/z.example..zone\n" +
		"        salt \"abcd\"\n" +
		"        iterations 5\n" +
		"        opt_out\n" +
		"    }\n"
	if got != want {
		t.Errorf("NSEC3 block golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// NSEC3 with empty salt (RFC 9276 recommended default) and no opt-out.
func TestRenderSigningBlock_Nsec3EmptySaltNoOptOut(t *testing.T) {
	params := &Nsec3Params{Salt: "", Iterations: 0, OptOut: false}
	got := renderSigningBlock("z.example.", "/zones", "/keys",
		[]string{"Kz.example.+013+12345.key"}, params)
	if !strings.Contains(got, "salt \"\"\n") {
		t.Errorf("empty salt should render explicit \"\" so operators see it; got %q", got)
	}
	if !strings.Contains(got, "iterations 0\n") {
		t.Errorf("missing iterations 0; got %q", got)
	}
	if strings.Contains(got, "opt_out") {
		t.Errorf("opt_out should NOT appear when false; got %q", got)
	}
}

// Key basenames sorted (deterministic etag).
func TestRenderSigningBlock_KeysSorted(t *testing.T) {
	got := renderSigningBlock("z.example.", "/zones", "/keys",
		[]string{"Z-second.key", "A-first.key"}, nil)
	idxA := strings.Index(got, "A-first.key")
	idxZ := strings.Index(got, "Z-second.key")
	if idxA < 0 || idxZ < 0 || idxA >= idxZ {
		t.Errorf("keys not sorted: A=%d Z=%d in %q", idxA, idxZ, got)
	}
}

// ===== RenderCorefileAuth =====

func TestRenderCorefileAuth_SortsZones(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames: []string{"z.example.", "a.example.", "m.example."},
		ZonesDir:  "/zones",
	})
	idxA := strings.Index(got, "a.example.:53")
	idxM := strings.Index(got, "m.example.:53")
	idxZ := strings.Index(got, "z.example.:53")
	if idxA < 0 || idxM < 0 || idxZ < 0 || !(idxA < idxM && idxM < idxZ) {
		t.Errorf("zones not sorted in Corefile: a=%d m=%d z=%d", idxA, idxM, idxZ)
	}
}

func TestRenderCorefileAuth_TrimsTrailingSlash(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames: []string{"z.example."},
		ZonesDir:  "/zones/",
	})
	if !strings.Contains(got, "file /zones/z.example..zone") {
		t.Errorf("ZonesDir trailing slash not stripped; got %q", got)
	}
}

// Golden Corefile for a minimal single-zone case — no DNSSEC, no
// views, no AXFR, no dnstap. Locks the basic block shape so any
// formatting drift fails loudly.
func TestRenderCorefileAuth_SingleZoneGolden(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames: []string{"example.com."},
		ZonesDir:  "/zones",
	})
	want := "example.com.:53 {\n" +
		"    file /zones/example.com..zone\n" +
		"    log\n" +
		"    errors\n" +
		"    prometheus :9153\n" +
		"    health :8080\n" +
		"}\n"
	if got != want {
		t.Errorf("single-zone golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// Golden Corefile with two zones — pins the two-block separator
// (`\n\n` between blocks, single `\n` at end). This catches the
// kind of trailing-newline divergence the catalog renderer had.
func TestRenderCorefileAuth_TwoZoneGolden(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames: []string{"a.example.", "b.example."},
		ZonesDir:  "/zones",
	})
	want := "a.example.:53 {\n" +
		"    file /zones/a.example..zone\n" +
		"    log\n" +
		"    errors\n" +
		"    prometheus :9153\n" +
		"    health :8080\n" +
		"}\n\n" +
		"b.example.:53 {\n" +
		"    file /zones/b.example..zone\n" +
		"    log\n" +
		"    errors\n" +
		"    prometheus :9153\n" +
		"    health :8080\n" +
		"}\n"
	if got != want {
		t.Errorf("two-zone golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// Zone with DNSSEC keys (NSEC mode) + dnstap socket.
func TestRenderCorefileAuth_SignedWithDnstapGolden(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames:        []string{"z.example."},
		ZonesDir:         "/zones",
		KeysDir:          sp("/keys"),
		DnssecKeysByZone: map[string][]string{"z.example.": {"Kz.example.+013+12345.key"}},
		DnstapSocket:     sp("/run/dnstap.sock"),
	})
	want := "z.example.:53 {\n" +
		"    file /zones/z.example..zone\n" +
		"    dnssec {\n" +
		"        key file /keys/Kz.example.+013+12345.key\n" +
		"    }\n" +
		"    dnstap /run/dnstap.sock full\n" +
		"    log\n" +
		"    errors\n" +
		"    prometheus :9153\n" +
		"    health :8080\n" +
		"}\n"
	if got != want {
		t.Errorf("signed+dnstap golden mismatch\nwant %q\ngot  %q", want, got)
	}
}

// Zone with split-horizon views — one view block per CIDR set, then
// a fallthrough default. Pins the view-block layout (Python's
// expectation: view-blocks are first, default block is LAST).
func TestRenderCorefileAuth_ViewsThenDefault(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames: []string{"z.example."},
		ZonesDir:  "/zones",
		ViewsByZone: map[string][]ViewConfig{
			"z.example.": {
				{Name: "internal", CIDRs: []string{"10.0.0.0/8"}},
			},
		},
	})
	wantView := "z.example.:53 {\n" +
		"    view internal {\n" +
		"        expr incidr(client_ip, '10.0.0.0/8')\n" +
		"    }\n" +
		"    file /zones/z.example..view-internal.zone\n" +
		"    log\n" +
		"    errors\n" +
		"}"
	if !strings.Contains(got, wantView) {
		t.Errorf("view block missing or wrong shape:\nwant subset:\n%s\ngot:\n%s", wantView, got)
	}
	// Default block follows the view block (prometheus is only on
	// the default block to avoid double-registering).
	idxView := strings.Index(got, "view internal")
	idxDefault := strings.Index(got, "prometheus :9153")
	if idxView < 0 || idxDefault < 0 || idxDefault < idxView {
		t.Errorf("default block must come after view block: view=%d default=%d", idxView, idxDefault)
	}
}

// AXFR ACL emitted on the default block when transferAclByZone is
// set. Pins both that the ACL appears in the right block (default,
// not view) and that the directive order matches Python.
func TestRenderCorefileAuth_AxfrAclOnDefaultBlock(t *testing.T) {
	got := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames:         []string{"catalog.example."},
		ZonesDir:          "/zones",
		TransferAclByZone: map[string][]string{"catalog.example.": {"10.0.0.0/8"}},
	})
	if !strings.Contains(got, "acl {\n        allow type AXFR net 10.0.0.0/8\n") {
		t.Errorf("missing AXFR ACL block; got %q", got)
	}
	// ACL precedes log/errors/prometheus in the block. Look for
	// `    log\n` (4-space-indented + newline) to avoid matching
	// the substring "log" inside the zone name "catalog.example.".
	idxAcl := strings.Index(got, "acl {")
	idxLog := strings.Index(got, "    log\n")
	if idxAcl < 0 || idxLog < 0 || idxAcl >= idxLog {
		t.Errorf("AXFR ACL must precede log directive: acl=%d log=%d", idxAcl, idxLog)
	}
}

// Determinism: same input → same output across calls. Required for
// etag stability.
func TestRenderCorefileAuth_Deterministic(t *testing.T) {
	in := CorefileAuthInput{
		ZoneNames:        []string{"a.example.", "b.example."},
		ZonesDir:         "/zones",
		KeysDir:          sp("/keys"),
		DnssecKeysByZone: map[string][]string{"a.example.": {"Ka.+013+1.key"}},
	}
	out1 := RenderCorefileAuth(in)
	out2 := RenderCorefileAuth(in)
	if out1 != out2 {
		t.Error("two calls produced different output (etag would flap)")
	}
}
