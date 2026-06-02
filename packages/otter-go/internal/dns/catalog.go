// RFC 9432 catalog zone rendering (PR 24 — first of the DNS bundle
// port sequence, mirroring DHCP bundle PRs #216-#219). Pure function:
// no DB calls, no I/O. Caller hands in the catalog apex name + the
// (filtered, frozen-elided) members + optional primaries IPs, gets
// back a BIND-format catalog zone file.
//
// Output is byte-equivalent with Python's render_catalog_zone in
// packages/otter/src/dcim/services/dns.py — same line order, same
// tab separators, same SOA layout, same property-record namespaces
// (group / epoch / primaries). Tests pin the format.
package dns

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// RenderCatalogZone emits an RFC 9432 §4 catalog zone in BIND text.
// Members are sorted by name for deterministic etag (frozen zones
// should be elided by the caller; bundle-assembly handles that).
//
// Each member emits:
//   - PTR  under `<zone-id-hex>.zones.<apex>` to the member zone apex
//   - TXT  under `group.<zone-id-hex>.zones.<apex>` carrying the
//     fabric/kind group label
//   - TXT  under `epoch.<zone-id-hex>.zones.<apex>` carrying the
//     member's updated_at epoch (RFC 9432 §5.2 lets consumers
//     detect per-member changes between catalog serial bumps)
//   - When `primaries` is non-empty, A/AAAA `primaries.<id>.zones`
//     records per RFC 9432 §4.2.3 so BIND 9.20+ can AXFR.
//
// `serial` controls the SOA serial. Zero means "auto" → max(epoch)
// across members, falling back to 1 when empty (matches Python's
// `int(max(..., default=0)) or 1`). Tests pin a literal for
// reproducibility.
//
// `defaultTTL` controls $TTL and SOA minimum. Zero defaults to
// 3600 to match Python's keyword default.
func RenderCatalogZone(
	catalogName string,
	members []dbq.DnsZone,
	defaultTTL int32,
	serial int64,
	primaries []string,
) string {
	if defaultTTL == 0 {
		defaultTTL = 3600
	}
	apex := strings.TrimRight(catalogName, ".") + "."

	// Sort by lower-cased name to match Python's
	// `sorted(members, key=lambda z: z.name.lower())`.
	ordered := append([]dbq.DnsZone(nil), members...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].Name) < strings.ToLower(ordered[j].Name)
	})

	if serial == 0 {
		serial = autoCatalogSerial(ordered)
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("$ORIGIN %s\n", apex))
	b.WriteString(fmt.Sprintf("$TTL %d\n", defaultTTL))
	b.WriteString(fmt.Sprintf("@\tIN\tSOA\tinvalid. hostmaster.%s (\n", apex))
	b.WriteString(fmt.Sprintf("\t\t\t%d\t; serial\n", serial))
	b.WriteString("\t\t\t3600\t; refresh\n")
	b.WriteString("\t\t\t600\t; retry\n")
	b.WriteString("\t\t\t604800\t; expire\n")
	b.WriteString(fmt.Sprintf("\t\t\t%d)\t; minimum\n", defaultTTL))
	// RFC 9432 §4.1: required NS RR. Deliberately `invalid.` because
	// catalog zones aren't reachable via the public DNS hierarchy.
	b.WriteString("@\tIN\tNS\tinvalid.\n")
	// RFC 9432 mandates "2". Consumers reject any other value.
	b.WriteString("version\tIN\tTXT\t\"2\"\n")
	b.WriteString("\n")

	for _, z := range ordered {
		b.WriteString(fmt.Sprintf("%s.zones\tIN\tPTR\t%s\n",
			zoneIDHex(z.ID.String()),
			memberApex(z.Name)))
	}

	if len(ordered) > 0 {
		b.WriteString("\n")
		b.WriteString("; --- per-member properties (RFC 9432 §5) ---\n")
		for _, z := range ordered {
			for _, line := range catalogMemberPropertyLines(z, primaries) {
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
	}
	// Trailing-newline parity with Python: Python uses
	// `"\n".join(lines)` where lines ends with `""` so the join
	// produces exactly ONE trailing `\n` for non-empty catalogs and
	// TWO for empty catalogs. The blank-line WriteString after
	// version_txt above gives us that second newline in the empty
	// case; no further trailing newline is needed in either path.
	return b.String()
}

// catalogMemberPropertyLines mirrors Python's
// _catalog_member_property_lines: group + epoch TXT plus optional
// primaries A/AAAA records.
func catalogMemberPropertyLines(z dbq.DnsZone, primaries []string) []string {
	id := zoneIDHex(z.ID.String())
	group := catalogGroupFor(z)
	// Zero time guard: Go's zero time.Time has Unix() = -62135596800,
	// which would render as a syntactically valid but semantically
	// garbage TXT value. Python would raise on `None.timestamp()`
	// for a null updated_at; mirror the "loud failure" intent by
	// emitting 0 (RFC 9432 consumers MAY treat 0 as "unknown") so
	// the bundle stays well-formed and the operator can detect the
	// problem from the TXT value rather than from BIND parse errors.
	var epoch int64
	if !z.UpdatedAt.IsZero() {
		epoch = z.UpdatedAt.Unix()
	}
	out := []string{
		fmt.Sprintf("group.%s.zones\tIN\tTXT\t\"%s\"", id, group),
		fmt.Sprintf("epoch.%s.zones\tIN\tTXT\t\"%d\"", id, epoch),
	}
	for _, raw := range primaries {
		// Strip any /prefix-len so the operator can pass CIDRs straight
		// from the IPAM SELECT result.
		bare := strings.TrimSpace(raw)
		if i := strings.IndexByte(bare, '/'); i >= 0 {
			bare = bare[:i]
		}
		ip := net.ParseIP(bare)
		if ip == nil {
			continue
		}
		// Decide AAAA-vs-A by the INPUT STRING form, not the parsed
		// bytes — net.ParseIP returns a 16-byte form even for IPv4
		// literals (so .To4() != nil even for `::ffff:1.2.3.4`).
		// Python's ipaddress.ip_address keeps the version that
		// matches the input syntax, so an IPv4-mapped IPv6 like
		// `::ffff:1.2.3.4` renders as AAAA in Python; mirror that
		// by treating any input containing `:` as IPv6.
		rtype := "A"
		rendered := ip.String()
		if strings.ContainsRune(bare, ':') {
			rtype = "AAAA"
			rendered = bare
		}
		out = append(out, fmt.Sprintf("primaries.%s.zones\tIN\t%s\t%s",
			id, rtype, rendered))
	}
	return out
}

// catalogGroupFor defaults to the zone's `kind` (apex / site /
// reverse) — same axis DCIM organizes zones along.
func catalogGroupFor(z dbq.DnsZone) string {
	return z.Kind
}

// zoneIDHex strips the dashes from a UUID string so the result
// matches Python's `uuid.UUID.hex`. Python uses .hex (no dashes)
// for catalog member subdomain labels; sticking with dashes would
// produce a different etag on every cutover.
func zoneIDHex(id string) string {
	return strings.ReplaceAll(id, "-", "")
}

// memberApex normalizes a zone name to its fully-qualified form
// for use as PTR target.
func memberApex(name string) string {
	return strings.TrimRight(name, ".") + "."
}

// autoCatalogSerial picks the max(epoch) across members, defaulting
// to 1 when there are no members (mirrors Python's
// `int(max(...)) or 1` short-circuit on empty + zero).
func autoCatalogSerial(members []dbq.DnsZone) int64 {
	if len(members) == 0 {
		return 1
	}
	var maxTS time.Time
	for _, z := range members {
		if z.UpdatedAt.After(maxTS) {
			maxTS = z.UpdatedAt
		}
	}
	s := maxTS.Unix()
	if s <= 0 {
		return 1
	}
	return s
}
