# Upstream PR descriptions (drafts)

Three upstream contributions waiting on your action. Each section
below is the ready-to-paste description for the corresponding
`gh pr create` (or web UI) on the upstream repo.

Branches live on your forks:

- [`1456055067/tinkerbell` `feat/dhcpv6`](https://github.com/1456055067/tinkerbell/tree/feat/dhcpv6)
  — three commits, addresses tinkerbell/roadmap#44 + a small chart fix.
- [`1456055067/hook` `feat/dhcpv6`](https://github.com/1456055067/hook/tree/feat/dhcpv6)
  — one commit, the dhcpcd `ipfamily=` knob.

Recommended sequence: open the Hook PR first (smaller, decoupled),
then the Tinkerbell monorepo PR. Land the chart fix as its own PR
if upstream prefers granular review; otherwise it can ride along
with the DHCPv6 PR.

---

## PR 1 — `tinkerbell/hook` : `ipfamily=` cmdline knob for dhcpcd

`gh pr create --repo tinkerbell/hook --head 1456055067:feat/dhcpv6 --base main`

### Title

```
feat(network): make dhcpcd waitip family selectable via ipfamily=
```

### Body

```markdown
## Summary

Hook's first-boot dhcpcd hardcodes `waitip 4` in `files/dhcpcd.conf`,
which blocks IPv6-only sites because dhcpcd never returns from its
v4 wait. SLAAC was already enabled (`slaac private`), so v6
addressing itself worked; only the gating was wrong.

This PR adds an `ipfamily=` kernel cmdline knob, parsed in
`files/dhcp.sh` the same way the existing `interface=` and `vlan_id=`
knobs are, and translated to dhcpcd's `--waitip` CLI flag:

| Cmdline value  | dhcpcd flag         | Use case                                 |
| -------------- | ------------------- | ---------------------------------------- |
| `ipfamily=4`   | `--waitip 4`        | Existing behaviour (default, unchanged)  |
| `ipfamily=6`   | `--waitip 6`        | v6-only sites                            |
| `ipfamily=any` | `--waitip` (no arg) | Dual-stack; proceed when either succeeds |
| (unset)        | `--waitip 4`        | Preserves the prior fallback             |

The hardcoded `waitip 4` directive is removed from `dhcpcd.conf` and
replaced with a comment pointing at the script.

## Why

Pair-feature with `tinkerbell/tinkerbell` #<TBD — your DHCPv6 PR>:
once Smee serves DHCPv6 + UEFI HTTP Boot v6, Hook needs to come
up in v6-only environments without falling over on the v4 wait
loop.

## Test plan

- [x] `shellcheck files/dhcp.sh` clean.
- [ ] Boot Hook with `ipfamily=4` (default) — confirm `waitip 4`
      semantics unchanged.
- [ ] Boot Hook with `ipfamily=6` on a v6-only test network —
      confirm dhcpcd waits for SLAAC and continues to Tink Worker.
- [ ] Boot Hook with `ipfamily=any` on a dual-stack network —
      confirm boot proceeds on whichever family wins.

## Compatibility

Backwards compatible — deployments that don't pass `ipfamily=` get
exactly the prior `waitip 4` semantics.

## Refs

- Companion fork: github.com/1456055067/tinkerbell branch feat/dhcpv6.
```

---

## PR 2 — `tinkerbell/tinkerbell` : DHCPv6 + UEFI HTTP Boot v6 in Smee

`gh pr create --repo tinkerbell/tinkerbell --head 1456055067:feat/dhcpv6 --base main`

### Title

```
feat(smee): DHCPv6 + UEFI HTTP Boot v6 (roadmap#44)
```

### Body

```markdown
## Summary

Adds DHCPv6 + UEFI HTTP Boot v6 to Smee, in support of
[roadmap#44](https://github.com/tinkerbell/roadmap/issues/44).

Ported from a six-commit progression on the (now-deprecated)
`tinkerbell/smee` repo onto the consolidated monorepo. Three
commits on this branch:

1. **`fc1008a` — feat(smee): DHCPv6 + UEFI HTTP Boot v6 support.**
   The core port: PacketV6, InfoV6 + BootURL, server/dhcp6.go,
   reservation/handler_v6.go (Solicit → Advertise, Request → Reply,
   Release no-op, Option 59 BOOTFILE_URL emission), and
   smee.Config.DHCPv6 + serving goroutine in smee.Start.

2. **`97db944` — feat(cmd,smee): wire CLI flags for the DHCPv6
   listener.** Refactored `Config.DHCPv6` to `netip.Addr` +
   `uint16` so the existing `ntip.Addr` flag-value adapter works,
   added four `--dhcp-v6-{enabled,bind-addr,bind-port,bind-interface}`
   flags in `cmd/tinkerbell/flag/smee.go`.

3. **`5b53477` — fix(helm): nest additionalArgs range inside a
   single args: key.** Independent chart bug surfaced while
   testing — the template wrapped each `additionalArgs` entry in
   its own `args:` block, so multi-entry lists silently lost all
   but the last. Drop into its own PR if upstream prefers granular
   review.

## Approach (mirrors insomniacslk/dhcp namespace separation)

- Parallel types, not generic refactor. `PacketV6`/`InfoV6`/
  `DHCPv6`/`HandlerV6` sit alongside the v4 originals; no v4
  signature changes. Mirrors how the upstream `insomniacslk/dhcp`
  itself separates `dhcpv4` and `dhcpv6` packages.

- Listener uses the upstream `dhcpv6/server6.NewIPv6UDPConn` for
  the socket. Multicast joins (`ff02::1:2`, `ff05::1:3`) happen
  when bound on the well-known port — matches `server6.NewServer`'s
  behaviour without forcing operators to use the upstream wrapper.

- Stateless DHCPv6 only. No address assignment — clients use
  SLAAC. Matches the roadmap proposal and keeps Smee out of the
  IPAM business.

- UEFI HTTP Boot v6 is the target boot path. Option 59 BOOTFILE_URL
  is emitted from a `BootURL` method that mirrors v4's
  `Info.Bootfile`, URL-only (no siaddr/file split — DHCPv6 has no
  such fields). Selection cases match the v4 path's order so dual-
  stack deployments emit consistent boot targets.

- Backend lookup is MAC-keyed via DUID-LL/LLT. DUID-EN and
  DUID-UUID don't carry MACs (Windows clients hit this) — the
  handler drops gracefully and logs; DUID-keyed lookup is a
  follow-up requiring a BackendReader-API addition.

## Tests

13 new unit tests in three packages:

- `smee/internal/dhcp` — `NewInfoV6` happy/nil/DUID-UUID paths,
  `ArchV6` sentinel, `ClientTypeFromV6` prefix variants,
  `IsNetbootClientV6` error composition, `BootURL` across 7
  case branches.

- `smee/internal/dhcp/server` — end-to-end listener correctness on
  `::1`.

- `smee/internal/dhcp/handler/reservation` — real-socket round
  trips for Solicit → Advertise, Request → Reply, Release (no
  response), no-MAC drop, hardware-not-found drop, Option 59
  present-when-Netboot-enabled / absent-when-disabled.

All existing v4 tests still pass. `go vet ./...` clean; full
`./smee/...` test suite green in golang:1.25.

## Validated end-to-end

Smoke-installed on a kind cluster against
`ghcr.io/1456055067/tinkerbell:dev-dhcpv6` (the fork image built
from this branch). Confirmed via a synthetic DHCPv6 Solicit sent to
`ff02::1:2%eth0:547`:
```

{"msg":"sent DHCPv6 response","mac":"02:00:00:00:00:01",
"xid":"abcdef","interface":"eth0","msgType":"SOLICIT",
"logger":"smee","reply":"ADVERTISE"}

```

Pair-feature with [tinkerbell/hook#<TBD>](https://github.com/tinkerbell/hook/pull/<TBD>)
which adds the matching `ipfamily=` cmdline knob to Hook's dhcpcd
for v6-only first-boot networking.

## Companion

Hook companion PR: tinkerbell/hook#<your-hook-PR>

## Outstanding (not in this PR)

- DUID-keyed Hardware lookup (BackendReader-API addition).
- OTel attribute encoder polish — currently inline-minimal in
  `handler_v6.go`. A full parallel encoder under
  `internal/dhcp/otel` would match the v4 structure.
- Proxy / auto-proxy v6 modes (RFC 5970 proxy DHCPv6 has subtle
  giaddr / relay semantics that warrant their own pass).

These can land as follow-up PRs once the surface here is reviewed.
```

---

## PR 3 (optional — split or roll into PR 2)

If upstream wants the chart fix separate from the DHCPv6 work:

### Title

```
fix(helm): nest additionalArgs range inside a single args: key
```

### Body

````markdown
## Summary

`helm/tinkerbell/templates/deployment.yaml` wrapped each
`additionalArgs` entry in its own `args:` block. With more than one
entry the rendered manifest contained multiple `args:` keys on the
same container, and YAML decoding kept only the last — silently
losing the rest.

Move the `range` inside a single `args:` block guarded by an `if`:

```yaml
{{- if .Values.deployment.additionalArgs }}
args:
  {{- range .Values.deployment.additionalArgs }}
  - {{ . }}
  {{- end }}
{{- end }}
```
````

## Reproduce on main (before the fix)

```bash
helm template tinkerbell helm/tinkerbell \
  --set 'deployment.additionalArgs={--foo,--bar}' | grep -A3 args:
# Shows two separate `args:` keys; the rendered pod only sees --bar.
```

## After

```yaml
args:
  - --foo
  - --bar
```

Independent of DHCPv6 but surfaced by it — multi-entry
`additionalArgs` lists are the first real consumer.

## Test plan

- [x] `make helm-lint helm-template` clean.
- [ ] Render with empty `additionalArgs` — no `args:` key in the
      output (existing behaviour preserved via the `if` guard).
- [ ] Render with one entry — `args:` with one item.
- [ ] Render with multiple entries — `args:` with all items.

```

---

## Posting checklist

For each PR:

1. `gh repo set-default tinkerbell/<repo>` so the cross-fork
   create finds the right upstream.
2. `gh pr create --repo tinkerbell/<repo> --head 1456055067:feat/dhcpv6 --base main`,
   paste the title + body above.
3. Mark as **Draft** initially. Comment on roadmap#44 with the
   link so maintainers see it aligns with the proposal.
4. Watch CI; fix anything that goes red. The Smee fork commits
   already pass `make test` locally — most likely CI issues are
   lint / formatting / coverage gates we don't enforce locally.
5. When CI is green, flip to ready-for-review and ping the
   relevant maintainer (Jacob Weinstock per smee#433).

## After upstream merges

- Bump `deploy/helm/tinkerbell/values.yaml` to upstream's
  `ghcr.io/tinkerbell/tinkerbell` image + version.
- Replace `deployment.additionalArgs` with first-class chart keys
  if upstream lands them.
- Archive the `1456055067/tinkerbell` and `1456055067/hook` forks
  (or keep them around for future divergence).
```
