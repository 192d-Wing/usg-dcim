// Authoritative CoreDNS Corefile renderer (PR 25 — DNS bundle 2/N).
// Pure function: no DB, no HTTP, no I/O. Caller hands in the zone
// names + per-zone DNSSEC keys + NSEC3 params + split-horizon views
// + AXFR ACLs, gets back the Corefile text the collector writes to
// disk and CoreDNS loads at boot.
//
// Byte-equivalent with Python's render_corefile_auth in
// services/dns.py L1013. Locked by golden tests.
package dns

import (
	"fmt"
	"sort"
	"strings"
)

// Nsec3Params carries the per-zone NSEC3 mode parameters. When the
// caller stores a non-nil value in CorefileAuthInput.Nsec3ParamsByZone
// for a given zone name, the renderer emits the `nsec3sign` block
// instead of the upstream `dnssec` block. salt may be empty (RFC 9276
// recommended default); iterations and opt-out come from the zone's
// nsec3_* columns.
type Nsec3Params struct {
	Salt       string
	Iterations int32
	OptOut     bool
}

// ViewConfig describes one split-horizon view bound to a zone. The
// per-zone slice is rendered in iteration order — operators encode
// view priority by ordering (narrowest first wins).
type ViewConfig struct {
	Name  string
	CIDRs []string
}

// CorefileAuthInput bundles the inputs to RenderCorefileAuth. Using
// a struct keeps the public surface stable across follow-up PRs
// that wire additional per-zone knobs.
type CorefileAuthInput struct {
	// ZoneNames is the apex name of each zone served by this
	// Corefile. Sorted by the renderer for deterministic etag.
	ZoneNames []string
	// ZonesDir is the absolute path on the CoreDNS container's
	// filesystem where the collector writes zone files. Trailing
	// slashes are trimmed.
	ZonesDir string
	// KeysDir is the absolute path to the DNSSEC key directory.
	// nil disables signing-block emission even when KeyBasenames
	// has entries; non-nil + key entries triggers the dnssec/
	// nsec3sign block on that zone.
	KeysDir              *string
	DnssecKeysByZone     map[string][]string
	Nsec3ParamsByZone    map[string]*Nsec3Params
	ViewsByZone          map[string][]ViewConfig
	// DnstapSocket emits a `dnstap <path> full` directive on the
	// default (non-view) block of every zone. nil/empty omits the
	// directive.
	DnstapSocket         *string
	TransferAclByZone    map[string][]string
}

// RenderCorefileAuth emits the Corefile text. Sort the zone-name
// list before iterating so the output is deterministic regardless
// of input order — caller-side preservation isn't enough since the
// map iteration in Go is randomized.
func RenderCorefileAuth(in CorefileAuthInput) string {
	base := strings.TrimRight(in.ZonesDir, "/")
	var keysBase string
	if in.KeysDir != nil {
		keysBase = strings.TrimRight(*in.KeysDir, "/")
	}
	dnstap := ""
	if in.DnstapSocket != nil {
		dnstap = *in.DnstapSocket
	}

	// Defensive nil-map handling — callers may pass nil for the
	// per-zone maps when no zone has the feature configured.
	dnssecMap := in.DnssecKeysByZone
	nsec3Map := in.Nsec3ParamsByZone
	viewsMap := in.ViewsByZone
	transferMap := in.TransferAclByZone

	ordered := append([]string(nil), in.ZoneNames...)
	sort.Strings(ordered)

	var blocks []string
	for _, name := range ordered {
		signing := renderSigningBlock(
			name, base, keysBase,
			dnssecMap[name],
			nsec3Map[name],
		)
		transferBlock := renderAxfrAclBlock(transferMap[name])

		// One block per split-horizon view, in caller-encoded
		// priority order (narrowest CIDRs first wins).
		for _, view := range viewsMap[name] {
			file := zoneViewFilename(name, &view.Name)
			blocks = append(blocks,
				fmt.Sprintf("%s:53 {\n", name)+
					fmt.Sprintf("    view %s {\n", view.Name)+
					fmt.Sprintf("        expr %s\n", viewExpr(view.CIDRs))+
					"    }\n"+
					fmt.Sprintf("    file %s/%s\n", base, file)+
					signing+
					"    log\n"+
					"    errors\n"+
					"}",
			)
		}

		// Default / fallthrough block — always last so the
		// view-scoped blocks above win when their expr matches.
		// Only this block carries prometheus + health so we don't
		// double-register them across views.
		dnstapLine := ""
		if dnstap != "" {
			dnstapLine = fmt.Sprintf("    dnstap %s full\n", dnstap)
		}
		blocks = append(blocks,
			fmt.Sprintf("%s:53 {\n", name)+
				fmt.Sprintf("    file %s/%s.zone\n", base, name)+
				signing+
				dnstapLine+
				transferBlock+
				"    log\n"+
				"    errors\n"+
				"    prometheus :9153\n"+
				"    health :8080\n"+
				"}",
		)
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// renderSigningBlock emits the per-zone DNSSEC directive — either the
// upstream `dnssec` plugin (NSEC) or our custom `nsec3sign` (NSEC3
// with on-the-fly signing). Returns "" when the zone isn't signed.
func renderSigningBlock(
	zoneName, base, keysBase string,
	keyBasenames []string,
	nsec3 *Nsec3Params,
) string {
	if keysBase == "" || len(keyBasenames) == 0 {
		return ""
	}
	sortedKeys := append([]string(nil), keyBasenames...)
	sort.Strings(sortedKeys)
	var keyLines []string
	for _, kb := range sortedKeys {
		keyLines = append(keyLines, fmt.Sprintf("        key file %s/%s", keysBase, kb))
	}
	keyBlock := strings.Join(keyLines, "\n")

	if nsec3 == nil {
		return fmt.Sprintf("    dnssec {\n%s\n    }\n", keyBlock)
	}

	// NSEC3 path. The duplicate zone-file path (also in the parent
	// `file` directive) is deliberate — see the coredns-nsec3sign
	// README for the coupling-tradeoff write-up.
	lines := []string{
		"    nsec3sign {",
		keyBlock,
		fmt.Sprintf("        zone file %s/%s.zone", base, zoneName),
		// Empty salt is the RFC 9276 recommended default; render
		// the literal "" so an operator scanning the Corefile can
		// see it explicitly rather than guessing at an absent
		// directive.
		fmt.Sprintf("        salt \"%s\"", nsec3.Salt),
		fmt.Sprintf("        iterations %d", nsec3.Iterations),
	}
	if nsec3.OptOut {
		lines = append(lines, "        opt_out")
	}
	lines = append(lines, "    }")
	return strings.Join(lines, "\n") + "\n"
}

// renderAxfrAclBlock emits the `acl { ... }` + `transfer { ... }`
// pair gating zone AXFR. CoreDNS's `transfer` plugin doesn't accept
// CIDRs; route the network gate through `acl` (which does) and open
// the transfer machinery itself with `to *`. Empty/nil ACL emits
// nothing — the transfer plugin's default is the closed posture.
func renderAxfrAclBlock(acl []string) string {
	if len(acl) == 0 {
		return ""
	}
	nets := strings.Join(acl, " ")
	return "    acl {\n" +
		fmt.Sprintf("        allow type AXFR net %s\n", nets) +
		"        block type AXFR\n" +
		"    }\n" +
		"    transfer {\n" +
		"        to *\n" +
		"    }\n"
}

// viewExpr composes the expr-lang predicate for CoreDNS's view
// plugin. ORing incidr(client_ip, '<cidr>') per CIDR gives the
// operator's intent. Empty input collapses to literal `false` so
// the view never matches but the Corefile still parses.
func viewExpr(cidrs []string) string {
	var parts []string
	for _, c := range cidrs {
		if c == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("incidr(client_ip, '%s')", c))
	}
	if len(parts) == 0 {
		return "false"
	}
	return strings.Join(parts, " || ")
}

// zoneViewFilename — default view (nil viewName) keeps the legacy
// `<zone>.zone` filename so existing operators don't see churn;
// per-view zones go to `<zone>.view-<name>.zone`.
func zoneViewFilename(zoneName string, viewName *string) string {
	if viewName == nil {
		return fmt.Sprintf("%s.zone", zoneName)
	}
	return fmt.Sprintf("%s.view-%s.zone", zoneName, *viewName)
}
