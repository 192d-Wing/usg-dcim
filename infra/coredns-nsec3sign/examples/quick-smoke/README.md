# quick-smoke

End-to-end smoke harness for `coredns-nsec3sign`. Spins up the
custom CoreDNS image against a hand-rolled three-record zone, with
one ECDSA-P256 ZSK loaded, so `dig +dnssec` can exercise positive,
NXDOMAIN, and NODATA responses on the wire.

This is the wire-level analogue of the Go unit suite — useful when
you want to confirm a change still produces a real DNS message that
real validators would accept.

## Files

| File | What |
|------|------|
| `example.test.zone` | Three-name BIND zone: apex + `ns1` + `host` (A and AAAA) |
| `Kexample.test.+013+19870.{key,private}` | ECDSA-P256 ZSK pair. Throwaway test material — regenerate via `go run keygen.go example.test.` if you want fresh keys |
| `keygen.go` | One-shot helper. Produces BIND-format key files via `miekg/dns.DNSKEY.Generate` |
| `Corefile` | Loads `file` + `nsec3sign`, binds `example.test:1053` so the distroless `nonroot` user can bind |

## Running

From this directory, with the custom image built (see the module
README's "Build" section):

```powershell
docker run -d --name nsec3sign-smoke `
  -p 127.0.0.1:15454:1053/udp `
  -p 127.0.0.1:15454:1053/tcp `
  -v "${pwd}:/data:ro" `
  coredns-nsec3sign-test:local `
  -conf /data/Corefile
```

Then:

```powershell
dig "@127.0.0.1" -p 15454 +dnssec host.example.test A
dig "@127.0.0.1" -p 15454 +dnssec missing.example.test A
dig "@127.0.0.1" -p 15454 +dnssec ns1.example.test AAAA
```

What you should see:

- **Positive (`host.example.test A`)**: NOERROR; answer carries the
  A RR + an RRSIG covering it; authority carries NS + RRSIG.
- **NXDOMAIN (`missing.example.test A`)**: NXDOMAIN; authority
  carries SOA + RRSIG, plus two or three NSEC3 RRs (each with its
  own RRSIG) forming the closest-encloser proof. With this tiny
  zone the next-closer and wildcard covers usually collapse into
  one NSEC3, giving two records total per RFC 5155 §7.2.1.
- **NODATA (`ns1.example.test AAAA`)**: NOERROR + empty answer;
  authority carries SOA + matching NSEC3 for `ns1` whose type
  bitmap lists `A RRSIG` but not `AAAA`, plus RRSIGs.

Tear down:

```powershell
docker rm -f nsec3sign-smoke
```

## Regenerating the key pair

The keytag is baked into the filename (BIND convention) and
referenced from `Corefile`. If you regenerate, update the
`key file` line in `Corefile` to match the new basename printed by
`keygen.go`.

```powershell
go run keygen.go example.test.
# → wrote Kexample.test.+013+<newtag>.{key,private}
```
