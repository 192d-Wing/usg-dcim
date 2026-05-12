"""Drive the DCIM render functions end-to-end and write a complete
CoreDNS bundle (Corefile + zone file + DNSSEC key files) to disk.

This is the integration smoke for the `coredns-nsec3sign` plugin —
it proves that the Python renderer in `dcim.services.dns` emits a
Corefile the Go plugin actually loads. quick-smoke verified the
plugin against a hand-written Corefile; this verifies the path
from the renderer that ships in DCIM production.

Run from this directory with:

    uv run --project ../../../../backend python render_bundle.py

then start the container and dig at it — README.md has the full
walkthrough.

No DB, no API server. The DCIM render layer is built on pure
functions that read attributes off zone / record / key objects;
we synthesize those objects with SimpleNamespace so this script
runs against the central code without bringing up Postgres."""

from __future__ import annotations

import os
import sys
from datetime import UTC, datetime
from pathlib import Path
from types import SimpleNamespace
from uuid import uuid4

# The renderer code lives under backend/src; add it to the path so
# we don't need an editable install of the dcim package just to
# generate a smoke bundle.
REPO_ROOT = Path(__file__).resolve().parents[4]
sys.path.insert(0, str(REPO_ROOT / "backend" / "src"))

from dcim.models.dns import (  # noqa: E402
    DnsKeyAlgorithm,
    DnsKeyRole,
    DnsRecordSource,
    DnsRecordType,
    DnsZoneKind,
)
from dcim.services.dns import (  # noqa: E402
    generate_dnssec_keypair,
    render_corefile_auth,
    render_dnssec_key_files,
    render_zone_file,
)

OUT_DIR = Path(__file__).resolve().parent
ZONE_NAME = "example.test"
ZONES_DIR = "/data/zones"  # path the container will see (mount target)
KEYS_DIR = "/data/keys"


def write_lf(path: Path, body: str) -> None:
    """Write `body` with LF line endings regardless of host platform.
    On Windows `Path.write_text` defaults to text mode and translates
    `\n` → `\r\n`, which miekg/dns's BIND `.private` parser rejects
    with "bad private key". The renderer emits LF; we just need to
    preserve it through the write."""
    path.write_text(body, encoding="utf-8", newline="\n")


def build_zone() -> SimpleNamespace:
    """Synthesize a zone row with NSEC3 enabled. The columns DCIM's
    migration 0028 added (nsec3_salt / nsec3_iterations / nsec3_opt_out)
    drive the renderer's choice between `dnssec` and `nsec3sign`."""
    return SimpleNamespace(
        id=uuid4(),
        name=ZONE_NAME,
        kind=DnsZoneKind.site,
        soa_mname="ns1",
        soa_rname="hostmaster",
        soa_refresh=3600,
        soa_retry=600,
        soa_expire=604800,
        soa_minimum=300,
        default_ttl=300,
        updated_at=datetime(2026, 5, 12, tzinfo=UTC),
        signed=True,
        # NSEC3 path — non-NULL salt triggers the `nsec3sign` plugin
        # block. RFC 9276 recommended defaults: empty salt, zero
        # iterations.
        nsec3_salt="aabbccdd",
        nsec3_iterations=0,
        nsec3_opt_out=False,
    )


def build_records() -> list[SimpleNamespace]:
    """Two A records and one AAAA at `host`, plus an NS at the apex
    so the zone has a non-trivial set of NSEC3 owners to chain."""
    return [
        SimpleNamespace(
            id=uuid4(), name="@", type=DnsRecordType.NS,
            ttl=None, data={"target": f"ns1.{ZONE_NAME}."},
            source=DnsRecordSource.manual,
        ),
        SimpleNamespace(
            id=uuid4(), name="ns1", type=DnsRecordType.A,
            ttl=None, data={"target": "10.0.0.1"},
            source=DnsRecordSource.manual,
        ),
        SimpleNamespace(
            id=uuid4(), name="host", type=DnsRecordType.A,
            ttl=None, data={"target": "10.0.0.2"},
            source=DnsRecordSource.manual,
        ),
        SimpleNamespace(
            id=uuid4(), name="host", type=DnsRecordType.AAAA,
            ttl=None, data={"target": "fd00::2"},
            source=DnsRecordSource.manual,
        ),
    ]


def build_zsk(zone: SimpleNamespace) -> SimpleNamespace:
    """Generate a real ECDSA-P256 ZSK via the DCIM keygen helper, then
    wrap it in a SimpleNamespace shaped like a DnsKey row so
    render_dnssec_key_files can read attributes off it."""
    material = generate_dnssec_keypair(
        zone.name, DnsKeyRole.zsk, DnsKeyAlgorithm.ecdsap256sha256,
    )
    return SimpleNamespace(
        id=uuid4(),
        zone_id=zone.id,
        role=material["role"],
        algorithm=material["algorithm"],
        private_pem=material["private_pem"],
        public_key_b64=material["public_key_b64"],
        key_tag=material["key_tag"],
        active_from=datetime(2026, 5, 12, tzinfo=UTC),
        active_until=None,
        retired_at=None,
    )


def main() -> int:
    # at-rest encryption defaults to a no-op when DCIM_DNS_DNSSEC_SECRET
    # is unset — generate_dnssec_keypair would otherwise return a
    # ciphertext that render_dnssec_key_files can't decrypt without the
    # original key. Force the env clear so the smoke runs standalone.
    os.environ.pop("DCIM_DNS_DNSSEC_SECRET", None)

    zone = build_zone()
    records = build_records()
    zsk = build_zsk(zone)

    # ── zone file ────────────────────────────────────────────────────
    zone_text = render_zone_file(zone, records)
    (OUT_DIR / "zones").mkdir(exist_ok=True)
    zone_path = OUT_DIR / "zones" / f"{ZONE_NAME}.zone"
    write_lf(zone_path, zone_text)

    # ── key files (.key + .private pair) ─────────────────────────────
    (OUT_DIR / "keys").mkdir(exist_ok=True)
    for fname, body in render_dnssec_key_files(zone, [zsk]).items():
        write_lf(OUT_DIR / "keys" / fname, body)

    # ── Corefile ─────────────────────────────────────────────────────
    # ZONES_DIR / KEYS_DIR are the in-container paths CoreDNS will see
    # after the docker volume mount. The bare 1053 port is so the
    # distroless:nonroot user can bind without elevated caps.
    corefile = render_corefile_auth(
        [zone.name],
        zones_dir=ZONES_DIR,
        keys_dir=KEYS_DIR,
        dnssec_keys_by_zone={zone.name: [f"K{zone.name}.+013+{zsk.key_tag:05d}"]},
        nsec3_params_by_zone={
            zone.name: {
                "salt": zone.nsec3_salt,
                "iterations": zone.nsec3_iterations,
                "opt_out": zone.nsec3_opt_out,
            },
        },
    )
    # CoreDNS binds on 53 by default; rewrite the port for the smoke
    # container which can't reach the privileged port.
    corefile = corefile.replace(f"{zone.name}:53 ", f"{zone.name}:1053 ")
    write_lf(OUT_DIR / "Corefile", corefile)

    print(f"wrote zone   : {zone_path}")
    print(f"wrote keys   : {OUT_DIR / 'keys'}/K{zone.name}.+013+{zsk.key_tag:05d}.{{key,private}}")
    print(f"wrote Corefile: {OUT_DIR / 'Corefile'}")
    print()
    print(f"keytag {zsk.key_tag} — boot with:")
    print("  docker run -d --name dcim-bundle-smoke \\")
    print("    -p 127.0.0.1:15555:1053/udp -p 127.0.0.1:15555:1053/tcp \\")
    print(f"    -v {OUT_DIR}:/data:ro coredns-nsec3sign-test:local \\")
    print("    -conf /data/Corefile")
    return 0


if __name__ == "__main__":
    sys.exit(main())
