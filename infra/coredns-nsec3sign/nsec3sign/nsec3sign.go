// Plugin entry point — declares the `Nsec3Sign` handler type and
// implements `plugin.Handler.ServeDNS`. For step 1 of the build-out
// `ServeDNS` is a deliberate no-op pass-through so the custom CoreDNS
// image build and chain wiring can be validated independently of the
// (still-to-be-written) cryptographic + denial logic.
//
// When the rest of the plugin lands, ServeDNS will:
//
//  1. Forward to the data-source plugin via a `nonwriter`-style
//     response interceptor so we can inspect the answer before it
//     reaches the client.
//  2. Decide whether the response is a positive answer, NODATA,
//     NXDOMAIN, or a wildcard expansion.
//  3. Sign each non-DNSSEC RRset that the client signaled DO=1 for,
//     drawing fresh RRSIGs from the cache where possible.
//  4. For NODATA / NXDOMAIN, attach the closest-encloser proof
//     (matching NSEC3 + covering NSEC3 + wildcard NSEC3) drawn from
//     the pre-computed sorted hash chain in chain.go.
//  5. Write the rewritten response to the real ResponseWriter.

package nsec3sign

import (
	"context"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
)

// Nsec3Sign is one parsed `nsec3sign { ... }` block from the
// Corefile. The fields are all populated by parse() in setup.go and
// then consumed by ServeDNS at query time. Subsequent build-out
// steps will add a `keys`, `chain`, and `cache` field — kept out of
// this struct for now so the noop build doesn't pull in unused
// types.
type Nsec3Sign struct {
	// Next is the next plugin in the CoreDNS chain; Caddy injects it
	// in setup() via dnsserver.GetConfig(c).AddPlugin.
	Next plugin.Handler

	// Zones this plugin instance covers. Populated from either the
	// directive arguments or the surrounding server block. The plugin
	// only signs queries whose QNAME is within one of these zones —
	// everything else passes through unchanged.
	Zones []string

	// KeyFiles are BIND-style basenames (no `.key`/`.private` suffix)
	// for the KSK + ZSK material this zone is signed with. The loader
	// in keys.go (step 2) reads both halves and groups them by tag.
	KeyFiles []string

	// Salt + Iterations + OptOut control NSEC3 chain generation per
	// RFC 5155. Defaults set by parse() follow RFC 9276 guidance
	// (empty salt, zero iterations).
	Salt       string
	Iterations uint16
	OptOut     bool

	// CacheCapacity is the LRU size for the signature cache. Default
	// 10 000 entries, matching the upstream `dnssec` plugin.
	CacheCapacity int
}

// Name returns the plugin's name. Implements plugin.Handler. Used by
// CoreDNS for logging, metrics labels, and `coredns -plugins` output.
func (n *Nsec3Sign) Name() string { return "nsec3sign" }

// ServeDNS handles one query. Step 1 unconditionally forwards to the
// next plugin without modifying the response. The `request.Request`
// is constructed so the handler shape matches what the eventual
// implementation needs — keeping the surface stable across the
// build-out lets the test scaffolding land alongside the noop.
func (n *Nsec3Sign) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}

	// Step 1: no zone-scoping check, no DO-bit check, no signing.
	// Just pass through. The two lines below exist only so the
	// imports compile without _-renames; they vanish when ServeDNS
	// gains real logic.
	_ = state.Name()
	_ = state.QType()

	return plugin.NextOrFailure(n.Name(), n.Next, ctx, w, r)
}
