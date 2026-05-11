# Site DNS bundle

Brings up the on-site CoreDNS deployment driven by central DCIM:

- **collector** — polls `/api/v1/dns/servers/{id}/bundle` for each
  configured DnsServer, writes Corefile + zone files (+ gobgp.yaml when
  recursive) into the shared `dns-state` volume, and signals reloads.
- **coredns-auth** — authoritative for the per-site zone (and loads the
  fabric-wide bundle for resilience). Listens on the management IP.
- **coredns-recursive** — forwards `*.<fabric_apex>` to the local
  authoritative pod and everything else to operator upstreams. Listens
  on the per-fabric anycast IP via host networking.
- **gobgp** — advertises the anycast `/32` and `/128` (when set) to
  the BGP peers DCIM has configured for this site.

## Prereqs

1. Register the two `DnsServer` rows (one `auth`, one `recursive`) at this
   site in DCIM. Note the server UUIDs.
2. Register the `BgpPeer` rows for the site's leaf(s).
3. Bind the recursive `DnsServer` to those `BgpPeer` rows via the
   AnycastBgpBinding endpoint.
4. Issue the collector an API token (or provision mTLS) with
   `inventory:read` + `inventory:write`.

## Collector config snippet

Add this to the collector YAML before bringing the bundle up:

```yaml
dns:
  enabled: true
  poll_interval_seconds: 30
  api_base: https://dcim.example.mil
  servers:
    - id: <auth dns_server uuid>
      role: auth
      output_dir: /var/lib/dcim-dns/auth
      coredns_pidfile: /var/lib/dcim-dns/auth/coredns.pid
    - id: <recursive dns_server uuid>
      role: recursive
      output_dir: /var/lib/dcim-dns/recursive
      coredns_pidfile: /var/lib/dcim-dns/recursive/coredns.pid
      gobgp_pidfile: /var/lib/dcim-dns/recursive/gobgp.pid
```

## Bring it up

```bash
COLLECTOR_CONFIG=/etc/dcim/site42-collector.yaml docker compose up -d
```

Check the collector logs for `dns_bundle_applied`; that's the signal the
first render succeeded. From a client on the same VLAN:

```bash
dig @<anycast-ip> leaf-01.site42.prod.dcim.mil
```
