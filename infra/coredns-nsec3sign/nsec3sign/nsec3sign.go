// Plugin entry point — declares the `Nsec3Sign` handler type and
// implements `plugin.Handler.ServeDNS`. ServeDNS runs the full
// online-signing pass: intercept the downstream response, attach
// NSEC3 denial proofs to NODATA / NXDOMAIN authority sections (via
// `denial.go`), then sign every RRset in answer + authority (via
// `signer.go`) when the client signals DO=1. The chain that powers
// denial proofs is populated at startup from the configured zone
// file (`zone.go`).
//
// Known gaps documented in SECURITY-REVIEW.md: wildcard-expansion
// proofs (RFC 5155 §7.2.5) and delegation referral DS-attestation
// proofs aren't synthesized. Both are corner cases for the DCIM zone
// shapes this plugin was built for.

package nsec3sign

import (
	"context"
	"time"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/metrics"
	"github.com/coredns/coredns/plugin/pkg/cache"
	"github.com/coredns/coredns/plugin/pkg/nonwriter"
	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
)

// Nsec3Sign is one parsed `nsec3sign { ... }` block from the
// Corefile. setup() populates the fields in three phases: parse()
// reads the Corefile values, loadKeys() opens the key files, and
// loadChain() parses the zone file into the NSEC3 chain. ServeDNS
// consumes all of that at query time.
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
	// in keys.go reads both halves and groups them by tag.
	KeyFiles []string

	// ZoneFile is the BIND-format zone file the chain builder parses
	// to enumerate owner names. Typically the same path the parent
	// `file` plugin block uses; DCIM's renderer emits both pointing
	// at the same path. Empty disables denial-of-existence — the
	// plugin still signs RRsets but doesn't synthesize NSEC3 RRs.
	ZoneFile string

	// Keys are the parsed key pairs ready for signing. Populated by
	// loadKeys() in setup() after parse(); empty when the Corefile
	// has no `key file` directives (responses pass through unsigned
	// — see the WARNING log line in setup).
	Keys []*signingKey

	// Chain is the pre-computed NSEC3 chain for this zone. nil when
	// no `zone file` directive is set; ServeDNS only uses the chain
	// for denial proofs, so positive responses work even without it.
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

	// SigCache holds previously-computed RRSIGs keyed by RRset
	// content. Populated by setup() once parse() has decided the
	// capacity. A nil cache is permitted (test wiring) — signRRset
	// short-circuits the cache path when it's absent.
	SigCache *cache.Cache

	// stopJanitor is closed by OnShutdown to terminate the cache's
	// background cleanup goroutine. nil when no cache was created.
	stopJanitor chan struct{}
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
	server := metrics.WithServer(ctx)
	// Attach NSEC3 denial proofs BEFORE signing, so the new NSEC3
	// RRsets get picked up by signMessage's authority-section walk.
	// Positive responses go through both calls unchanged — neither
	// step modifies a message it has nothing to do.
	signed := n.attachDenialProof(nw.Msg, state.Name(), server, now)
	signed = n.signMessage(signed, zone, server, now)
	return code, w.WriteMsg(signed)
}
