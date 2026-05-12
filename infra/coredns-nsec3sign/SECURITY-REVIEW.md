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

### Documented as known gaps (correctness, not vulnerability)

**S-03 — Wildcard expansion proofs not synthesized.** RFC 5155 §7.2.5
requires that a positive response derived from a wildcard expansion
ship with an NSEC3 record proving QNAME itself doesn't exist (only
the wildcard does). We don't emit this — wildcard-expanded responses
go out without the supporting NSEC3, and strict validators (BIND
`validate-except`, Unbound `harden-below-nxdomain`) treat them as
bogus.

Impact: limited. DCIM site zones are typically flat — host-name to
A/AAAA mappings, no wildcards. Apex zones rarely use wildcards
either. The gap matters only if an operator explicitly defines a
`*.something.zone.` record.

Mitigation when needed: classify response via `state.IsExpansion()`
(miekg/dns can detect this from the wildcard-source label) and
attach the NSEC3 proof for `QNAME` in the answer section's
authority companion.

**S-04 — Delegation referrals don't get DS-attestation NSEC3s.** A
query for a name below an in-bailiwick delegation point should
return a referral (NOERROR + NS in authority, no SOA). The matching
NSEC3 for the delegation owner should be attached to prove either
"DS present" (for secure delegations) or "DS absent" (for opt-out
insecure delegations).

Our `attachDenialProof` classifies only `NameError` and `NoData`
through `response.Typify`. Referrals (`response.Delegation`) fall
through unsigned. Validators chasing into a child zone can't
establish a chain of trust if the parent's referral lacks the
DS-attestation NSEC3.

Impact: matters mostly for apex zones that delegate to site zones.
A site zone is unlikely to delegate further. Mitigation: extend the
switch in `attachDenialProof` to handle `response.Delegation` by
emitting the matching NSEC3 of the delegation owner.

**S-05 — Empty non-terminals not synthesized in the chain.** When a
zone has `a.b.example.test.` but no records at `b.example.test.`,
the parent `b.example.test.` is an "empty non-terminal" (ENT) per
RFC 4592. RFC 5155 §7.2.2 requires ENTs to be in the NSEC3 chain
because queries for them must return NODATA (the name "exists" by
virtue of having descendants) — and the matching NSEC3 must list
no types at all.

Our `parseZoneFile` enumerates only owners with at least one record
in the file. ENTs are absent from the chain. Queries for them
return our generic NXDOMAIN proof, which validates against
`!matchingNSEC3(qname)` and thus claims the name doesn't exist.
For a deep zone this is technically incorrect — but flat DCIM zones
have no ENTs.

Mitigation: in `ownersToNameInfo`, walk every label-shortening of
every owner name and add the prefixes as ENTs (empty Types slice).
Easy enough but unnecessary for current zone shapes.

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

| ID    | Severity                   | Disposition          |
| ----- | -------------------------- | -------------------- |
| S-01  | low (defense-in-depth)     | fixed                |
| S-02  | low (operability)          | fixed (warning)      |
| S-03  | medium (DNSSEC compliance) | documented gap       |
| S-04  | medium (DNSSEC compliance) | documented gap       |
| S-05  | low (DNSSEC compliance)    | documented gap       |
| S-06  | low (DoS, requires insider)| matches upstream     |
| S-07  | low (footgun)              | documented           |
| S-08  | info                       | matches upstream     |
| S-09  | info (year-2038)           | noted                |
| S-10  | info (Go limitation)       | noted                |
| S-11  | low (misconfig only)       | noted                |

No HIGH or CRITICAL findings. The two MEDIUM items (S-03, S-04) are
DNSSEC-protocol completeness gaps that affect zones with wildcards
or delegations respectively. Neither applies to the design target
(flat DCIM site zones); operators who venture beyond that profile
should plan for the implementation work, not assume the plugin
already covers it.
