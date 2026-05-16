# dcim-bundle-smoke

End-to-end smoke that drives the **DCIM Python renderer** to produce
a complete CoreDNS bundle (Corefile + zone file + DNSSEC key pair),
then runs the custom CoreDNS image against it.

`quick-smoke` proved the Go plugin against a hand-rolled Corefile;
this smoke closes the loop — it proves the Python renderer in
`packages/otter/src/dcim/services/dns.py` emits a Corefile the Go plugin
actually loads, with the right key file format, the right zone file
format, and the right `nsec3sign { ... }` directive.

No Postgres. No API server. The DCIM render layer is built on pure
functions that read attributes off zone / record / key objects; the
script synthesizes those objects with `SimpleNamespace` so the same
code paths exercise without bringing up the full stack.

## Files

| File | What |
|------|------|
| `render_bundle.py` | Drives `render_zone_file` + `render_dnssec_key_files` + `render_corefile_auth` to produce a complete bundle in this directory |
| `.gitignore` | Excludes the generated artifacts (`Corefile`, `zones/`, `keys/`) so a stale run can't drift into version control |

The generated files **are not** committed — re-run `render_bundle.py`
to regenerate. Each run produces fresh ECDSA-P256 keys so the keytag
in the resulting filenames will differ.

## Running

From this directory, with the custom image built (see the module
README's "Build" section):

```powershell
# 1. Generate the bundle via the DCIM renderer.
uv run --project ../../../../../packages/otter --with cryptography `
  python render_bundle.py

# 2. Boot the custom CoreDNS against the bundle. The keytag in the
#    Corefile rotates per run — render_bundle.py prints the exact
#    docker run command at the end of its output.
docker run -d --name dcim-bundle-smoke `
  -p 127.0.0.1:15555:1053/udp -p 127.0.0.1:15555:1053/tcp `
  -v "${pwd}:/data:ro" `
  coredns-nsec3sign-test:local `
  -conf /data/Corefile

# 3. Query.
dig "@127.0.0.1" -p 15555 +dnssec host.example.test A
dig "@127.0.0.1" -p 15555 +dnssec missing.example.test A
dig "@127.0.0.1" -p 15555 +dnssec ns1.example.test AAAA

# 4. Tear down.
docker rm -f dcim-bundle-smoke
```

## What the run proves

- **Renderer output is plugin-compatible**: the `nsec3sign { ... }`
  block `render_corefile_auth` emits is accepted by the plugin's
  setup() with no parse errors.
- **Key format is interoperable**: the `.key` + `.private` pair
  `render_dnssec_key_files` writes is loadable by miekg/dns's
  `DNSKEY.ReadPrivateKey`. (We hit a CRLF gotcha here on Windows
  — `render_bundle.py` writes with explicit LF endings; the
  Linux-native production collector doesn't need this care.)
- **Zone parser sees the same zone**: the chain builder's
  `loadChain` re-parses the BIND zone we wrote and produces the
  same owner-name set DCIM signed, so denials line up with the
  zone the `file` plugin serves from.
- **Signatures validate**: every RRSIG in the response (over A,
  NS, SOA, and each NSEC3 RRset) is generated with the loaded
  ZSK and verifies via the standard `RRSIG.Verify` path a real
  resolver takes.
