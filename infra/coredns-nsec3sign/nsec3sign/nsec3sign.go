// Plugin entry point — declares the `Nsec3Sign` handler type and
// implements `plugin.Handler.ServeDNS`. As of step 5, ServeDNS does
// the full online-signing pass: intercept the downstream response,
// attach NSEC3 denial proofs to NODATA / NXDOMAIN authority sections
// (via `denial.go`), then sign every RRset in answer + authority
// (via `signer.go`) when the client signals DO=1.
//
// The chain that powers denial proofs is still populated by hand in
// tests — the file-plugin integration that walks a live zone tree
// lands in step 5b. Until then `Nsec3Sign.Chain` defaults to nil and
// attachDenialProof short-circuits, so positive responses keep
// working without the chain.
//
// Still to land: LRU signature cache (step 6) that will collapse
// duplicate-RRset signing work across queries; per-query metrics.

package nsec3sign

import (
	"context"
	"time"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/nonwriter"
	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
)

// Nsec3Sign is one parsed `nsec3sign { ... }` block from the
// Corefile. The fields are populated in two phases: parse() in
// setup.go reads the Corefile values, then loadKeys() (also in
// setup) opens the key files. ServeDNS consumes the result at query
// time. A future cache field will slot in alongside Chain when step
// 6 lands.
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
	// in keys.go reads both halves and groups them by tag. Stored
	// here even after loadKeys() runs so a future SIGUSR1 reload can
	// re-open them without revisiting the Corefile.
	KeyFiles []string

	// Keys are the parsed key pairs ready for signing. Populated by
	// loadKeys() in setup() after parse(); empty in step-1 plugins
	// that have no `key file` directives.
	Keys []*signingKey

	// Chain is the pre-computed NSEC3 chain for this zone. nil until
	// the file-plugin integration (later step) walks the zone tree
	// and calls buildChain. ServeDNS only uses the chain for denial
	// proofs, so positive responses work even before it's populated.
	Chain *chain

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

// ServeDNS handles one query. The signing path runs only when all
// of (a) the QNAME is inside one of our configured zones, (b) the
// client set the EDNS0 DO bit, and (c) at least one key is loaded.
// Failing any of those, the query passes through to the next plugin
// unchanged, which lets unsigned-but-DNSSEC-capable resolvers still
// reach the data plane.
func (n *Nsec3Sign) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}

	zone := plugin.Zones(n.Zones).Matches(state.Name())
	if zone == "" {
		return plugin.NextOrFailure(n.Name(), n.Next, ctx, w, r)
	}
	if !state.Do() {
		// RFC 6840 §5.9 — don't ship DNSSEC RRs to clients that
		// didn't ask for them.
		return plugin.NextOrFailure(n.Name(), n.Next, ctx, w, r)
	}
	if len(n.Keys) == 0 {
		// Permitted in the build-out phase; step 7 will gate this
		// at setup() so production deploys can't slip through.
		return plugin.NextOrFailure(n.Name(), n.Next, ctx, w, r)
	}

	// Intercept the downstream response so we can sign it before it
	// reaches the wire. nonwriter is the standard CoreDNS plugin
	// pattern for this — it embeds the real writer and captures the
	// final WriteMsg into .Msg without forwarding.
	nw := nonwriter.New(w)
	code, err := plugin.NextOrFailure(n.Name(), n.Next, ctx, nw, r)
	if err != nil {
		return code, err
	}
	if nw.Msg == nil {
		// Some plugins return an rcode without writing a message
		// (e.g. when chaining further). Nothing to sign in that case.
		return code, nil
	}

	now := time.Now().UTC()
	// Attach NSEC3 denial proofs BEFORE signing, so the new NSEC3
	// RRsets get picked up by signMessage's authority-section walk.
	// Positive responses go through both calls unchanged — neither
	// step modifies a message it has nothing to do.
	signed := n.attachDenialProof(nw.Msg, state.Name(), now)
	signed = n.signMessage(signed, zone, now)
	return code, w.WriteMsg(signed)
}
