# Security review — coredns-nsec3sign

Methodical pass over every Go file in the plugin, looking for input
validation gaps, crypto correctness issues, resource-exhaustion
vectors, concurrency bugs, and DNSSEC-protocol gaps that would let
validators reject our responses.

The plugin sits in the authoritative path — it signs RRsets the
data-source plugin emits and synthesizes NSEC3 records for denial
responses. It does **not** validate signatures from elsewhere (the
KeyTrap (CVE-2023-50387) family of CPU-amplification bugs lives on
the validator side, not here). The attack surface is:

1. Operator-controlled Corefile + zone file + key files at boot.
2. Client-supplied QNAMEs at query time (untrusted).
3. Downstream plugin responses (semi-trusted; same Corefile origin).

## Test posture

`go test ./nsec3sign/... -count=1` — 60+ subtests across parser,
key loader, chain, signer, denial, cache, metrics, and zone-file
ingestion. Coverage 84.4% by statement (`setup()` and `parseZone`
were the previously-uncovered functions; this commit adds parseZone
coverage and notes setup() as inherently a Caddy-driven entry that
isn't unit-testable without spinning the whole framework).

Two wire-level smoke harnesses round out the test posture:
`examples/quick-smoke` (hand-rolled Corefile against the Go plugin)
and `examples/dcim-bundle-smoke` (DCIM Python renderer's output
against the Go plugin). Both validate signed responses end-to-end
via `dig +dnssec` against a running container.

## Findings

### Fixed in this commit

**S-01 — Salt directive accepted any string.** `parseSalt` in setup.go
normalized `""` / `-` to empty but didn't validate hex-ness or
length. A typo like `salt deadbeefg` or `salt abc` (odd length) would
slip past the parser and reach `miekg/dns.HashName` at chain-build
time, where the salt-decode failure is silent (HashName returns a
hash computed against the garbage salt rather than erroring).

Operator-controlled, but defense-in-depth at the Go boundary catches
typos that the DCIM Python Pydantic schema would catch on the API
path. Fixed by routing the value through `hex.DecodeString` at parse
time and capping at 32 bytes (RFC 9276 §3.1 recommends empty; we
allow legacy migrations up to 32 bytes, well under the wire's
255-byte ceiling).

**S-02 — Empty-keys configuration shipped silent unsigned responses.**
`ServeDNS` passes through to the next plugin when no keys are loaded.
Tests rely on this. But a Corefile that's missing `key file`
directives in production would silently serve unsigned responses
with no boot-time signal. Fixed by adding a WARNING log in setup()
when `Keys` is empty. The pass-through behaviour stays (test wiring
still works) but the WARNING means a misconfiguration is
journalctl-visible.

### Fixed in follow-up commits

**S-03 — Wildcard expansion proofs.** Fixed in commit `d40f587`.

RFC 5155 §7.2.4 requires that a positive response derived from a
wildcard expansion ship with the *covering NSEC3 for the
next-closer name* — proof that QNAME itself doesn't exist (only
the wildcard does). RFC 4034 §3.1.3 also requires RRSIG.Labels to
reflect the wildcard owner's label count (minus the `*`), not the
qname's, so validators can reconstruct the canonical wildcard
owner when verifying.

The fix has three pieces in [chain.go](nsec3sign/chain.go),
[signer.go](nsec3sign/signer.go), and [denial.go](nsec3sign/denial.go):
`chain.wildcardSource(owner)` detects expansion by checking
whether owner is missing from the chain but `*.<closest-encloser>`
exists; signRRset clones the RRset with the wildcard owner before
calling `miekg/dns.RRSIG.Sign` so the library computes Labels and
the canonical signing form correctly, then patches `Hdr.Name`
back to qname; `attachWildcardProof` scans answer RRsets and
appends the covering NSEC3 to the authority section.

§7.2.3 wildcard-NODATA (queries that fall on a wildcard but ask
for a type the wildcard doesn't carry) is still deferred —
detecting it requires correlating rcode + chain state in concert,
and the §7.2.4 positive-wildcard case is the dominant pattern.

**S-04 — Delegation referrals carry DS-attestation NSEC3.** Fixed
in commit `caebe81`.

A `response.Delegation` classification in [denial.go](nsec3sign/denial.go)
now routes through `proofForDelegation`, which emits the matching
NSEC3 for the delegation owner (showing NS + DS for secure
delegations, NS-without-DS for insecure non-opt-out) or the
covering NSEC3 with the opt-out flag for elided opt-out
delegations.

This was the highest-priority correctness gap — DCIM's documented
architecture has the per-fabric apex zone delegating to per-site
zones, and without DS attestation every cross-site DNSSEC
validation through the apex would have failed.

**S-05 — Empty non-terminals synthesized.** Fixed in commit
`be1e47a`.

`zone.go`'s new `synthesizeENTs` helper walks each explicit
owner's ancestor labels and emits a `nameInfo` with an empty
Types slice for any intermediate name not already an explicit
owner. Direct queries for ENT names now return a matching NSEC3
with empty bitmap — verifiable NODATA — instead of falling back
to NXDOMAIN.

Important enabling change: the wildcard-source detection in S-03
relies on ENTs being in the chain for deep wildcards like
`*.dev.example.test.` where `dev.example.test.` has no records of
its own. Without S-05, `findClosestEncloser` would climb past the
intermediate and look for `*.example.test.` (the wrong, less-
specific wildcard). S-05 had to land before S-03 worked end-to-end.

### Defense-in-depth observations (no change recommended)

**S-06 — FNV-64a cache key has theoretical collision risk.** The
signature cache (`sigcache.go`) hashes RRsets with `fnv.New64a()`,
matching the upstream `dnssec` plugin's pattern. A 64-bit hash has
birthday collisions at ~2^32. An attacker who can inject RRset
content (via DDNS, IPAM PTR projection, or manual record creation
— all org-authenticated paths) could engineer two RRsets that hash
to the same key. The result: the second RRset gets the first's
cached signature, validators reject the mismatch, target name
becomes unresolvable. A DoS, not a forgery.

Realistic exploitability requires authenticated org access and
offline brute force. The same vector exists in the upstream
`dnssec` plugin; switching to SHA-256 would diverge from upstream
without obvious benefit. Documented and left as-is.

**S-07 — Cached RRSIG slice returned without defensive copy.**
`signRRset` returns the cached `[]dns.RR` directly on hit. If a
caller mutated the slice in place, they'd corrupt the cached
entry. Current call path (`appendRRSIGs` → `dns.Msg.Answer`/`Ns` →
`WriteMsg`) doesn't mutate, but the invariant isn't enforced.
Documented in signer.go's header comment so future maintainers
know to preserve immutability.

**S-08 — `ServeDNS` doesn't check `ctx.Done()` between heavy steps.**
If the request is cancelled mid-flight we still complete the full
sign + denial work. Matches upstream plugin behavior; an authoritative
server with no upstream chasing has nothing meaningful to do with
the cancellation signal anyway. Documented, no change.

**S-09 — Year-2038 rollover on RRSIG Inception/Expiration.** Both
fields are `uint32` seconds-since-epoch (RFC 4034 §3.1.5). Long
after this code is retired, but worth noting.

**S-10 — Private key material not zeroed on plugin reload.** Go's
GC will reclaim old `*ecdsa.PrivateKey` / `ed25519.PrivateKey`
memory eventually, but Go has no exposed wipe primitive. Standard
limitation of Go crypto code; the cryptography community has
generally accepted this.

**S-11 — No per-zone match check on loaded DNSKEYs.** `loadKey`
doesn't verify that the DNSKEY's owner name falls within
`n.Zones`. A Corefile with a misrouted key file would sign with the
wrong key — validators chasing the parent DS would fail to find a
match. Misconfiguration only; no forgery vector.

## What was NOT a problem

- **`cache.Walk` concurrency**: read the upstream cache source. Walk
  takes the shard's RLock to snapshot keys, then a full Lock per
  `f()` invocation. Mutating the map under `f` is safe per-call.
- **`$INCLUDE` traversal**: `parseZoneFile` calls
  `zp.SetIncludeAllowed(false)`. A zone file with `$INCLUDE /etc/passwd`
  fails at parse time.
- **Algorithm/private-key mismatch**: `dk.ReadPrivateKey` validates
  the `Algorithm:` field in the `.private` file against the
  `.key`'s DNSKEY algorithm. Mismatched pairs fail at load time.
- **Iteration-count DoS**: `parseIterations` caps at 150. An
  operator can't crank to a million.
- **Cache exhaustion**: capacity is operator-bounded (default
  10,000, max set per Corefile). FIFO eviction means worst case is
  steady-state churn, not unbounded growth.
- **Goroutine leak on reload**: `runSigCacheJanitor` terminates on
  the `stopJanitor` channel; setup() registers an `OnShutdown` hook
  that closes it.

## Severity summary

| ID    | Severity                    | Disposition           |
| ----- | --------------------------- | --------------------- |
| S-01  | low (defense-in-depth)      | fixed                 |
| S-02  | low (operability)           | fixed (warning)       |
| S-03  | medium (DNSSEC compliance)  | fixed (§7.2.4)        |
| S-04  | medium (DNSSEC compliance)  | fixed                 |
| S-05  | low (DNSSEC compliance)     | fixed                 |
| S-06  | low (DoS, requires insider) | matches upstream      |
| S-07  | low (footgun)               | documented            |
| S-08  | info                        | matches upstream      |
| S-09  | info (year-2038)            | noted                 |
| S-10  | info (Go limitation)        | noted                 |
| S-11  | low (misconfig only)        | noted                 |

No HIGH or CRITICAL findings. All three MEDIUM-LOW DNSSEC-
correctness gaps (S-03 wildcard expansion, S-04 delegation
attestation, S-05 empty non-terminals) are now fixed. One sub-case
of S-03 remains deferred — wildcard-NODATA per RFC 5155 §7.2.3,
which requires correlating rcode + chain state and is rare enough
in DCIM-shaped zones to defer until an operator hits it.
