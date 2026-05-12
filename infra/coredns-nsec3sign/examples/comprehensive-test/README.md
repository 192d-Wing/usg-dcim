# comprehensive-test

End-to-end deploy-validation harness. Exercises every code path
the plugin grew across the build-out + security review against
the **GHCR-published image** (not a local build) — a stronger
test of "what production gets" than `quick-smoke` (hand-rolled,
local build) or `dcim-bundle-smoke` (renderer-driven, local
build).

Eight scenarios in one zone:

| # | Query                                  | Type | Tests           |
| - | -------------------------------------- | ---- | --------------- |
| 1 | `host1.example.test.`                  | A    | Positive answer + RRSIG |
| 2 | `missing.example.test.`                | A    | NXDOMAIN closest-encloser proof |
| 3 | `ns1.example.test.`                    | AAAA | NODATA — matching NSEC3 with `A RRSIG` bitmap (no AAAA) |
| 4 | `foo.dev.example.test.`                | A    | **S-03**: wildcard expansion (`*.dev`) — RRSIG.Labels = 3 (wildcard owner's label count, not qname's) + covering NSEC3 |
| 5 | `floor3.building-a.example.test.`      | A    | **S-05**: ENT NODATA — matching NSEC3 with **empty** type bitmap |
| 6 | `building-a.example.test.`             | A    | **S-05**: deep ENT — same shape, one level up |
| 7 | `host.secure.example.test.`            | A    | **S-04**: secure delegation referral — matching NSEC3 bitmap = `NS DS RRSIG` |
| 8 | `host.insecure.example.test.`          | A    | **S-04**: insecure delegation referral — matching NSEC3 bitmap = `NS RRSIG` (no DS) |

The zone fixture in `example.test.zone` is hand-crafted to make
each scenario reproducible without going through the DCIM
renderer. The renderer is exercised separately by
`dcim-bundle-smoke`; this harness focuses on the **plugin
behavior** against operator-shaped zones.

## Files

| File                                 | What |
| ------------------------------------ | ---- |
| `example.test.zone`                  | BIND zone covering all 8 scenarios |
| `keygen.go`                          | ECDSA-P256 ZSK generator (same as quick-smoke) |
| `Kexample.test.+013+<tag>.{key,private}` | Generated key pair (regenerate with `go run keygen.go example.test.`) |
| `Corefile`                           | Loads file + nsec3sign with salt=`aabbccdd`, iter=0 |
| `.gitignore`                         | Local exception to the repo-root `*.key` block |

## Running

```powershell
# 1. Pull the published image (force a real fetch, not local cache).
docker rmi ghcr.io/192d-wing/coredns-nsec3sign:v1.11.3-2
docker pull ghcr.io/192d-wing/coredns-nsec3sign:v1.11.3-2

# 2. Boot against the test bundle.
docker run -d --name nsec3-deploy-test `
  -p 127.0.0.1:15656:1053/udp -p 127.0.0.1:15656:1053/tcp `
  -v "${pwd}:/data:ro" `
  ghcr.io/192d-wing/coredns-nsec3sign:v1.11.3-2 `
  -conf /data/Corefile
docker logs nsec3-deploy-test
# Expect: "nsec3sign: chain built for example.test. with 12 owner names"
# (9 explicit owners + 3 synthesized ENTs).

# 3. Run the eight scenarios.
dig "@127.0.0.1" -p 15656 +dnssec host1.example.test A           # 1. positive
dig "@127.0.0.1" -p 15656 +dnssec missing.example.test A         # 2. NXDOMAIN
dig "@127.0.0.1" -p 15656 +dnssec ns1.example.test AAAA          # 3. NODATA
dig "@127.0.0.1" -p 15656 +dnssec foo.dev.example.test A         # 4. wildcard
dig "@127.0.0.1" -p 15656 +dnssec floor3.building-a.example.test A   # 5. ENT
dig "@127.0.0.1" -p 15656 +dnssec building-a.example.test A      # 6. deep ENT
dig "@127.0.0.1" -p 15656 +dnssec host.secure.example.test A     # 7. secure deleg
dig "@127.0.0.1" -p 15656 +dnssec host.insecure.example.test A   # 8. insecure deleg

# 4. Tear down.
docker rm -f nsec3-deploy-test
```

## What to look for

- **Scenario 1**: NOERROR + A record + one RRSIG covering A. RRSIG
  inception ≈ now-1h, expiration ≈ now+8d, keytag matches the ZSK.
- **Scenario 2**: NXDOMAIN + 2 NSEC3s + their RRSIGs + RRSIG over
  SOA. Each NSEC3 covers a slot in the chain that brackets the
  missing name's hash.
- **Scenario 3**: NOERROR + one matching NSEC3 whose type bitmap
  ends with `A RRSIG` (no AAAA — that's the proof).
- **Scenario 4** (S-03 wildcard): NOERROR + the wildcard's A
  rewritten to the qname owner. **Critical check**: the
  RRSIG line reads `RRSIG A 13 3 3600 …` — the `3` is the
  Labels field, equal to the wildcard owner's label count
  (`*.dev.example.test.` → `dev`, `example`, `test` = 3 non-*
  labels). It is NOT 4 (which would be the qname's label count).
  Plus one covering NSEC3 in authority + its RRSIG.
- **Scenario 5/6** (S-05 ENT): NOERROR + one matching NSEC3 with
  **NO type bitmap on the wire** — the empty bitmap is the ENT
  signal. Validators interpret it as "name exists, no records here."
- **Scenario 7** (S-04 secure): NS + DS + RRSIG(DS) + matching
  NSEC3 with type bitmap = `NS DS RRSIG`. Validators read the
  bitmap → chase DS → child.
- **Scenario 8** (S-04 insecure): NS + matching NSEC3 with type
  bitmap = `NS RRSIG` (no DS). Validators read the bitmap → "no
  DS here, child is unsigned" → stop chasing.

## Coverage map

This harness validates the entire build-out:

| Fix  | What it does                                       | Tested by  |
| ---- | -------------------------------------------------- | ---------- |
| S-01 | Salt hex+length validation at parse time           | implicit (chain boots with hex salt) |
| S-02 | Empty-keys WARNING at startup                      | not exercised (we have keys) |
| S-03 | Wildcard expansion proof + RRSIG.Labels fix        | scenario 4 |
| S-04 | Delegation NSEC3 (secure / insecure / opt-out)     | scenarios 7, 8 |
| S-05 | ENT synthesis                                      | scenarios 5, 6 |

Plus the baseline functionality from steps 4-5b: scenarios 1-3.

A green run of this harness against the published v1.11.3-2 image
means every documented fix is live in production.
