// Recursive CoreDNS Corefile renderer + blocklist template helpers
// (PR 28 — DNS bundle 5/N). Pure function: no DB, no I/O. Mirrors
// render_corefile_recursive + _render_blocklist_template +
// _pattern_to_regex from services/dns.py L1152-L1271.
package dns

import (
	"fmt"
	"sort"
	"strings"
)

// CorefileRecursiveInput bundles the recursive-pod Corefile inputs.
type CorefileRecursiveInput struct {
	// FabricApexes — one stub-zone forward per apex (sorted +
	// deduped internally) routes internal lookups to AuthUnicastIP.
	FabricApexes []string
	// AuthUnicastIP — when non-nil, emit per-apex forward blocks
	// pointing at this IP. nil/empty skips the per-apex blocks
	// entirely (useful for a stripped-down recursive-only pod).
	AuthUnicastIP *string
	// UpstreamResolvers — the catch-all block's `forward .` target.
	// Empty defaults to "1.1.1.1 8.8.8.8" matching Python.
	UpstreamResolvers []string
	// ConditionalForwarders — operator-defined (pattern, upstreams)
	// pairs. Empty upstreams are skipped (no-op forward would crash
	// CoreDNS). Sorted by pattern for deterministic Corefile.
	ConditionalForwarders []ConditionalForwarder
	// Blocklists — applied as `template` directives inside the
	// catch-all block. Iterated in caller order so operators can
	// pin priority.
	Blocklists []Blocklist
}

// ConditionalForwarder = one (zone-pattern, upstream-list) pair.
type ConditionalForwarder struct {
	Pattern   string
	Upstreams []string
}

// Blocklist is one blocklist entry to compile to a CoreDNS
// `template` directive. SinkIPv4/SinkIPv6 are only consulted for
// Action="sinkhole"; for Action="block" they're ignored (NXDOMAIN
// has no answer payload).
type Blocklist struct {
	Action   string // "block" or "sinkhole"
	Patterns []string
	SinkIPv4 *string
	SinkIPv6 *string
}

// RenderCorefileRecursive emits the recursive CoreDNS Corefile text.
func RenderCorefileRecursive(in CorefileRecursiveInput) string {
	upstreamList := strings.Join(in.UpstreamResolvers, " ")
	if upstreamList == "" {
		upstreamList = "1.1.1.1 8.8.8.8"
	}

	var blocks []string

	// Per-apex stub-zone forwards. Dedup + sort the apex list so
	// the render is deterministic regardless of caller order.
	if in.AuthUnicastIP != nil && *in.AuthUnicastIP != "" {
		apexes := dedupSorted(in.FabricApexes)
		for _, apex := range apexes {
			blocks = append(blocks, fmt.Sprintf(
				"%s:53 {\n    forward . %s:53\n    log\n    errors\n}",
				apex, *in.AuthUnicastIP,
			))
		}
	}

	// Operator conditional forwarders. Sort on pattern for
	// determinism (matches Python's sorted(..., key=lambda t: t[0])).
	cfs := append([]ConditionalForwarder(nil), in.ConditionalForwarders...)
	sort.SliceStable(cfs, func(i, j int) bool {
		return cfs[i].Pattern < cfs[j].Pattern
	})
	for _, cf := range cfs {
		if len(cf.Upstreams) == 0 {
			continue
		}
		blocks = append(blocks, fmt.Sprintf(
			"%s:53 {\n    forward . %s\n    log\n    errors\n}",
			cf.Pattern, strings.Join(cf.Upstreams, " "),
		))
	}

	// Catch-all block with blocklist templates folded in.
	var templateLines []string
	for _, bl := range in.Blocklists {
		templateLines = append(templateLines, RenderBlocklistTemplate(bl)...)
	}
	catchallLines := []string{".:53 {"}
	catchallLines = append(catchallLines, templateLines...)
	catchallLines = append(catchallLines,
		fmt.Sprintf("    forward . %s", upstreamList),
		"    cache 300",
		"    log",
		"    errors",
		"    prometheus :9153",
		"    health :8080",
		"}",
	)
	blocks = append(blocks, strings.Join(catchallLines, "\n"))
	return strings.Join(blocks, "\n\n") + "\n"
}

// matchLineFmt — the 8-space-indented `match <regex>` line used by
// every CoreDNS `template` block (block + sinkhole-A + sinkhole-AAAA).
const matchLineFmt = "        match %s"

// RenderBlocklistTemplate compiles one Blocklist into 0, 1, or 2
// CoreDNS `template` directive snippets. Returns indented lines
// ready to drop into the catch-all block — empty slice when nothing
// renders (no patterns, or sinkhole with no sink IPs configured).
func RenderBlocklistTemplate(bl Blocklist) []string {
	if len(bl.Patterns) == 0 {
		return nil
	}
	regex := combinedPatternRegex(bl.Patterns)
	switch bl.Action {
	case "block":
		return []string{
			"    template ANY ANY {",
			fmt.Sprintf(matchLineFmt, regex),
			"        rcode NXDOMAIN",
			"    }",
		}
	case "sinkhole":
		var lines []string
		if bl.SinkIPv4 != nil && *bl.SinkIPv4 != "" {
			lines = append(lines,
				"    template IN A {",
				fmt.Sprintf(matchLineFmt, regex),
				fmt.Sprintf("        answer \"{{ .Name }} 60 IN A %s\"", *bl.SinkIPv4),
				"    }",
			)
		}
		if bl.SinkIPv6 != nil && *bl.SinkIPv6 != "" {
			lines = append(lines,
				"    template IN AAAA {",
				fmt.Sprintf(matchLineFmt, regex),
				fmt.Sprintf("        answer \"{{ .Name }} 60 IN AAAA %s\"", *bl.SinkIPv6),
				"    }",
			)
		}
		return lines
	}
	return nil
}

func combinedPatternRegex(patterns []string) string {
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		parts = append(parts, fmt.Sprintf("(%s)", patternToRegex(p)))
	}
	return strings.Join(parts, "|")
}

// patternToRegex translates one DNS-name pattern into a regex
// fragment for the CoreDNS `template match` directive. Only `*.`
// (leading-label wildcard) is supported; every other character is
// escaped so dots in domain names don't accidentally match anything.
func patternToRegex(pattern string) string {
	p := strings.ToLower(strings.TrimRight(strings.TrimSpace(pattern), "."))
	wildcardHead := strings.HasPrefix(p, "*.")
	body := p
	if wildcardHead {
		body = p[2:]
	}
	// Escape ONLY dots, matching Python's `body.replace(".", r"\.")`.
	// regexp.QuoteMeta would also escape `+`, `*`, `?`, `(`, `)` etc;
	// a pattern carrying any of those would compile differently
	// across the two renderers (Python leaves them raw — which then
	// behaves as regex metachars at CoreDNS load time).
	escaped := strings.ReplaceAll(body, ".", `\.`)
	if wildcardHead {
		return fmt.Sprintf(`^.+\.%s\.?$`, escaped)
	}
	return fmt.Sprintf(`^%s\.?$`, escaped)
}

func dedupSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
