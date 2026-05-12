// Package nsec3sign registers the `nsec3sign` CoreDNS plugin.
//
// The plugin pairs with the data-source plugin (`file`, `auto`, etc.)
// to add NSEC3-based DNSSEC online signing on the response path. It's
// the NSEC3 counterpart to CoreDNS's upstream `dnssec` plugin.
//
// This file owns Corefile parsing + Caddy registration. The runtime
// signing path lives in nsec3sign.go; the cryptographic + denial
// machinery (key loading, chain builder, signer, denial proofs) will
// land in keys.go / chain.go / signer.go / denial.go across the next
// build-out steps. Step 1 wires the plugin into the chain as a no-op
// so the custom CoreDNS image build can be validated independently of
// the crypto.
package nsec3sign

import (
	"strconv"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
)

var log = clog.NewWithPlugin("nsec3sign")

func init() {
	plugin.Register("nsec3sign", setup)
}

// setup is invoked by Caddy for each `nsec3sign { ... }` block in the
// Corefile. It parses the block, wires the plugin into the
// server-block's response chain, and returns. An error here aborts
// CoreDNS startup, which is what we want — silent
// misconfiguration of a signing plugin is the worst failure mode.
func setup(c *caddy.Controller) error {
	n, err := parse(c)
	if err != nil {
		return plugin.Error("nsec3sign", err)
	}

	// Load key material at startup so file I/O / parse errors abort
	// CoreDNS before it starts answering queries. Silent fall-through
	// to unsigned responses is the worst failure mode for a signing
	// plugin — better to fail loud and visible at boot.
	if err := n.loadKeys(); err != nil {
		return plugin.Error("nsec3sign", err)
	}

	// Build the NSEC3 chain from the configured zone file. Same
	// rationale as loadKeys: surface parse / I/O failures at startup,
	// not at first denial query. No-op when no zone file is set
	// (operator opted out of denial proofs).
	if err := n.loadChain(); err != nil {
		return plugin.Error("nsec3sign", err)
	}

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		n.Next = next
		return n
	})

	log.Infof("nsec3sign registered for %v with %d key(s)", n.Zones, len(n.Keys))
	return nil
}

// parse reads one `nsec3sign { ... }` block and returns the
// configured plugin instance. The directive keywords are recognized
// here in step 1 but most of them only store raw values — the
// crypto/chain code that consumes them isn't online yet. We still
// validate the *shape* (arity, integer parses) so an operator who
// drafts the eventual Corefile today gets useful errors at startup.
func parse(c *caddy.Controller) (*Nsec3Sign, error) {
	n := &Nsec3Sign{
		// RFC 9276 recommended defaults — empty salt, zero iterations.
		// An operator who writes only `nsec3sign { key file ... }`
		// gets the safe modern profile.
		Salt:          "",
		Iterations:    0,
		CacheCapacity: 10000,
	}

	for c.Next() {
		// `nsec3sign` may take zone arguments on the directive line;
		// when absent we inherit the surrounding server block's zones,
		// matching how `dnssec` behaves.
		n.Zones = plugin.OriginsFromArgsOrServerBlock(c.RemainingArgs(), c.ServerBlockKeys)

		for c.NextBlock() {
			if err := parseDirective(c, n); err != nil {
				return nil, err
			}
		}
	}

	return n, nil
}

// directiveParsers dispatches one Corefile keyword to its handler.
// Pulled out of parse() so adding a directive doesn't grow the
// switch any further — each keyword owns its own validation logic
// and the top-level function stays linear.
var directiveParsers = map[string]func(*caddy.Controller, *Nsec3Sign) error{
	"key":            parseKey,
	"zone":           parseZone,
	"salt":           parseSalt,
	"iterations":     parseIterations,
	"opt_out":        parseOptOut,
	"cache_capacity": parseCacheCapacity,
}

func parseDirective(c *caddy.Controller, n *Nsec3Sign) error {
	fn, ok := directiveParsers[c.Val()]
	if !ok {
		return c.Errf("unknown nsec3sign directive %q", c.Val())
	}
	return fn(c, n)
}

// parseKey accepts `key file <basename>`. The basename omits the
// `.key` / `.private` suffix; loadKey appends them. Repeatable so
// one block can carry both KSK and ZSK material.
func parseKey(c *caddy.Controller, n *Nsec3Sign) error {
	args := c.RemainingArgs()
	if len(args) != 2 || args[0] != "file" {
		return c.ArgErr()
	}
	n.KeyFiles = append(n.KeyFiles, args[1])
	return nil
}

// parseZone accepts `zone file <path>` — the BIND-format zone the
// chain builder parses. Only one per block; multi-zone setups
// should use one `nsec3sign` block per zone so each gets its own
// chain. The duplicate of the parent `file` directive's path is
// deliberate: option 1 (walking the file plugin's private tree)
// would couple us to CoreDNS internals.
func parseZone(c *caddy.Controller, n *Nsec3Sign) error {
	args := c.RemainingArgs()
	if len(args) != 2 || args[0] != "file" {
		return c.ArgErr()
	}
	if n.ZoneFile != "" {
		return c.Errf("zone file already set to %q; one zone file per nsec3sign block", n.ZoneFile)
	}
	n.ZoneFile = args[1]
	return nil
}

// parseSalt accepts a hex-encoded NSEC3 salt, or "" / "-" for empty.
// RFC 9276 recommends empty; we accept both spellings so a renderer
// can use whichever reads better in its template.
func parseSalt(c *caddy.Controller, n *Nsec3Sign) error {
	args := c.RemainingArgs()
	if len(args) != 1 {
		return c.ArgErr()
	}
	salt := args[0]
	if salt == `""` || salt == "-" {
		salt = ""
	}
	n.Salt = salt
	return nil
}

// parseIterations enforces a non-negative integer up to 150 (the
// historic BIND cap). RFC 9276 §3.1 recommends 0 for new
// deployments; any non-zero value gets logged so it doesn't slip
// through review unnoticed.
func parseIterations(c *caddy.Controller, n *Nsec3Sign) error {
	args := c.RemainingArgs()
	if len(args) != 1 {
		return c.ArgErr()
	}
	it, err := strconv.Atoi(args[0])
	if err != nil || it < 0 {
		return c.Errf("iterations must be a non-negative integer, got %q", args[0])
	}
	if it > 150 {
		return c.Errf("iterations=%d exceeds the maximum of 150", it)
	}
	if it > 0 {
		log.Warningf("iterations=%d set on %v — RFC 9276 recommends 0", it, n.Zones)
	}
	n.Iterations = uint16(it)
	return nil
}

// parseOptOut is a flag — no arguments. Setting it opts insecure
// delegations out of the NSEC3 chain per RFC 5155 §6.
func parseOptOut(c *caddy.Controller, n *Nsec3Sign) error {
	if len(c.RemainingArgs()) != 0 {
		return c.ArgErr()
	}
	n.OptOut = true
	return nil
}

// parseCacheCapacity sets the LRU size for the signature cache.
// Zero is rejected because a cache that never holds anything is a
// pathological config — operators who want signing without caching
// should remove the directive and rely on the default.
func parseCacheCapacity(c *caddy.Controller, n *Nsec3Sign) error {
	args := c.RemainingArgs()
	if len(args) != 1 {
		return c.ArgErr()
	}
	capN, err := strconv.Atoi(args[0])
	if err != nil || capN <= 0 {
		return c.Errf("cache_capacity must be a positive integer, got %q", args[0])
	}
	n.CacheCapacity = capN
	return nil
}
