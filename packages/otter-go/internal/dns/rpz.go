// RPZ zone renderer (PR 34 — recursive bundle 1/N). Pure function:
// no DB, no I/O. Mirrors Python's render_rpz_zone at
// services/dns.py L1283.
//
// RPZ-aware resolvers (BIND, Unbound, recent Hickory builds) consume
// the rendered zone as a response policy; non-RPZ resolvers see a
// normal zone and ignore the unexpected owners. Block actions emit
// `<pattern> CNAME .` (the standard NXDOMAIN-equivalent); sinkhole
// actions emit `<pattern> A <sink_ipv4>` / `AAAA <sink_ipv6>`.
//
// Caller-supplied `now` keeps the renderer pure — the serial is
// derived from it so tests can pin a literal value. Production
// callers pass time.Now().UTC().
package dns

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// RPZInput bundles render_rpz_zone's arguments.
type RPZInput struct {
	RpzZoneName string
	Action      string // "block" or "sinkhole"
	Patterns    []string
	SinkIPv4    string // empty disables A emission
	SinkIPv6    string // empty disables AAAA emission
	DefaultTTL  int32  // 0 → 60 (Python's keyword default)
	Now         time.Time
}

// RenderRPZZone emits one RPZ-format zone file. Returns empty
// string with no error when there's nothing renderable (no
// patterns, or sinkhole with neither sink IP set — callers should
// pre-filter via _build_rpz_artifacts logic, but the guard here
// keeps the renderer well-defined).
func RenderRPZZone(in RPZInput) string {
	patterns := normalizeRPZPatterns(in.Patterns)
	if len(patterns) == 0 {
		return ""
	}
	if in.Action == "sinkhole" && in.SinkIPv4 == "" && in.SinkIPv6 == "" {
		return ""
	}
	defaultTTL := in.DefaultTTL
	if defaultTTL == 0 {
		defaultTTL = 60
	}
	apex := strings.TrimRight(in.RpzZoneName, ".")
	serial := in.Now.Unix()

	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s.\n", apex)
	fmt.Fprintf(&b, "$TTL %d\n", defaultTTL)
	fmt.Fprintf(&b, "@\tIN\tSOA\tns1.%s. hostmaster.%s. (\n", apex, apex)
	fmt.Fprintf(&b, "\t\t\t%d\t; serial\n", serial)
	b.WriteString("\t\t\t900\t; refresh\n")
	b.WriteString("\t\t\t900\t; retry\n")
	b.WriteString("\t\t\t1800\t; expire\n")
	fmt.Fprintf(&b, "\t\t\t%d)\t; minimum\n", defaultTTL)
	fmt.Fprintf(&b, "@\t300\tIN\tNS\tns1.%s.\n", apex)
	b.WriteString("\n")

	for _, pat := range patterns {
		owner := rpzPatternToOwner(pat)
		switch in.Action {
		case "block":
			fmt.Fprintf(&b, "%s\tIN\tCNAME\t.\n", owner)
		case "sinkhole":
			if in.SinkIPv4 != "" {
				fmt.Fprintf(&b, "%s\tIN\tA\t%s\n", owner, in.SinkIPv4)
			}
			if in.SinkIPv6 != "" {
				fmt.Fprintf(&b, "%s\tIN\tAAAA\t%s\n", owner, in.SinkIPv6)
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

// normalizeRPZPatterns strips whitespace + drops empties, then
// dedupes and sorts. Matches Python's
// `sorted({p for p in patterns if p.strip()})`.
func normalizeRPZPatterns(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		seen[p] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// rpzPatternToOwner strips a trailing dot — RPZ owners are
// zone-origin-relative. Python's _rpz_pattern_to_owner.
func rpzPatternToOwner(pattern string) string {
	return strings.TrimRight(strings.TrimSpace(pattern), ".")
}

// BuildRpzArtifacts renders one RPZ zone file per blocklist and
// returns the (filename → text) map + the (name, filename) pairs
// the Hickory recursive config needs to reference them.
//
// Mirrors Python's _build_rpz_artifacts in services/dns.py L2186.
// Sinkhole blocklists with neither v4 nor v6 sink IPs are skipped.
// Predictable naming (`bl-NNN.rpz.dcim.local`) keeps the bundle
// etag stable across renders even if blocklist ordering changes
// upstream.
func BuildRpzArtifacts(blocklists []Blocklist, now time.Time) (zones map[string]string, refs []RPZRef) {
	zones = map[string]string{}
	for i, bl := range blocklists {
		patterns := bl.Patterns
		if len(patterns) == 0 {
			continue
		}
		if bl.Action == "sinkhole" {
			v4 := derefOr(bl.SinkIPv4, "")
			v6 := derefOr(bl.SinkIPv6, "")
			if v4 == "" && v6 == "" {
				continue
			}
		}
		name := fmt.Sprintf("bl-%03d.rpz.dcim.local", i)
		filename := name + ".zone"
		zones[filename] = RenderRPZZone(RPZInput{
			RpzZoneName: name,
			Action:      bl.Action,
			Patterns:    patterns,
			SinkIPv4:    derefOr(bl.SinkIPv4, ""),
			SinkIPv6:    derefOr(bl.SinkIPv6, ""),
			Now:         now,
		})
		refs = append(refs, RPZRef{Name: name, Filename: filename})
	}
	return zones, refs
}

// RPZRef is one (zone-name, filename) pair the Hickory renderer
// references in its `[[response_policy]]` block.
type RPZRef struct {
	Name     string
	Filename string
}

func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
