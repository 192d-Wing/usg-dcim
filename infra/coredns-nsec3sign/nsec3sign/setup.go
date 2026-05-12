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

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		n.Next = next
		return n
	})

	log.Infof("nsec3sign registered for %v (step-1 noop)", n.Zones)
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
		// Setting these as defaults means an operator who writes only
		// `nsec3sign { key file ... }` gets the safe modern profile.
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
			switch c.Val() {
			case "key":
				// `key file <basename>` — the BIND-format key pair the
				// signer will load (basename without the `.key` /
				// `.private` suffix). Repeatable to mix KSK + ZSK.
				args := c.RemainingArgs()
				if len(args) != 2 || args[0] != "file" {
					return nil, c.ArgErr()
				}
				n.KeyFiles = append(n.KeyFiles, args[1])

			case "salt":
				// Hex-encoded NSEC3 salt, or "" / "-" for empty. RFC
				// 9276 recommends empty; we accept both spellings so
				// generated Corefiles can use whichever is more
				// readable.
				args := c.RemainingArgs()
				if len(args) != 1 {
					return nil, c.ArgErr()
				}
				salt := args[0]
				if salt == `""` || salt == "-" {
					salt = ""
				}
				n.Salt = salt

			case "iterations":
				args := c.RemainingArgs()
				if len(args) != 1 {
					return nil, c.ArgErr()
				}
				it, err := strconv.Atoi(args[0])
				if err != nil || it < 0 {
					return nil, c.Errf("iterations must be a non-negative integer, got %q", args[0])
				}
				// RFC 9276 §3.1: do not exceed 0 for new deployments.
				// We still allow up to 150 (the historic BIND cap) for
				// operators migrating from existing NSEC3 chains, but
				// we log a warning so the value doesn't slip through
				// review unnoticed.
				if it > 0 {
					log.Warningf("iterations=%d set on %v — RFC 9276 recommends 0", it, n.Zones)
				}
				if it > 150 {
					return nil, c.Errf("iterations=%d exceeds the maximum of 150", it)
				}
				n.Iterations = uint16(it)

			case "opt_out":
				if len(c.RemainingArgs()) != 0 {
					return nil, c.ArgErr()
				}
				n.OptOut = true

			case "cache_capacity":
				args := c.RemainingArgs()
				if len(args) != 1 {
					return nil, c.ArgErr()
				}
				cap, err := strconv.Atoi(args[0])
				if err != nil || cap <= 0 {
					return nil, c.Errf("cache_capacity must be a positive integer, got %q", args[0])
				}
				n.CacheCapacity = cap

			default:
				return nil, c.Errf("unknown nsec3sign directive %q", c.Val())
			}
		}
	}

	return n, nil
}
