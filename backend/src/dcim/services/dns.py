"""DNS render + projection helpers.

Pure functions live up here so the unit suite can drive zone-file,
Corefile, and GoBGP rendering without spinning up Postgres. The async
helpers below them touch the DB to:

  - project IPAM IPAddress.dns_name rows into A/AAAA/PTR records,
  - assemble the bundle a single DnsServer needs (Corefile + zones +
    GoBGP), and
  - hash the bundle for an etag so the collector can short-circuit.

The renderer emits BIND-format zone files because CoreDNS's `file`
plugin reads BIND. Deterministic ordering is important — the collector
diffs the bundle against its last etag, and a stable order keeps the
diff meaningful.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
from collections.abc import Iterable
from datetime import UTC, datetime, timedelta
from uuid import UUID

from sqlalchemy import func, select, update
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.dns import (
    AnycastBgpBinding,
    AnycastGroup,
    BgpPeer,
    DnsBlocklist,
    DnsBlocklistAction,
    DnsBlocklistEntry,
    DnsForwarder,
    DnsHealthCheck,
    DnsHealthCheckStatus,
    DnsKey,
    DnsKeyAlgorithm,
    DnsKeyRole,
    DnsRecord,
    DnsRecordSource,
    DnsRecordType,
    DnsServer,
    DnsServerRole,
    DnsView,
    DnsZone,
    DnsZoneKind,
)
from ..models.ipam import IPAddress, IpAddressSource, Subnet
from ..settings import get_settings
from .ipam import parse_address, parse_network


# ---------- Zone serial number ----------

def _zone_serial(zone: DnsZone) -> int:
    """SOA serial. We use the zone's `updated_at` as a Unix timestamp,
    which is monotonic per zone and fits in 32 bits until year 2106 —
    plenty for the lifespan of a DCIM deployment. Operators who want a
    YYYYMMDDnn-style serial can override later; the renderer doesn't
    care as long as it goes up."""
    ts = zone.updated_at if zone.updated_at else datetime.now(UTC)
    return int(ts.timestamp())


# ---------- BIND record line emitters ----------

def _ttl_field(record_ttl: int | None, zone_default: int) -> str:
    return str(record_ttl if record_ttl is not None else zone_default)


def _format_record_line(record: DnsRecord, zone: DnsZone) -> str:
    """Emit one BIND-format RR line. The leading name uses '@' for
    apex; everything else is the bare label (relative to the zone
    origin) so the same record file works regardless of the zone's
    parent."""
    name = record.name if record.name else "@"
    ttl = _ttl_field(record.ttl, zone.default_ttl)
    rtype = record.type.value if hasattr(record.type, "value") else record.type
    data = record.data or {}
    rdata = _format_rdata(rtype, data)
    return f"{name}\t{ttl}\tIN\t{rtype}\t{rdata}"


def _format_rdata(rtype: str, data: dict) -> str:
    """Type-specific RDATA formatting. Schemas validated the shape, so
    we can reach into `data` without defensive .get()s."""
    if rtype in ("A", "AAAA", "CNAME", "NS", "PTR"):
        return data["target"]
    if rtype == "MX":
        return f"{data['priority']} {data['target']}"
    if rtype == "TXT":
        # Single-string TXT; quote and escape inner quotes per RFC 1035.
        text = data["text"].replace("\\", "\\\\").replace('"', '\\"')
        return f'"{text}"'
    if rtype == "SRV":
        return f"{data['priority']} {data['weight']} {data['port']} {data['target']}"
    if rtype == "CAA":
        # CAA values are quoted per RFC 6844.
        value = data["value"].replace('"', '\\"')
        return f'{data["flags"]} {data["tag"]} "{value}"'
    raise ValueError(f"unknown record type {rtype}")


# Health-check probes hit operator-trusted internal targets that
# often use self-signed certs. Constant-bound so the security note
# lives in one place rather than scattered noqa comments.
_INSECURE_INTERNAL_PROBE = False


async def _probe_tcp(target: str, port: int, deadline_s: int) -> tuple[DnsHealthCheckStatus, str | None]:
    import asyncio
    if port <= 0:
        return DnsHealthCheckStatus.unhealthy, "tcp probe requires a port"
    try:
        async with asyncio.timeout(deadline_s):
            _, writer = await asyncio.open_connection(target, port)
        writer.close()
        try:
            await writer.wait_closed()
        except OSError:
            pass
        return DnsHealthCheckStatus.healthy, None
    except OSError as e:
        # TimeoutError is an OSError subclass in 3.11+; one branch
        # covers both connection failure and asyncio.timeout()
        # cancellation.
        return DnsHealthCheckStatus.unhealthy, f"tcp probe failed: {e}"[:512]


async def _probe_http(
    proto: str, target: str, port: int, path: str, deadline_s: int,
) -> tuple[DnsHealthCheckStatus, str | None]:
    import asyncio
    import httpx
    url = f"{proto}://{target}:{port}{path or '/'}"
    try:
        async with asyncio.timeout(deadline_s), httpx.AsyncClient(
            verify=_INSECURE_INTERNAL_PROBE,
            follow_redirects=False,
        ) as client:
            r = await client.get(url)
    except (httpx.HTTPError, OSError) as e:
        return DnsHealthCheckStatus.unhealthy, f"http probe failed: {e}"[:512]
    if 200 <= r.status_code < 400:
        return DnsHealthCheckStatus.healthy, None
    return DnsHealthCheckStatus.unhealthy, f"http {r.status_code}"


# ---------- DNSSEC ----------

# At-rest encryption for DnsKey.private_pem. Ciphertext is base64
# Fernet output prefixed with this tag so we can tell plaintext-
# legacy rows from encrypted ones at read time. v1 lets us migrate
# the wire format later without a schema change.
_DNSSEC_ENC_PREFIX = "enc:v1:"


def _fernet():
    """Lazy Fernet handle keyed by settings.dns_dnssec_secret. Returns
    None when the operator hasn't configured a key — callers fall
    back to plaintext storage with a structlog warning."""
    secret = get_settings().dns_dnssec_secret
    if not secret:
        return None
    from cryptography.fernet import Fernet
    return Fernet(secret.encode("ascii") if isinstance(secret, str) else secret)


def encrypt_dnssec_private_pem(pem: str) -> str:
    """Encrypt the PEM-encoded private key for at-rest storage. When
    the encryption secret isn't configured, returns the input
    unchanged — operators see plaintext in the database and a one-time
    warning that DNSSEC material is not encrypted at rest."""
    f = _fernet()
    if f is None:
        return pem
    token = f.encrypt(pem.encode("utf-8")).decode("ascii")
    return f"{_DNSSEC_ENC_PREFIX}{token}"


def decrypt_dnssec_private_pem(stored: str) -> str:
    """Inverse of encrypt — handles both encrypted and legacy
    plaintext rows. A row missing the prefix is assumed plaintext."""
    if not stored.startswith(_DNSSEC_ENC_PREFIX):
        return stored
    f = _fernet()
    if f is None:
        raise RuntimeError(
            "dns_dnssec_secret is unset but the stored key is encrypted; "
            "set DCIM_DNS_DNSSEC_SECRET to the original Fernet key",
        )
    return f.decrypt(stored[len(_DNSSEC_ENC_PREFIX):].encode("ascii")).decode("utf-8")


# Algorithm-number ↔ enum mapping per IANA DNS Security Algorithm
# Numbers (https://www.iana.org/assignments/dns-sec-alg-numbers/).
_DNSSEC_ALG_NUMBER = {
    DnsKeyAlgorithm.rsasha256: 8,
    DnsKeyAlgorithm.ecdsap256sha256: 13,
    DnsKeyAlgorithm.ed25519: 15,
}


def _key_flags(role: DnsKeyRole) -> int:
    """DNSKEY flags field: ZSK = 256, KSK = 257 (KSK adds the
    Secure Entry Point bit). RFC 4034 §2.1.1."""
    return 257 if role == DnsKeyRole.ksk else 256


def _key_tag_from_dnskey(flags: int, algorithm: int, public_key_b64: str) -> int:
    """RFC 4034 Appendix B keytag algorithm — sum of 16-bit words over
    the DNSKEY rdata, with the high byte of word zero given an extra
    multiplier."""
    import base64
    pubkey = base64.b64decode(public_key_b64)
    protocol = 3  # always 3 for DNSSEC
    rdata = (
        flags.to_bytes(2, "big")
        + bytes([protocol, algorithm])
        + pubkey
    )
    acc = 0
    for i, b in enumerate(rdata):
        acc += b << 8 if i % 2 == 0 else b
    acc += (acc >> 16) & 0xFFFF
    return acc & 0xFFFF


def generate_dnssec_keypair(
    zone_name: str, role: DnsKeyRole,
    algorithm: DnsKeyAlgorithm = DnsKeyAlgorithm.ecdsap256sha256,
) -> dict:
    """Produce one DNSSEC key (KSK or ZSK) for a zone. Returns a dict
    of the fields a DnsKey row needs. Defaults to ECDSAP256 — short
    keys, ubiquitous resolver support.

    Lazily imports `cryptography` so the import cost only lands when
    an operator actually enables DNSSEC."""
    import base64
    from cryptography.hazmat.primitives import serialization
    from cryptography.hazmat.primitives.asymmetric import ec, ed25519, rsa

    if algorithm == DnsKeyAlgorithm.ecdsap256sha256:
        priv = ec.generate_private_key(ec.SECP256R1())
        pub_numbers = priv.public_key().public_numbers()
        # RFC 6605: ECDSAP256 public key is the raw x || y, 64 bytes.
        pub_bytes = (
            pub_numbers.x.to_bytes(32, "big")
            + pub_numbers.y.to_bytes(32, "big")
        )
    elif algorithm == DnsKeyAlgorithm.ed25519:
        priv = ed25519.Ed25519PrivateKey.generate()
        pub_bytes = priv.public_key().public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
    elif algorithm == DnsKeyAlgorithm.rsasha256:
        priv = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        pub_numbers = priv.public_key().public_numbers()
        # RFC 3110 wire format: 1-byte exponent length, exponent,
        # modulus. Operators of RSA zones rarely show up but we keep
        # parity for catalog completeness.
        e = pub_numbers.e
        e_bytes = e.to_bytes((e.bit_length() + 7) // 8, "big")
        n_bytes = pub_numbers.n.to_bytes(
            (pub_numbers.n.bit_length() + 7) // 8, "big",
        )
        pub_bytes = bytes([len(e_bytes)]) + e_bytes + n_bytes
    else:
        raise ValueError(f"unsupported dnssec algorithm {algorithm}")

    pem = priv.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    ).decode("ascii")
    pub_b64 = base64.b64encode(pub_bytes).decode("ascii")
    tag = _key_tag_from_dnskey(
        flags=_key_flags(role),
        algorithm=_DNSSEC_ALG_NUMBER[algorithm],
        public_key_b64=pub_b64,
    )
    # private_pem lands in Postgres as a Fernet-encrypted blob when
    # settings.dns_dnssec_secret is configured. Without a secret,
    # plaintext flows through (with a warning at write time
    # elsewhere). zone_name is on the signature only as a hint for
    # future use (DNSKEY rdata is keyed by the zone the caller binds
    # to the DnsKey row, not by anything we return).
    _ = zone_name
    return {
        "role": role,
        "algorithm": algorithm,
        "private_pem": encrypt_dnssec_private_pem(pem),
        "public_key_b64": pub_b64,
        "key_tag": tag,
    }


def _bind_key_basename(zone_name: str, alg_number: int, key_tag: int) -> str:
    """BIND key-file basename — CoreDNS's dnssec plugin reads
    `<basename>.key` + `<basename>.private` from disk. Format is
    `K<zone>.+<alg:03d>+<tag:05d>` (RFC 5074 §5.1.1, BIND convention)."""
    fqdn = zone_name.rstrip(".") + "."
    return f"K{fqdn}+{alg_number:03d}+{key_tag:05d}"


def _bind_public_key_file(zone: DnsZone, key: DnsKey) -> str:
    """Text of the BIND `.key` file — one DNSKEY RR in presentation
    form. CoreDNS reads this to know which keys belong to the zone."""
    alg = _DNSSEC_ALG_NUMBER[key.algorithm]
    flags = _key_flags(key.role)
    fqdn = zone.name.rstrip(".") + "."
    return (
        f"; This is a {key.role.value.upper()}-type key, keyid {key.key_tag}, "
        f"for {fqdn}\n"
        f"{fqdn} IN DNSKEY {flags} 3 {alg} {key.public_key_b64}\n"
    )


def _ecdsa_private_scalar_b64(pem: str) -> str:
    """Extract the raw 32-byte private scalar from a PKCS8-PEM
    ECDSAP256 key and base64-encode it. BIND's `.private` file wants
    the bare scalar, not the PKCS8 wrapping."""
    import base64
    from cryptography.hazmat.primitives import serialization
    priv = serialization.load_pem_private_key(pem.encode("ascii"), password=None)
    scalar = priv.private_numbers().private_value
    return base64.b64encode(scalar.to_bytes(32, "big")).decode("ascii")


def _ed25519_private_raw_b64(pem: str) -> str:
    """Same idea for Ed25519 — the 32-byte private seed."""
    import base64
    from cryptography.hazmat.primitives import serialization
    priv = serialization.load_pem_private_key(pem.encode("ascii"), password=None)
    raw = priv.private_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PrivateFormat.Raw,
        encryption_algorithm=serialization.NoEncryption(),
    )
    return base64.b64encode(raw).decode("ascii")


def _rsa_private_bind_fields(pem: str) -> str:
    """Render the eight BIND `.private` fields for an RSA key. BIND
    expects base64-encoded big-endian integers for each CRT component
    (Modulus = n, PublicExponent = e, PrivateExponent = d, Prime1 = p,
    Prime2 = q, Exponent1 = dmp1, Exponent2 = dmq1, Coefficient = iqmp)."""
    import base64
    from cryptography.hazmat.primitives import serialization
    priv = serialization.load_pem_private_key(pem.encode("ascii"), password=None)
    nums = priv.private_numbers()
    pub = nums.public_numbers

    def _b64(n: int) -> str:
        # Minimum-length big-endian; BIND tolerates leading zeros but we
        # mirror what `dnssec-keygen` emits.
        return base64.b64encode(
            n.to_bytes((n.bit_length() + 7) // 8 or 1, "big"),
        ).decode("ascii")

    return (
        f"Modulus: {_b64(pub.n)}\n"
        f"PublicExponent: {_b64(pub.e)}\n"
        f"PrivateExponent: {_b64(nums.d)}\n"
        f"Prime1: {_b64(nums.p)}\n"
        f"Prime2: {_b64(nums.q)}\n"
        f"Exponent1: {_b64(nums.dmp1)}\n"
        f"Exponent2: {_b64(nums.dmq1)}\n"
        f"Coefficient: {_b64(nums.iqmp)}\n"
    )


def _bind_private_key_file(key: DnsKey) -> str:
    """Text of the BIND `.private` file. ECDSAP256 / Ed25519 ship the
    raw scalar; RSASHA256 ships the eight CRT fields."""
    alg = _DNSSEC_ALG_NUMBER[key.algorithm]
    alg_label = {
        DnsKeyAlgorithm.ecdsap256sha256: "ECDSAP256SHA256",
        DnsKeyAlgorithm.ed25519: "ED25519",
        DnsKeyAlgorithm.rsasha256: "RSASHA256",
    }[key.algorithm]
    header = (
        "Private-key-format: v1.3\n"
        f"Algorithm: {alg} ({alg_label})\n"
    )
    pem = decrypt_dnssec_private_pem(key.private_pem)
    if key.algorithm == DnsKeyAlgorithm.ecdsap256sha256:
        return header + f"PrivateKey: {_ecdsa_private_scalar_b64(pem)}\n"
    if key.algorithm == DnsKeyAlgorithm.ed25519:
        return header + f"PrivateKey: {_ed25519_private_raw_b64(pem)}\n"
    if key.algorithm == DnsKeyAlgorithm.rsasha256:
        return header + _rsa_private_bind_fields(pem)
    raise ValueError(f"unsupported algorithm {key.algorithm}")


def render_dnssec_key_files(
    zone: DnsZone, keys: Iterable[DnsKey],
) -> dict[str, str]:
    """Map of `{filename: text}` for every active key on this zone.
    Filenames carry both .key and .private suffixes; CoreDNS infers
    the pair from the basename in the Corefile's `key file` line.

    Retired keys are included so cached validators can continue to
    verify until the operator purges them — same semantics as the
    `keys` table in the UI."""
    out: dict[str, str] = {}
    fqdn = zone.name.rstrip(".") + "."
    for key in keys:
        alg = _DNSSEC_ALG_NUMBER[key.algorithm]
        base = _bind_key_basename(fqdn, alg, key.key_tag)
        out[f"{base}.key"] = _bind_public_key_file(zone, key)
        out[f"{base}.private"] = _bind_private_key_file(key)
    return out


def render_ds_records(zone: DnsZone, keys: Iterable[DnsKey]) -> list[dict]:
    """Compute DS records for the zone's KSK(s). DS = digest of the
    canonical owner name (lowercased FQDN with trailing dot) plus the
    DNSKEY rdata, hashed with SHA-256 (DS digest type 2). Operators
    upload these to the parent zone's operator."""
    import base64
    import hashlib
    out: list[dict] = []
    fqdn = zone.name.rstrip(".") + "."
    # DNS canonical wire-format encoding of the owner name.
    name_wire = b""
    for label in fqdn.split(".")[:-1]:
        name_wire += bytes([len(label)]) + label.lower().encode("ascii")
    name_wire += b"\x00"
    for key in keys:
        if key.role != DnsKeyRole.ksk or key.retired_at is not None:
            continue
        alg = _DNSSEC_ALG_NUMBER[key.algorithm]
        rdata = (
            _key_flags(key.role).to_bytes(2, "big")
            + bytes([3, alg])  # protocol=3, algorithm
            + base64.b64decode(key.public_key_b64)
        )
        digest = hashlib.sha256(name_wire + rdata).hexdigest().upper()
        out.append({
            "key_tag": key.key_tag,
            "algorithm": alg,
            "digest_type": 2,  # SHA-256
            "digest": digest,
            # BIND DS RR presentation form, ready to paste into the
            # parent zone's operator portal.
            "rr": f"{fqdn} IN DS {key.key_tag} {alg} 2 {digest}",
        })
    return out


async def rotate_zone_key(
    db: AsyncSession, zone: DnsZone, role: DnsKeyRole,
) -> tuple[DnsKey, list[DnsKey]]:
    """Generate a fresh key of the named role for `zone`, mark every
    currently-active key of that role as retired, and bump the zone's
    updated_at so SOA serial moves. Returns (new_key, retired_keys).

    Shared between the operator-driven API endpoint and the scheduled
    rotation worker. Caller commits the transaction + records the
    audit event with whatever actor info applies (Principal for the
    API, "scheduler" for the cron)."""
    active_keys = list((
        await db.execute(
            select(DnsKey).where(
                DnsKey.zone_id == zone.id,
                DnsKey.role == role,
                DnsKey.retired_at.is_(None),
            )
        )
    ).scalars().all())
    now = datetime.now(UTC)
    # Inherit the algorithm of the most-recently-active key of this
    # role; falls back to ECDSAP256 when nothing's active yet.
    prior_alg = active_keys[0].algorithm if active_keys else None
    material = generate_dnssec_keypair(
        zone.name, role,
        algorithm=prior_alg or DnsKeyAlgorithm.ecdsap256sha256,
    )
    new_key = DnsKey(
        zone_id=zone.id,
        role=material["role"],
        algorithm=material["algorithm"],
        private_pem=material["private_pem"],
        public_key_b64=material["public_key_b64"],
        key_tag=material["key_tag"],
        active_from=now,
    )
    db.add(new_key)
    for k in active_keys:
        k.retired_at = now
    # Bump zone.updated_at so the rendered SOA serial moves and the
    # bundle etag flips for downstream resolvers.
    await db.execute(
        update(DnsZone).where(DnsZone.id == zone.id).values(updated_at=func.now()),
    )
    await db.flush()
    return new_key, active_keys


async def central_health_checks_due(db: AsyncSession) -> list[DnsHealthCheck]:
    """Health checks the central worker should probe — anything
    enabled whose last_checked_at is older than interval_seconds * 1.5
    (or never). Collectors that probe on their own keep
    last_checked_at fresh, so this naturally yields the gap-filler
    set for central."""
    now = datetime.now(UTC)
    checks = list((
        await db.execute(
            select(DnsHealthCheck).where(DnsHealthCheck.enabled.is_(True))
        )
    ).scalars().all())
    due: list[DnsHealthCheck] = []
    for c in checks:
        if c.last_checked_at is None:
            due.append(c)
            continue
        # 1.5x grace lets the collector's probe race with the
        # central worker without us treating a slightly-late
        # collector probe as "stale".
        cutoff = now - timedelta(seconds=c.interval_seconds * 1.5)
        if c.last_checked_at < cutoff:
            due.append(c)
    return due


async def auto_rotate_due_zsks(db: AsyncSession) -> dict:
    """Walk every signed zone with zsk_rotation_days > 0 and rotate
    the ZSK if its current active key is older than the threshold.
    Returns a summary {checked, rotated} for the worker log."""
    rows = list((
        await db.execute(
            select(DnsZone).where(
                DnsZone.signed.is_(True),
                DnsZone.zsk_rotation_days > 0,
            )
        )
    ).scalars().all())
    rotated = 0
    now = datetime.now(UTC)
    for zone in rows:
        active_zsk = (
            await db.execute(
                select(DnsKey).where(
                    DnsKey.zone_id == zone.id,
                    DnsKey.role == DnsKeyRole.zsk,
                    DnsKey.retired_at.is_(None),
                )
                .order_by(DnsKey.active_from.desc())
                .limit(1)
            )
        ).scalar_one_or_none()
        if active_zsk is None:
            # Signed zone without an active ZSK — skip; an operator
            # has manually retired everything and rotation here
            # would mask a misconfiguration.
            continue
        age_days = (now - active_zsk.active_from).total_seconds() / 86400
        if age_days < zone.zsk_rotation_days:
            continue
        await rotate_zone_key(db, zone, DnsKeyRole.zsk)
        rotated += 1
    if rotated:
        await db.commit()
    return {"checked": len(rows), "rotated": rotated}


async def probe_health_check(
    check: DnsHealthCheck,
) -> tuple[DnsHealthCheckStatus, str | None]:
    """Run one probe and return (status, error). Pure async helper —
    caller is responsible for persisting status + last_checked_at."""
    target = str(check.target_ip).split("/", 1)[0]
    proto = check.protocol.value if hasattr(check.protocol, "value") else check.protocol
    deadline = check.timeout_seconds or 5
    if proto == "icmp":
        # Raw sockets need root; skipped in v1.
        return DnsHealthCheckStatus.unknown, "icmp not supported in worker"
    if proto == "tcp":
        return await _probe_tcp(target, check.port or 0, deadline)
    if proto in ("http", "https"):
        port = check.port or (443 if proto == "https" else 80)
        return await _probe_http(proto, target, port, check.path or "/", deadline)
    return DnsHealthCheckStatus.unknown, "unsupported protocol"


def render_zone_file(
    zone: DnsZone,
    records: Iterable[DnsRecord],
    *,
    unhealthy_check_ids: set | None = None,
    extra_lines: Iterable[str] | None = None,
) -> str:
    """Emit a BIND-format zone file. Records are sorted by (name, type)
    for diffability. Records bound to an unhealthy health check are
    skipped — the caller hands the (set of unhealthy check ids) so we
    don't query inside this pure function.

    `extra_lines` is appended verbatim after the regular records — the
    bundle-assembly path uses this to inject DS records for any
    DCIM-owned child zones, completing the DNSSEC chain of trust from
    parent → child without an operator manually copying RRs."""
    skip = unhealthy_check_ids or set()
    rec_list = sorted(
        (r for r in records if getattr(r, "health_check_id", None) not in skip),
        key=lambda r: (
            r.name or "",
            r.type.value if hasattr(r.type, "value") else r.type,
        ),
    )
    lines = [
        f"$ORIGIN {zone.name}.",
        f"$TTL {zone.default_ttl}",
        f"@\tIN\tSOA\t{zone.soa_mname}.{zone.name}. "
        f"{zone.soa_rname}.{zone.name}. (",
        f"\t\t\t{_zone_serial(zone)}\t; serial",
        f"\t\t\t{zone.soa_refresh}\t; refresh",
        f"\t\t\t{zone.soa_retry}\t; retry",
        f"\t\t\t{zone.soa_expire}\t; expire",
        f"\t\t\t{zone.soa_minimum})\t; minimum",
        "",
    ]
    for r in rec_list:
        lines.append(_format_record_line(r, zone))
    if extra_lines:
        lines.append("")
        lines.append("; --- DS records for DCIM-owned child zones ---")
        lines.extend(extra_lines)
    lines.append("")
    return "\n".join(lines)


# ---------- Corefile rendering ----------

def _view_expr(cidrs: Iterable[str]) -> str:
    """Compose CoreDNS view-plugin `expr` for a set of client CIDRs.
    `incidr(client_ip, '<cidr>')` is the expr-lang function CoreDNS
    ships with the view plugin; ORing each CIDR gives the operator's
    intent without needing a separate ACL plugin."""
    parts = [f"incidr(client_ip, '{c}')" for c in cidrs if c]
    return " || ".join(parts) if parts else "false"


def _zone_view_filename(zone_name: str, view_name: str | None) -> str:
    """Filename for a (zone, view) zone file. Default view (None)
    keeps the legacy `<zone>.zone` shape so existing operators don't
    see churn; per-view zones go to `<zone>.view-<name>.zone`."""
    if view_name is None:
        return f"{zone_name}.zone"
    return f"{zone_name}.view-{view_name}.zone"


def _render_signing_block(
    zone_name: str,
    base: str,
    keys_base: str | None,
    key_basenames: list[str],
    nsec3_params: dict | None,
) -> str:
    """Render the per-zone signing directive — either the upstream
    `dnssec` plugin (NSEC chains) or our `nsec3sign` plugin (NSEC3
    chains with on-the-fly online signing).

    Returns `""` when the zone isn't signed. When `nsec3_params` is
    set the zone is in NSEC3 mode (custom CoreDNS image required) —
    the block carries the salt + iterations + opt-out + zone-file
    path the plugin needs to build its chain at boot. Without
    `nsec3_params` we fall back to the upstream `dnssec` block so
    existing operators on the stock CoreDNS image keep working
    unchanged.
    """
    if not keys_base or not key_basenames:
        return ""
    key_lines = "\n".join(
        f"        key file {keys_base}/{kb}" for kb in sorted(key_basenames)
    )
    if nsec3_params is None:
        return f"    dnssec {{\n{key_lines}\n    }}\n"

    # NSEC3 path. The duplicate zone-file path (also in the parent
    # `file` directive) is deliberate — see the
    # coredns-nsec3sign README for the coupling-tradeoff write-up.
    salt = nsec3_params.get("salt") or ""
    iterations = int(nsec3_params.get("iterations") or 0)
    opt_out = bool(nsec3_params.get("opt_out"))
    lines = [
        "    nsec3sign {",
        key_lines,
        f"        zone file {base}/{zone_name}.zone",
        # Empty salt is the RFC 9276 recommended default; we render
        # it as the literal "" so an operator scanning the Corefile
        # can see it explicitly rather than guessing at an absent
        # directive.
        f'        salt "{salt}"',
        f"        iterations {iterations}",
    ]
    if opt_out:
        lines.append("        opt_out")
    lines.append("    }")
    return "\n".join(lines) + "\n"


def render_corefile_auth(
    zone_names: Iterable[str],
    *,
    zones_dir: str,
    keys_dir: str | None = None,
    dnssec_keys_by_zone: dict[str, list[str]] | None = None,
    nsec3_params_by_zone: dict[str, dict] | None = None,
    views_by_zone: dict[str, list[dict]] | None = None,
    dnstap_socket: str | None = None,
) -> str:
    """Authoritative Corefile: one `file` block per zone, plus health,
    prometheus, errors, log.

    `zones_dir` is the absolute path where the collector writes zone
    files inside the CoreDNS container (CoreDNS resolves relative paths
    against its cwd, not the Corefile's directory, so we always emit
    absolute paths).

    `dnssec_keys_by_zone` maps each signed zone's name to the list of
    BIND key-file basenames CoreDNS should load. When set + `keys_dir`
    is provided, the renderer emits a signing directive in that
    zone's block so CoreDNS signs responses on the fly using the
    operator's KSK + ZSK material.

    `nsec3_params_by_zone` selects per-zone NSEC3 mode. When a zone
    is in this map (value is `{salt, iterations, opt_out}`) AND has
    keys loaded, the renderer emits `nsec3sign { ... }` instead of
    `dnssec { ... }` — the custom coredns-nsec3sign image is required
    to load the plugin. Zones absent from this map fall back to the
    upstream `dnssec` plugin (NSEC chains).

    `views_by_zone` maps each zone with split-horizon configured to a
    priority-sorted list of view dicts (`{name, cidrs}`). When set,
    the renderer emits one `<zone>:53` block per view scoped by the
    CoreDNS `view` plugin's `expr` directive, plus a fallback block
    serving the default zone file for clients that don't match any
    view's CIDR list. CoreDNS picks the first matching block on the
    fly (priority order is enforced by the order we write blocks).
    """
    base = zones_dir.rstrip("/")
    keys_base = keys_dir.rstrip("/") if keys_dir else None
    dnssec_map = dnssec_keys_by_zone or {}
    nsec3_map = nsec3_params_by_zone or {}
    views_map = views_by_zone or {}
    blocks: list[str] = []
    for name in sorted(zone_names):
        dnssec_block = _render_signing_block(
            zone_name=name,
            base=base,
            keys_base=keys_base,
            key_basenames=dnssec_map.get(name) or [],
            nsec3_params=nsec3_map.get(name),
        )
        zone_views = views_map.get(name) or []
        # One block per view, then a default block as the fallthrough.
        # Operators put narrower CIDRs in the higher-priority view; we
        # emit blocks in that same order so CoreDNS's first-match
        # wins. Each view points at its own zone file so per-view
        # records don't bleed across.
        for view in zone_views:
            view_file = _zone_view_filename(name, view["name"])
            blocks.append(
                f"{name}:53 {{\n"
                f"    view {view['name']} {{\n"
                f"        expr {_view_expr(view['cidrs'])}\n"
                f"    }}\n"
                f"    file {base}/{view_file}\n"
                f"{dnssec_block}"
                f"    log\n"
                f"    errors\n"
                f"}}"
            )
        # Default / fallthrough block — always last for the zone so
        # the view-scoped blocks above win when their expr matches.
        # Only this block carries prometheus + health to avoid
        # double-registering them per view.
        # `dnstap` taps every query handled by this server-block onto
        # a UNIX socket the collector reads. `full` ships the full
        # query + response so the collector can correlate; we drop
        # the response side and only count the question name, but
        # operators sometimes want the full stream for ad-hoc
        # debugging via dnstap-read on the host.
        dnstap_line = (
            f"    dnstap {dnstap_socket} full\n"
            if dnstap_socket else ""
        )
        blocks.append(
            f"{name}:53 {{\n"
            f"    file {base}/{name}.zone\n"
            f"{dnssec_block}"
            f"{dnstap_line}"
            f"    log\n"
            f"    errors\n"
            f"    prometheus :9153\n"
            f"    health :8080\n"
            f"}}"
        )
    return "\n\n".join(blocks) + "\n"


def _pattern_to_regex(pattern: str) -> str:
    """Translate one DNS-name pattern into a regex fragment for CoreDNS
    `template match`. Only `*.` (leading-label wildcard) is supported;
    every other character is escaped so dots in domain names don't
    accidentally match anything."""
    p = pattern.strip().rstrip(".").lower()
    wildcard_head = p.startswith("*.")
    body = p[2:] if wildcard_head else p
    escaped = body.replace(".", r"\.")
    if wildcard_head:
        # Match any non-empty sequence of labels followed by the body.
        return rf"^.+\.{escaped}\.?$"
    return rf"^{escaped}\.?$"


def _render_blocklist_template(
    *, action: str, patterns: list[str],
    sink_ipv4: str | None, sink_ipv6: str | None,
) -> list[str]:
    """Compile one blocklist into zero, one, or two CoreDNS `template`
    snippets. Returns the indented lines ready to drop into the
    catch-all block — empty list if nothing renderable (no patterns,
    or sinkhole with no sink IPs)."""
    if not patterns:
        return []
    regex = "|".join(f"({_pattern_to_regex(p)})" for p in patterns)
    if action == "block":
        return [
            "    template ANY ANY {",
            f"        match {regex}",
            "        rcode NXDOMAIN",
            "    }",
        ]
    if action == "sinkhole":
        lines: list[str] = []
        if sink_ipv4:
            lines += [
                "    template IN A {",
                f"        match {regex}",
                f'        answer "{{{{ .Name }}}} 60 IN A {sink_ipv4}"',
                "    }",
            ]
        if sink_ipv6:
            lines += [
                "    template IN AAAA {",
                f"        match {regex}",
                f'        answer "{{{{ .Name }}}} 60 IN AAAA {sink_ipv6}"',
                "    }",
            ]
        return lines
    return []


def render_corefile_recursive(
    *,
    fabric_apexes: Iterable[str],
    auth_unicast_ip: str | None,
    upstream_resolvers: Iterable[str],
    conditional_forwarders: Iterable[tuple[str, list[str]]] = (),
    blocklists: Iterable[dict] = (),
) -> str:
    """Recursive Corefile: forward `*.<apex>` for each fabric apex to
    the local auth pod, route operator-configured zone patterns to
    their declared upstreams, apply blocklist `template` rules at the
    catch-all, and forward everything else to global upstreams.

    `blocklists` is an iterable of dicts of the form
    `{"action": "block"|"sinkhole", "patterns": [str, ...],
      "sink_ipv4": str|None, "sink_ipv6": str|None}` — typically built
    by the caller from DnsBlocklist + DnsBlocklistEntry rows.
    """
    upstream_list = " ".join(upstream_resolvers) or "1.1.1.1 8.8.8.8"
    blocks = []
    if auth_unicast_ip:
        # One stub-zone forward per apex — keeps internal lookups off
        # the public root. Sorted for deterministic render diffs.
        for apex in sorted(set(fabric_apexes)):
            blocks.append(
                f"{apex}:53 {{\n"
                f"    forward . {auth_unicast_ip}:53\n"
                f"    log\n"
                f"    errors\n"
                f"}}"
            )
    # Operator-defined zone forwarders. Sorted on the pattern for a
    # deterministic Corefile across renders.
    for pattern, upstreams in sorted(
        conditional_forwarders, key=lambda t: t[0],
    ):
        if not upstreams:
            continue
        forward_targets = " ".join(upstreams)
        blocks.append(
            f"{pattern}:53 {{\n"
            f"    forward . {forward_targets}\n"
            f"    log\n"
            f"    errors\n"
            f"}}"
        )
    # Blocklist `template` directives live inside the catch-all block —
    # they run at the recursive layer before any upstream forward. The
    # match regex is the OR of every pattern in the list.
    template_lines: list[str] = []
    for bl in blocklists:
        template_lines += _render_blocklist_template(
            action=bl.get("action", "block"),
            patterns=list(bl.get("patterns") or []),
            sink_ipv4=bl.get("sink_ipv4"),
            sink_ipv6=bl.get("sink_ipv6"),
        )
    catchall_lines = [".:53 {", *template_lines,
                      f"    forward . {upstream_list}",
                      "    cache 300",
                      "    log",
                      "    errors",
                      "    prometheus :9153",
                      "    health :8080",
                      "}"]
    blocks.append("\n".join(catchall_lines))
    return "\n\n".join(blocks) + "\n"


def _rpz_pattern_to_owner(pattern: str) -> str:
    """Translate a blocklist pattern to its RPZ-zone owner label.
    DCIM operators write `evil.example` or `*.evil.example`; RPZ
    expects the same shape inside the RPZ zone (the zone's $ORIGIN
    appends automatically). Trailing dots are stripped because
    they're zone-origin-relative, not absolute."""
    return pattern.strip().rstrip(".")


def render_rpz_zone(
    *,
    rpz_zone_name: str,
    action: str,
    patterns: Iterable[str],
    sink_ipv4: str | None,
    sink_ipv6: str | None,
    default_ttl: int = 60,
) -> str:
    """Render one RPZ-format zone file. Block actions emit
    `<pattern> CNAME .` (the standard NXDOMAIN-equivalent); sinkhole
    actions emit `<pattern> A <sink_ipv4>` / `AAAA <sink_ipv6>`.

    The zone is self-contained: SOA + NS at apex, one record per
    pattern. RPZ-aware resolvers (BIND, Unbound, recent Hickory
    builds) consume it as a response policy; non-RPZ resolvers just
    see a normal zone and ignore the unexpected owners."""
    pattern_list = sorted({p for p in patterns if p.strip()})
    rpz_apex = rpz_zone_name.rstrip(".")
    lines = [
        f"$ORIGIN {rpz_apex}.",
        f"$TTL {default_ttl}",
        f"@\tIN\tSOA\tns1.{rpz_apex}. hostmaster.{rpz_apex}. (",
        f"\t\t\t{int(datetime.now(UTC).timestamp())}\t; serial",
        "\t\t\t900\t; refresh",
        "\t\t\t900\t; retry",
        "\t\t\t1800\t; expire",
        f"\t\t\t{default_ttl})\t; minimum",
        f"@\t300\tIN\tNS\tns1.{rpz_apex}.",
        "",
    ]
    for pat in pattern_list:
        owner = _rpz_pattern_to_owner(pat)
        if action == "block":
            lines.append(f"{owner}\tIN\tCNAME\t.")
        elif action == "sinkhole":
            if sink_ipv4:
                lines.append(f"{owner}\tIN\tA\t{sink_ipv4}")
            if sink_ipv6:
                lines.append(f"{owner}\tIN\tAAAA\t{sink_ipv6}")
    lines.append("")
    return "\n".join(lines)


def render_hickory_recursive_config(
    *,
    fabric_apexes: Iterable[str],
    auth_unicast_ip: str | None,
    upstream_resolvers: Iterable[str],
    conditional_forwarders: Iterable[tuple[str, list[str]]] = (),
    rpz_zone_files: Iterable[tuple[str, str]] = (),
    deny_networks: Iterable[str] | None = None,
    allow_networks: Iterable[str] | None = None,
) -> str:
    """Hickory DNS recursive config (TOML). Counterpart to
    render_corefile_recursive — same inputs, different output format.

    Coverage:
      - One Forward-type zone per fabric apex pointing at the local
        auth pod.
      - One Forward-type zone per operator-configured conditional
        forwarder.
      - A recursive-store root zone that points at the catch-all
        upstreams when nothing else matches.
      - One Primary-type zone per RPZ blocklist; the response_policy
        block at the end lists them in priority order so Hickory
        (when its response-policy plugin is enabled) applies them
        before forwarding.

    Deferred (later phases):
      - DoH / DoT listeners (separate ROADMAP item).
      - Per-zone TTL overrides (not used today).

    Hickory expects newline-terminated TOML; the renderer produces
    deterministic output (sorted apex names, conditional forwarders,
    and RPZ zones) so the bundle etag is stable across runs.
    """
    upstreams = list(upstream_resolvers) or ["1.1.1.1", "8.8.8.8"]
    lines: list[str] = [
        "# Generated by DCIM — do not hand-edit. Re-render via",
        "# /api/v1/dns/servers/{id}/bundle.",
        "",
        # Hickory 0.26 takes listener config at document root — no
        # `[server]` wrapper. Phase-5 smoke against the live image
        # caught the older draft schema; keep these flat.
        'listen_addrs_ipv4 = ["0.0.0.0"]',
        'listen_addrs_ipv6 = ["::"]',
        "listen_port = 53",
        "tcp_request_timeout = 5",
        "",
    ]
    # Prometheus exporter line — only emitted when the operator has
    # opted in via `DCIM_DNS_HICKORY_PROM_METRICS=true`. The upstream
    # `hickorydns/hickory-dns` image is built without the
    # `prometheus-metrics` Cargo feature, so the field is unknown to
    # its TOML parser and would crash the resolver on load. Our
    # custom `hickory-prom` build (infra/hickory-prom) compiles it in
    # and honors the line. Operators flip the env var on after
    # swapping the compose image; the collector still needs
    # `metrics_enabled: true` on its server entry to actually scrape.
    if get_settings().dns_hickory_prom_metrics:
        port = get_settings().dns_hickory_prom_port
        lines.append(f'prometheus_listen_addr = "0.0.0.0:{port}"')
        lines.append("")
    # Order matters here: TOML tables consume every field that
    # follows them until the next table header, so the ACL lines
    # (which are top-level) MUST land before `[tls_cert]`. Otherwise
    # Hickory parses `allow_networks` as a field of `tls_cert` and
    # rejects the config on load.
    lines.extend(_hickory_acl_lines(deny_networks, allow_networks))
    lines.extend(_hickory_tls_lines(get_settings()))
    # Forward queries back to the local auth pod for each apex —
    # internal lookups never leave the site.
    if auth_unicast_ip:
        for apex in sorted(set(fabric_apexes)):
            lines += _hickory_forward_zone(
                apex, [(auth_unicast_ip, 53)],
            )
    # Operator-defined conditional forwarders — same shape, different
    # zone names + upstream lists.
    for pattern, fwd_upstreams in sorted(
        conditional_forwarders, key=lambda t: t[0],
    ):
        if not fwd_upstreams:
            continue
        lines += _hickory_forward_zone(
            pattern,
            [_split_host_port(u, 53) for u in fwd_upstreams],
        )
    # RPZ blocklists rendered as Primary zones. Hickory's response-
    # policy support uses these as a deny-/redirect-set; we list the
    # zone names in a [[response_policy]] array so the response-
    # policy plugin applies them on every recursive query.
    rpz_list = sorted(rpz_zone_files, key=lambda t: t[0])
    for rpz_zone_name, rpz_filename in rpz_list:
        lines += _hickory_primary_zone(rpz_zone_name, rpz_filename)
    if rpz_list:
        lines.append("[[response_policy]]")
        rpz_zone_array = ", ".join(
            f'"{name.rstrip(".") + "."}"' for name, _ in rpz_list
        )
        lines.append(f"zones = [{rpz_zone_array}]")
        lines.append("")
    # Catch-all when nothing else matches — routes to the configured
    # upstreams (system-wide setting or per-fabric override).
    lines += _hickory_forward_zone(
        ".",
        [_split_host_port(u, 53) for u in upstreams],
    )
    return "\n".join(lines)


_HICKORY_ZONE_HEADER = "[[zones]]"
# Hickory 0.26 zone_type variants are Primary | Secondary | External.
# Forward-resolver zones use `External` paired with a `type = "forward"`
# store underneath — the older `Forward` variant was removed.
_HICKORY_FORWARD_ZONE_TYPE = 'zone_type = "External"'
_HICKORY_FORWARD_ZONE_STORES = "[zones.stores]"
_HICKORY_FORWARD_STORE_TYPE = 'type = "forward"'


def _hickory_primary_zone(zone_name: str, file_path: str) -> list[str]:
    """Emit one Hickory Primary-zone block — used for RPZ zones loaded
    from disk. The `file = "..."` path is resolved relative to the
    Hickory process's `--zone-dir` flag (which the collector sets to
    `<output_dir>/zones`)."""
    zone_label = zone_name.rstrip(".") + "."
    return [
        _HICKORY_ZONE_HEADER,
        f'zone = "{zone_label}"',
        'zone_type = "Primary"',
        "[zones.stores]",
        'type = "file"',
        f'file = "{file_path}"',
        "",
    ]


def _hickory_tls_lines(settings) -> list[str]:
    """DoT + DoH listener config. Both modes share one `[tls_cert]`
    block — Hickory uses the same cert for TLS and HTTPS listeners.
    Hickory 0.26's `TlsCertConfig` requires `path` (cert) AND
    `private_key` (key) as separate files, hence two settings.

    Empty cert OR key path disables emission entirely so the
    renderer is safe on operators who haven't provisioned a cert
    yet. Either enable flag being true with missing paths skips
    the block silently; the alternative (rendering with stub
    paths) would crash Hickory at config-load time.

    Requires the `hickory-prom:v0.26.0-2`+ image (tls-ring +
    https-ring features compiled in). The stock upstream image
    rejects `tls_listen_port` / `https_listen_port` on parse."""
    cert = settings.dns_hickory_tls_cert_path
    key = settings.dns_hickory_tls_key_path
    dot = settings.dns_hickory_dot_enabled
    doh = settings.dns_hickory_doh_enabled
    if not cert or not key or not (dot or doh):
        return []
    lines: list[str] = []
    if dot:
        lines.append(
            f"tls_listen_port = {settings.dns_hickory_tls_listen_port}"
        )
    if doh:
        lines.append(
            f"https_listen_port = {settings.dns_hickory_https_listen_port}"
        )
        lines.append(
            f'http_endpoint = "{settings.dns_hickory_doh_path}"'
        )
    lines.append("")
    lines.append("[tls_cert]")
    lines.append(f'path = "{cert}"')
    lines.append(f'private_key = "{key}"')
    lines.append("")
    return lines


def _hickory_acl_lines(
    deny_networks: Iterable[str] | None,
    allow_networks: Iterable[str] | None,
) -> list[str]:
    """Render Hickory's top-level `deny_networks` + `allow_networks`
    CIDR lists, plus `allow_networks_strict = true` when the operator
    opted into firewall-style semantics via
    `DCIM_DNS_HICKORY_ALLOW_NETWORKS_STRICT`. Sorted CIDRs for
    deterministic bundle etags. Empty lists deliberately skip
    emission so a fat-fingered fabric edit can't lock everyone out
    by writing `allow_networks = []` into the rendered TOML — null
    vs empty needs an explicit operator intent, which today's UI
    surfaces as "leave the field blank for no restriction"."""
    deny_list = sorted(set(deny_networks or []))
    allow_list = sorted(set(allow_networks or []))
    if not deny_list and not allow_list:
        return []
    lines: list[str] = []
    if deny_list:
        lines.append(
            "deny_networks = ["
            + ", ".join(f'"{c}"' for c in deny_list)
            + "]"
        )
    if allow_list:
        lines.append(
            "allow_networks = ["
            + ", ".join(f'"{c}"' for c in allow_list)
            + "]"
        )
    # Strict-allowlist field is only meaningful when the operator
    # actually configured allow rules. Skip emission when allow is
    # empty so the rendered TOML doesn't carry the flag on a
    # deny-only fabric where it has no effect anyway.
    if allow_list and get_settings().dns_hickory_allow_networks_strict:
        lines.append("allow_networks_strict = true")
    lines.append("")
    return lines


def _hickory_forward_zone(
    zone: str, upstreams: list[tuple[str, int]],
) -> list[str]:
    """Emit one Hickory External-zone forwarder block. `upstreams` is a
    list of (host, port) tuples; the renderer normalizes the zone name
    to trailing-dot form and emits one `[[zones.stores.name_servers]]`
    array-table per upstream — Hickory 0.26 wants each name server as
    its own block with `ip = "..."` and a `connections` array carrying
    `{ port = N, protocol = { type = "udp"|"tcp" } }` entries."""
    zone_label = zone if zone == "." else zone.rstrip(".") + "."
    lines = [
        _HICKORY_ZONE_HEADER,
        f'zone = "{zone_label}"',
        _HICKORY_FORWARD_ZONE_TYPE,
        "",
        _HICKORY_FORWARD_ZONE_STORES,
        _HICKORY_FORWARD_STORE_TYPE,
        "",
    ]
    for host, port in upstreams:
        lines += _hickory_ns_entry(host, port)
    return lines


def _split_host_port(target: str, default_port: int) -> tuple[str, int]:
    """Parse "ip" or "ip:port" — used by the Hickory renderer to turn
    operator upstream strings into (host, port). IPv6 literals carry the
    standard `[::1]:5353` bracket form on input; this function strips
    the brackets so the output is always a bare IP suitable for
    Hickory's `ip = "..."` field."""
    if target.startswith("["):
        # IPv6 literal — `[::1]:5353` or just `[::1]`.
        end = target.find("]")
        if end == -1:
            return target, default_port
        host = target[1:end]
        rest = target[end + 1:]
        if rest.startswith(":"):
            try:
                return host, int(rest[1:])
            except ValueError:
                return host, default_port
        return host, default_port
    if ":" in target and target.count(":") == 1:
        host, _, port = target.partition(":")
        try:
            return host, int(port)
        except ValueError:
            return target, default_port
    return target, default_port


def _hickory_ns_entry(host: str, port: int) -> list[str]:
    """Render one upstream as a `[[zones.stores.name_servers]]` block.
    Emits both UDP and TCP connections on the same port so the
    forwarder falls back to TCP when a response is truncated. `ip` is
    the bare address — IPv6 goes in unbracketed (Hickory parses it as
    an IpAddr, not a socket-addr)."""
    return [
        "[[zones.stores.name_servers]]",
        f'ip = "{host}"',
        "connections = [",
        f'    {{ port = {port}, protocol = {{ type = "udp" }} }},',
        f'    {{ port = {port}, protocol = {{ type = "tcp" }} }},',
        "]",
        "",
    ]


# ---------- GoBGP rendering ----------

def render_gobgp_config(
    *,
    server: DnsServer,
    peers: Iterable[BgpPeer],
    peer_asns: dict,
    local_asn: int,
) -> dict:
    """GoBGP YAML config (returned as a dict; collector serializes to
    YAML on disk).

    `local_asn` is the originating AS for every DNS anycast
    announcement — pulled from settings.dns_anycast_originate_asn so
    every recursive site advertises from the same origin (default
    4200000000). The BgpPeer.local_asn_id on individual peers is
    informational here; we deliberately don't read it so a typo in a
    catalog row can't desync sites.

    `peer_asns` maps BgpPeer.peer_asn_id → ASN integer, resolved by the
    caller against the ASN catalog. Missing entries (dangling FK) get
    0 — GoBGP will reject the config, surfacing the bad reference at
    render time instead of silently advertising into nowhere."""
    peer_list = list(peers)
    neighbors = [
        {
            "config": {
                "neighbor-address": str(p.peer_ip).split("/", 1)[0],
                "peer-as": peer_asns.get(p.peer_asn_id, 0),
            },
        }
        for p in peer_list
    ]
    # gobgpd's config file schema only accepts `global`, `neighbors`,
    # `defined-sets`, and `policy-definitions`. Prefix advertisement
    # (the anycast /32 and /128) is a runtime operation against the
    # gobgp gRPC API — `gobgp global rib add <prefix>` — driven by the
    # collector or a sidecar once the session is up. The anycast
    # group's IPs are not surfaced in the gobgpd config file.
    return {
        "global": {
            "config": {
                "as": local_asn,
                "router-id": str(server.unicast_ip).split("/", 1)[0],
            },
        },
        "neighbors": neighbors,
        "defined-sets": {},
        "policy-definitions": [],
    }


# ---------- Bundle assembly ----------

def _filename_for_zone(zone_name: str) -> str:
    """Drop the trailing dot if the operator typed a fully-qualified
    name; CoreDNS doesn't care but disk filenames do."""
    return zone_name.rstrip(".")


def bundle_etag(
    corefile: str,
    zones: dict[str, str],
    gobgp: dict | None,
    *,
    key_files: dict[str, str] | None = None,
    anycast_prefixes: list[str] | None = None,
) -> str:
    """Stable hash over the bundle so the collector can skip no-op
    pulls. Sorted JSON keeps the etag deterministic across renders.
    DNSSEC key files are folded in too so the collector re-applies
    after a key rotation. Anycast prefixes are folded in so flipping
    an `anycast_ipv4` value re-keys the bundle even when the gobgp
    YAML (which doesn't carry the prefixes) hasn't changed."""
    h = hashlib.sha256()
    h.update(corefile.encode("utf-8"))
    h.update(b"\x00")
    for k in sorted(zones):
        h.update(k.encode("utf-8"))
        h.update(b"\x00")
        h.update(zones[k].encode("utf-8"))
        h.update(b"\x00")
    if gobgp is not None:
        h.update(json.dumps(gobgp, sort_keys=True).encode("utf-8"))
    if key_files:
        h.update(b"\x01")  # discriminator vs the zone-name stream
        for k in sorted(key_files):
            h.update(k.encode("utf-8"))
            h.update(b"\x00")
            h.update(key_files[k].encode("utf-8"))
            h.update(b"\x00")
    if anycast_prefixes:
        h.update(b"\x02")  # discriminator vs the zone/key streams
        for p in sorted(anycast_prefixes):
            h.update(p.encode("utf-8"))
            h.update(b"\x00")
    return h.hexdigest()[:32]


async def _zones_for_server(db: AsyncSession, server: DnsServer) -> list[DnsZone]:
    """An auth pod loads every zone in its fabric (apex + all sites)
    for resilience — internal lookups never have to leave the box.
    A recursive pod loads no zone files; it's a forwarder only."""
    if server.role != DnsServerRole.auth:
        return []
    rows = (
        await db.execute(
            select(DnsZone).where(DnsZone.fabric_id == server.fabric_id)
        )
    ).scalars().all()
    return list(rows)


async def _records_by_zone(
    db: AsyncSession, zones: Iterable[DnsZone],
) -> dict[UUID, list[DnsRecord]]:
    zone_ids = [z.id for z in zones]
    if not zone_ids:
        return {}
    rows = (
        await db.execute(select(DnsRecord).where(DnsRecord.zone_id.in_(zone_ids)))
    ).scalars().all()
    grouped: dict[UUID, list[DnsRecord]] = {z.id: [] for z in zones}
    for r in rows:
        grouped[r.zone_id].append(r)
    return grouped


async def _views_and_zone_files_for_split_horizon(
    db: AsyncSession,
    zones: Iterable[DnsZone],
    records_by_zone: dict[UUID, list[DnsRecord]],
    unhealthy_check_ids: set,
) -> tuple[dict[str, str], dict[str, list[dict]]]:
    """Build (zone_files, views_by_zone) for any zone whose records
    bind to a DnsView. Returns:

      - zone_files: filename -> zone-file text. Includes both per-view
        files (records matching a specific view + null-view records)
        AND a default file (only null-view records) — the latter is
        served to clients that don't match any view's CIDR list.
      - views_by_zone: zone-name -> list of view dicts ordered by
        priority (lower first) for the Corefile renderer.

    Zones without any view-bound records yield neither and fall back
    to the legacy single-file render handled by the caller.
    """
    zone_files: dict[str, str] = {}
    views_by_zone: dict[str, list[dict]] = {}
    # Find which zones actually have view-bound records — for zones
    # with none we let the caller handle the legacy render path.
    fabric_ids = {z.fabric_id for z in zones}
    if not fabric_ids:
        return zone_files, views_by_zone
    all_views = list((
        await db.execute(
            select(DnsView)
            .where(DnsView.fabric_id.in_(fabric_ids))
            .order_by(DnsView.priority.asc(), DnsView.name.asc())
        )
    ).scalars().all())
    views_by_fabric: dict[UUID, list[DnsView]] = {}
    for v in all_views:
        views_by_fabric.setdefault(v.fabric_id, []).append(v)
    for z in zones:
        recs = records_by_zone.get(z.id, [])
        if not any(r.view_id is not None for r in recs):
            continue
        zone_views = views_by_fabric.get(z.fabric_id, [])
        if not zone_views:
            continue  # records reference views but none exist — render default only
        # Default file: only null-view records (the "served to
        # everyone else" answer set).
        default_records = [r for r in recs if r.view_id is None]
        zone_files[_zone_view_filename(z.name, None)] = render_zone_file(
            z, default_records, unhealthy_check_ids=unhealthy_check_ids,
        )
        # Per-view files: records matching that view OR null-view
        # (so a view sees both its overrides and the default rrset).
        per_zone_views: list[dict] = []
        for view in zone_views:
            view_recs = [
                r for r in recs
                if r.view_id == view.id or r.view_id is None
            ]
            zone_files[_zone_view_filename(z.name, view.name)] = render_zone_file(
                z, view_recs, unhealthy_check_ids=unhealthy_check_ids,
            )
            per_zone_views.append({
                "name": view.name,
                "cidrs": list(view.match_cidrs or []),
            })
        views_by_zone[z.name] = per_zone_views
    return zone_files, views_by_zone


async def _dnssec_artifacts_for_zones(
    db: AsyncSession, zones: Iterable[DnsZone],
) -> tuple[dict[str, str], dict[str, list[str]], dict[str, dict]]:
    """Return (key_files, dnssec_keys_by_zone, nsec3_params_by_zone)
    for every signed zone in the iterable.

    - `key_files` maps filename → text (both .key and .private
      members per key).
    - `dnssec_keys_by_zone` maps zone name to the basenames CoreDNS's
      signing directive should load.
    - `nsec3_params_by_zone` maps the subset of zones with
      `nsec3_salt IS NOT NULL` to `{salt, iterations, opt_out}` —
      these get rendered with `nsec3sign` instead of `dnssec`.
      Zones absent from this dict (NULL salt) stay on the upstream
      `dnssec` plugin (NSEC chains).

    All three dicts are empty when no zone is signed.
    """
    signed = [z for z in zones if z.signed]
    if not signed:
        return {}, {}, {}
    keys = list((
        await db.execute(
            select(DnsKey).where(DnsKey.zone_id.in_([z.id for z in signed]))
        )
    ).scalars().all())
    keys_by_zone: dict[UUID, list[DnsKey]] = {}
    for k in keys:
        keys_by_zone.setdefault(k.zone_id, []).append(k)
    files: dict[str, str] = {}
    basenames_by_zone: dict[str, list[str]] = {}
    nsec3_params_by_zone: dict[str, dict] = {}
    for z in signed:
        zone_keys = keys_by_zone.get(z.id, [])
        if not zone_keys:
            continue
        zone_files = render_dnssec_key_files(z, zone_keys)
        files.update(zone_files)
        # Strip the .key suffix to get the basename CoreDNS expects in
        # `key file <basename>` (it appends .key + .private itself).
        basenames_by_zone[z.name] = sorted(
            fn[:-4] for fn in zone_files if fn.endswith(".key")
        )
        if z.nsec3_salt is not None:
            nsec3_params_by_zone[z.name] = {
                "salt": z.nsec3_salt,
                "iterations": z.nsec3_iterations,
                "opt_out": z.nsec3_opt_out,
            }
    return files, basenames_by_zone, nsec3_params_by_zone


async def _local_auth_unicast_ip(db: AsyncSession, server: DnsServer) -> str | None:
    """For a recursive pod, find the auth pod at the same site so we
    can stub-zone the fabric apex back to it."""
    auth = (
        await db.execute(
            select(DnsServer).where(
                DnsServer.site_id == server.site_id,
                DnsServer.role == DnsServerRole.auth,
            )
        )
    ).scalar_one_or_none()
    return str(auth.unicast_ip).split("/", 1)[0] if auth else None


async def _fabric_forwarders(
    db: AsyncSession, fabric_id: UUID,
) -> list[tuple[str, list[str]]]:
    """Conditional forwarders configured for this fabric — emitted as
    extra Corefile blocks ahead of the catch-all `.:53`."""
    rows = (
        await db.execute(
            select(DnsForwarder.zone_pattern, DnsForwarder.upstreams)
            .where(DnsForwarder.fabric_id == fabric_id)
        )
    ).all()
    return [(p, list(u or [])) for p, u in rows]


async def _fabric_blocklists(
    db: AsyncSession, fabric_id: UUID,
) -> list[dict]:
    """Enabled blocklists for this fabric, each shaped for the
    Corefile renderer. Patterns are gathered in one extra query so the
    n+1 doesn't grow with the number of blocklists."""
    lists = list((
        await db.execute(
            select(DnsBlocklist).where(
                DnsBlocklist.fabric_id == fabric_id,
                DnsBlocklist.enabled.is_(True),
            )
        )
    ).scalars().all())
    if not lists:
        return []
    ids = [bl.id for bl in lists]
    entry_rows = (
        await db.execute(
            select(DnsBlocklistEntry.blocklist_id, DnsBlocklistEntry.pattern)
            .where(DnsBlocklistEntry.blocklist_id.in_(ids))
        )
    ).all()
    patterns_by_id: dict[UUID, list[str]] = {bid: [] for bid in ids}
    for bid, pat in entry_rows:
        patterns_by_id[bid].append(pat)
    return [
        {
            "action": bl.action.value,
            "patterns": sorted(patterns_by_id[bl.id]),
            "sink_ipv4": (
                str(bl.sink_ipv4).split("/", 1)[0]
                if bl.sink_ipv4 is not None else None
            ),
            "sink_ipv6": (
                str(bl.sink_ipv6).split("/", 1)[0]
                if bl.sink_ipv6 is not None else None
            ),
        }
        for bl in lists
    ]


async def _recursive_upstreams_for_fabric(
    db: AsyncSession, fabric_id: UUID,
) -> list[str]:
    """Fabric-level override beats the system-wide setting. Local
    import keeps the models.ipam ↔ services.dns dependency one-way."""
    from ..models.ipam import Fabric  # noqa: PLC0415 — avoid import cycle
    fabric = await db.get(Fabric, fabric_id)
    if fabric is not None and fabric.dns_recursive_upstreams:
        return list(fabric.dns_recursive_upstreams)
    return list(get_settings().dns_recursive_upstreams)


async def _fabric_apex_names(db: AsyncSession, fabric_id: UUID) -> list[str]:
    """Every apex zone bound to this fabric. Multiple are allowed — the
    recursive Corefile emits a stub-forward per apex."""
    rows = (
        await db.execute(
            select(DnsZone.name).where(
                DnsZone.fabric_id == fabric_id, DnsZone.kind == DnsZoneKind.apex,
            )
        )
    ).scalars().all()
    return list(rows)


async def _bgp_for_server(
    db: AsyncSession, server: DnsServer,
) -> tuple[list[BgpPeer], dict, AnycastGroup | None]:
    """Resolve the BGP peers a recursive server advertises to + the
    ASN-id → integer map for those peers + the server's anycast group.
    Returns ([], {}, None) for auth servers."""
    if server.role != DnsServerRole.recursive or server.anycast_group_id is None:
        return [], {}, None
    peer_ids = (
        await db.execute(
            select(AnycastBgpBinding.bgp_peer_id).where(
                AnycastBgpBinding.dns_server_id == server.id,
            )
        )
    ).scalars().all()
    peers: list[BgpPeer] = []
    peer_asns: dict = {}
    if peer_ids:
        peers = list((
            await db.execute(select(BgpPeer).where(BgpPeer.id.in_(peer_ids)))
        ).scalars().all())
        # One query to pull every ASN catalog row a peer points at.
        # The render function only needs peer_asn_id (the downstream
        # router AS); local_asn_id is intentionally ignored — the DNS
        # anycast origin AS is a system constant from settings.
        asn_ids = {p.peer_asn_id for p in peers}
        if asn_ids:
            from ..models.bgp import Asn  # avoid top-level cycle
            rows = (
                await db.execute(select(Asn).where(Asn.id.in_(asn_ids)))
            ).scalars().all()
            peer_asns = {row.id: row.asn for row in rows}
    anycast = await db.get(AnycastGroup, server.anycast_group_id)
    return peers, peer_asns, anycast


async def _engine_for_server(db: AsyncSession, server: DnsServer) -> str:
    """Engine hint surfaced on the bundle response. Auth pods always
    render Corefile (CoreDNS); recursive pods follow the fabric's
    recursive_engine selector. The collector reads this to pick the
    on-disk filename + reload signal."""
    if server.role != DnsServerRole.recursive:
        return "coredns"
    from ..models.ipam import Fabric, RecursiveDnsEngine  # noqa: PLC0415
    fabric = await db.get(Fabric, server.fabric_id)
    if fabric and fabric.recursive_engine == RecursiveDnsEngine.hickory:
        return "hickory"
    return "coredns"


async def _render_recursive_config(
    db: AsyncSession, server: DnsServer,
) -> tuple[str, dict[str, str]]:
    """Assemble the recursive-side config text (Corefile or Hickory
    TOML) for one server, plus any auxiliary zone files the resolver
    needs (RPZ on Hickory). Returns (config_text, rpz_zones_dict).
    Empty dict for CoreDNS — its blocklists live inline in the
    Corefile, not as separate zone files."""
    from ..models.ipam import Fabric, RecursiveDnsEngine  # noqa: PLC0415

    apex_names = await _fabric_apex_names(db, server.fabric_id)
    local_auth_ip = await _local_auth_unicast_ip(db, server)
    forwarders = await _fabric_forwarders(db, server.fabric_id)
    upstreams = await _recursive_upstreams_for_fabric(db, server.fabric_id)
    blocklists = await _fabric_blocklists(db, server.fabric_id)
    fabric = await db.get(Fabric, server.fabric_id)
    engine = (
        fabric.recursive_engine if fabric else RecursiveDnsEngine.coredns
    )
    if engine == RecursiveDnsEngine.hickory:
        rpz_zones, rpz_refs = _build_rpz_artifacts(blocklists)
        config_text = render_hickory_recursive_config(
            fabric_apexes=apex_names,
            auth_unicast_ip=local_auth_ip,
            upstream_resolvers=upstreams,
            conditional_forwarders=forwarders,
            rpz_zone_files=rpz_refs,
            deny_networks=fabric.dns_deny_networks if fabric else None,
            allow_networks=fabric.dns_allow_networks if fabric else None,
        )
        return config_text, rpz_zones
    config_text = render_corefile_recursive(
        fabric_apexes=apex_names,
        auth_unicast_ip=local_auth_ip,
        upstream_resolvers=upstreams,
        conditional_forwarders=forwarders,
        blocklists=blocklists,
    )
    return config_text, {}


def _build_rpz_artifacts(
    blocklists: Iterable[dict],
) -> tuple[dict[str, str], list[tuple[str, str]]]:
    """Render one RPZ zone file per blocklist + return the (zone-name,
    filename) pairs the Hickory config needs to reference them.
    Sinkhole blocklists with neither v4 nor v6 sink IPs are skipped —
    they wouldn't produce any usable records."""
    zones: dict[str, str] = {}
    refs: list[tuple[str, str]] = []
    for i, bl in enumerate(blocklists):
        patterns = list(bl.get("patterns") or [])
        if not patterns:
            continue
        action = bl.get("action", "block")
        sink_v4 = bl.get("sink_ipv4")
        sink_v6 = bl.get("sink_ipv6")
        if action == "sinkhole" and not sink_v4 and not sink_v6:
            continue
        # Predictable name + filename so etag stability holds across
        # renders even when blocklist ordering changes upstream.
        rpz_name = f"bl-{i:03d}.rpz.dcim.local"
        rpz_filename = f"{rpz_name}.zone"
        zones[rpz_filename] = render_rpz_zone(
            rpz_zone_name=rpz_name,
            action=action,
            patterns=patterns,
            sink_ipv4=sink_v4,
            sink_ipv6=sink_v6,
        )
        refs.append((rpz_name, rpz_filename))
    return zones, refs


def _dnstap_socket_for(server: DnsServer) -> str | None:
    """Path the resolver writes dnstap frames to (and the collector
    tails) for this server, or None when dnstap is globally disabled.

    Lives alongside zones/keys on the shared dns-state volume so both
    the resolver container and the collector see it at the same path.
    Only meaningful for CoreDNS auth pods today — Hickory doesn't
    support dnstap at all, so callers gate on `server.role`."""
    settings = get_settings()
    if not settings.dns_dnstap_enabled:
        return None
    return (
        f"/var/lib/dcim-dns/{server.role.value}/"
        f"{settings.dns_dnstap_socket_filename}"
    )


def _bundle_dnstap_path(server: DnsServer, engine: str) -> str | None:
    """Path of the dnstap socket reported in the bundle response.
    Hickory doesn't support dnstap, and only auth pods emit it today,
    so we limit the return path to CoreDNS auth and let the collector
    skip its reader loop on null."""
    if server.role != DnsServerRole.auth or engine != "coredns":
        return None
    return _dnstap_socket_for(server)


def _anycast_prefixes(anycast: AnycastGroup | None) -> list[str]:
    """v4 `/32` + v6 `/128` strings the collector should advertise
    via gobgpd's gRPC. Empty when the server has no anycast group
    bound (auth pods, or recursives without a group). Both families
    ship when set so a fabric with dual-stack lands on a single
    recursive without per-family fan-out."""
    if anycast is None:
        return []
    out: list[str] = []
    if anycast.anycast_ipv4:
        out.append(f"{anycast.anycast_ipv4}/32")
    if anycast.anycast_ipv6:
        out.append(f"{anycast.anycast_ipv6}/128")
    return out


async def _child_ds_lines_by_parent(
    db: AsyncSession, zones: Iterable[DnsZone],
) -> dict[UUID, list[str]]:
    """For every parent zone in `zones`, return the DS-record lines
    that should be embedded in its rendered zone file — one set of DS
    RRs per DCIM-owned child zone that is a direct subdomain.

    "Direct subdomain" means the child's name strips to the parent's
    name after removing exactly one leading label. Multi-label nested
    delegation (`a.b.example.com` → `example.com`) is intentionally
    not supported: it would require the operator to also create the
    intermediate `b.example.com` zone for the chain to be reachable.

    Only signed children contribute records — an unsigned child has
    nothing to delegate. Retired KSKs are skipped so the parent stays
    in sync with the child's current key material across rotations."""
    zone_list = list(zones)
    by_name = {z.name.rstrip(".").lower(): z for z in zone_list}
    out: dict[UUID, list[str]] = {z.id: [] for z in zone_list}
    for child in zone_list:
        if not getattr(child, "signed", False):
            continue
        child_name = child.name.rstrip(".").lower()
        parent_name = child_name.split(".", 1)[1] if "." in child_name else ""
        parent = by_name.get(parent_name) if parent_name else None
        if parent is None or parent.id == child.id:
            continue
        keys = (
            await db.execute(
                select(DnsKey).where(DnsKey.zone_id == child.id)
            )
        ).scalars().all()
        for ds in render_ds_records(child, keys):
            out[parent.id].append(ds["rr"])
    return out


async def render_bundle_for_server(db: AsyncSession, server: DnsServer) -> dict:
    """One call returns the complete bundle a single server needs:
    Corefile, zone files, optional GoBGP config, etag.

    Bundle assembly is per-role:
      auth      -> zones for the whole fabric + authoritative Corefile.
      recursive -> empty zones, recursive Corefile (with stub for the
                   fabric apex), GoBGP config + anycast advertisement.
    """
    # Default everything that's only populated on one branch — keeps
    # the bundle shape stable across roles and the return statement
    # safe regardless of which branch ran.
    key_files: dict[str, str] = {}
    if server.role == DnsServerRole.auth:
        zones = await _zones_for_server(db, server)
        records_by_zone = await _records_by_zone(db, zones)
        # Health-check filter: every record bound to a check in this
        # set is silently dropped from the rendered zone, so resolvers
        # downstream stop handing it out until the check recovers.
        unhealthy = {
            row[0] for row in (await db.execute(
                select(DnsHealthCheck.id).where(
                    DnsHealthCheck.fabric_id == server.fabric_id,
                    DnsHealthCheck.status == DnsHealthCheckStatus.unhealthy,
                    DnsHealthCheck.enabled.is_(True),
                )
            )).all()
        }
        # Split-horizon: zones with view-bound records get one zone
        # file per view (filtered to that view's records + null-view
        # defaults) plus a default file. Zones without view-bound
        # records use the legacy single-file path.
        split_files, views_by_zone = await _views_and_zone_files_for_split_horizon(
            db, zones, records_by_zone, unhealthy,
        )
        child_ds_lines = await _child_ds_lines_by_parent(db, zones)
        zone_files: dict[str, str] = {}
        for z in zones:
            if z.name in views_by_zone:
                # split_files already contains both the default and
                # per-view files keyed by their proper filenames.
                # Split-horizon + delegation is unusual; the DS
                # records would need to land in every view's file.
                # Punt on that combo until an operator asks for it.
                continue
            zone_files[_filename_for_zone(z.name)] = render_zone_file(
                z, records_by_zone.get(z.id, []),
                unhealthy_check_ids=unhealthy,
                extra_lines=child_ds_lines.get(z.id) or None,
            )
        zone_files.update(split_files)
        key_files, dnssec_keys_by_zone, nsec3_params_by_zone = (
            await _dnssec_artifacts_for_zones(db, zones)
        )
        # Path matches the site-dns compose layout: the dns-state
        # volume is mounted at /var/lib/dcim-dns in both the collector
        # and CoreDNS containers, and the collector writes zones to
        # /var/lib/dcim-dns/<role>/zones/.
        zones_dir_path = f"/var/lib/dcim-dns/{server.role.value}/zones"
        keys_dir_path = f"/var/lib/dcim-dns/{server.role.value}/keys"
        dnstap_socket_path = _dnstap_socket_for(server)
        corefile = render_corefile_auth(
            (z.name for z in zones),
            zones_dir=zones_dir_path,
            keys_dir=keys_dir_path if dnssec_keys_by_zone else None,
            dnssec_keys_by_zone=dnssec_keys_by_zone or None,
            nsec3_params_by_zone=nsec3_params_by_zone or None,
            views_by_zone=views_by_zone or None,
            dnstap_socket=dnstap_socket_path,
        )
        gobgp: dict | None = None
        anycast_prefixes: list[str] = []
    else:
        corefile, rpz_zones_recursive = await _render_recursive_config(db, server)
        # Hickory loads RPZ zones from the same `zones/` directory the
        # collector materializes; folding them into zone_files keeps
        # the bundle shape uniform across engines (CoreDNS just won't
        # have RPZ entries because its renderer returns an empty
        # dict).
        zone_files = dict(rpz_zones_recursive)
        # `anycast` gates whether we emit a gobgpd config at all — the
        # group's IPs don't flow into the yaml (route advertisement is
        # a runtime gRPC operation), but a server without a bound
        # anycast group has no reason to run gobgp.
        peers, peer_asns, anycast = await _bgp_for_server(db, server)
        gobgp = render_gobgp_config(
            server=server, peers=peers, peer_asns=peer_asns,
            local_asn=get_settings().dns_anycast_originate_asn,
        ) if anycast else None
        anycast_prefixes = _anycast_prefixes(anycast)
    engine = await _engine_for_server(db, server)
    etag = bundle_etag(
        corefile, zone_files, gobgp,
        key_files=key_files, anycast_prefixes=anycast_prefixes,
    )
    return {
        "engine": engine,
        "corefile": corefile,
        "zones": zone_files,
        "gobgp": gobgp,
        "key_files": key_files,
        "etag": etag,
        "dnstap_socket": _bundle_dnstap_path(server, engine),
        "anycast_prefixes": anycast_prefixes,
    }


# ---------- IPAM → DNS projection ----------

def _ptr_owner(addr: str) -> str:
    """Compute the full .in-addr.arpa / .ip6.arpa name for a given
    INET address (no prefix length). This is the PTR's owner name."""
    a = parse_address(addr)
    if isinstance(a, ipaddress.IPv4Address):
        return ".".join(reversed(str(a).split("."))) + ".in-addr.arpa"
    nibbles = a.exploded.replace(":", "")
    return ".".join(reversed(nibbles)) + ".ip6.arpa"


def reverse_zone_name(addr: str) -> str:
    """The reverse-zone *origin* (not the PTR's owner) that the given
    address belongs to. /24 for IPv4, /64 for IPv6 — the classful or
    nibble-aligned cuts that don't need RFC 2317 CNAME indirection.
    Any subnet finer than that just shares the upstream /24 or /64."""
    a = parse_address(addr)
    if isinstance(a, ipaddress.IPv4Address):
        octets = str(a).split(".")
        return ".".join(reversed(octets[:3])) + ".in-addr.arpa"
    nibbles = a.exploded.replace(":", "")
    # /64 = first 16 nibbles, reversed.
    return ".".join(reversed(nibbles[:16])) + ".ip6.arpa"


def _ptr_label_in(owner: str, zone_origin: str) -> str:
    """Strip the zone origin off a PTR owner to get the relative label
    we store in DnsRecord.name. Caller guarantees `owner` ends with
    `.` + `zone_origin`."""
    suffix = "." + zone_origin
    if owner.endswith(suffix):
        return owner[: -len(suffix)]
    return owner


async def _get_or_create_reverse_zone(
    db: AsyncSession, *, name: str, fabric_id: UUID, site_id: UUID,
) -> DnsZone:
    """Find the reverse DnsZone for `name` in this (fabric, site), or
    create it. Reverse zones get the same SOA defaults as freshly-
    created site zones."""
    existing = (
        await db.execute(
            select(DnsZone).where(
                DnsZone.kind == DnsZoneKind.reverse,
                DnsZone.fabric_id == fabric_id,
                DnsZone.site_id == site_id,
                DnsZone.name == name,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        return existing
    z = DnsZone(
        name=name,
        kind=DnsZoneKind.reverse,
        fabric_id=fabric_id,
        site_id=site_id,
    )
    db.add(z)
    await db.flush()
    return z


async def _drop_ipam_records_for_site(
    db: AsyncSession, forward_zone: DnsZone, reverse_zones: list[DnsZone],
) -> int:
    """Delete every projector-owned record (source=ipam or =ddns) in
    the forward zone + each reverse zone. Manual records stay put."""
    zone_ids = [forward_zone.id, *(z.id for z in reverse_zones)]
    existing = (
        await db.execute(
            select(DnsRecord).where(
                DnsRecord.zone_id.in_(zone_ids),
                DnsRecord.source.in_(
                    (DnsRecordSource.ipam, DnsRecordSource.ddns),
                ),
            )
        )
    ).scalars().all()
    for r in existing:
        await db.delete(r)
    await db.flush()
    return len(existing)


def _forward_label_for(dns_name: str, zone_name: str) -> str:
    """The bare label we store in DnsRecord.name for the forward A/AAAA
    row — strips the zone suffix if the operator wrote an FQDN, or
    collapses to `@` if the name *is* the zone origin."""
    suffix = "." + zone_name
    if dns_name.endswith(suffix):
        return dns_name[: -len(suffix)]
    if dns_name == zone_name:
        return "@"
    return dns_name


def _ptr_target_for(ip: IPAddress, forward_zone: DnsZone) -> str:
    """Prefer the operator's dns_name if it's already absolute,
    otherwise reassemble label + forward-zone origin into an FQDN."""
    return (
        ip.dns_name
        if ip.dns_name.endswith(".")
        else f"{ip.dns_name}.{forward_zone.name}."
    )


async def _emit_forward_and_reverse(
    db: AsyncSession,
    *,
    ip: IPAddress,
    forward_zone: DnsZone,
    rev_by_name: dict[str, DnsZone],
) -> UUID | None:
    """Emit the A/AAAA + matching PTR for one IPAM row. Returns the
    reverse zone id if a PTR was added (caller tracks touched zones for
    the SOA-serial bump), or None on invalid input."""
    # Operators can opt out of projecting DHCP leases into DNS via
    # the dns_ddns_enabled setting; static IPAM rows still project
    # so the projector isn't a binary on/off.
    if ip.source == IpAddressSource.dhcp and not get_settings().dns_ddns_enabled:
        return None
    addr_str = str(ip.address).split("/", 1)[0]
    try:
        a = parse_address(addr_str)
    except ValueError:
        return None
    rtype = DnsRecordType.AAAA if isinstance(a, ipaddress.IPv6Address) else DnsRecordType.A
    # DHCP-sourced IP rows turn into DDNS-marked DNS records; static
    # IPAM allocations stay source=ipam. This lets the UI tell
    # operators which records will vanish on lease expiry.
    record_source = (
        DnsRecordSource.ddns
        if ip.source == IpAddressSource.dhcp
        else DnsRecordSource.ipam
    )
    db.add(DnsRecord(
        zone_id=forward_zone.id,
        name=_forward_label_for(ip.dns_name, forward_zone.name),
        type=rtype,
        data={"target": addr_str},
        source=record_source,
        ipam_address_id=ip.id,
    ))
    rev_origin = reverse_zone_name(addr_str)
    rev_zone = rev_by_name.get(rev_origin)
    if rev_zone is None:
        rev_zone = await _get_or_create_reverse_zone(
            db, name=rev_origin,
            fabric_id=forward_zone.fabric_id, site_id=forward_zone.site_id,
        )
        rev_by_name[rev_origin] = rev_zone
    ptr_label = _ptr_label_in(_ptr_owner(addr_str), rev_origin)
    db.add(DnsRecord(
        zone_id=rev_zone.id, name=ptr_label, type=DnsRecordType.PTR,
        data={"target": _ptr_target_for(ip, forward_zone)},
        source=record_source,
        ipam_address_id=ip.id,
    ))
    return rev_zone.id


async def sync_ipam_records_for_zone(
    db: AsyncSession, zone: DnsZone,
) -> tuple[int, int]:
    """Rebuild `source=ipam` records for a site zone + every reverse
    zone derived from the same IPs. Returns (added, removed) totals
    across all touched zones — replaces, never merges (IPAM is the
    source of truth for these rows).

    Reverse zones are auto-created here on demand at the /24 (v4) or
    /64 (v6) boundary, scoped to the same (fabric, site) as the
    triggering site zone. Apex zones are skipped (operator-curated).
    """
    if zone.kind != DnsZoneKind.site or zone.site_id is None:
        return (0, 0)

    reverse_zones = list((
        await db.execute(
            select(DnsZone).where(
                DnsZone.kind == DnsZoneKind.reverse,
                DnsZone.fabric_id == zone.fabric_id,
                DnsZone.site_id == zone.site_id,
            )
        )
    ).scalars().all())
    removed = await _drop_ipam_records_for_site(db, zone, reverse_zones)

    subnet_rows = (
        await db.execute(select(Subnet).where(Subnet.site_id == zone.site_id))
    ).scalars().all()
    if not subnet_rows:
        return (0, removed)
    subnet_ids = [s.id for s in subnet_rows]
    ip_rows = (
        await db.execute(
            select(IPAddress).where(
                IPAddress.subnet_id.in_(subnet_ids),
                IPAddress.dns_name.is_not(None),
            )
        )
    ).scalars().all()

    rev_by_name: dict[str, DnsZone] = {z.name: z for z in reverse_zones}
    touched_zone_ids: set[UUID] = set()
    added = 0
    for ip in ip_rows:
        rev_zone_id = await _emit_forward_and_reverse(
            db, ip=ip, forward_zone=zone, rev_by_name=rev_by_name,
        )
        if rev_zone_id is None:
            continue
        added += 2  # one A/AAAA + one PTR
        touched_zone_ids.add(rev_zone_id)

    # SOA serial moves with each zone's updated_at — touch every zone
    # we actually changed so its bundle etag flips and resolvers
    # downstream see the new view.
    if added > 0 or removed > 0:
        touched_zone_ids.add(zone.id)
    for zid in touched_zone_ids:
        await db.execute(
            update(DnsZone).where(DnsZone.id == zid).values(updated_at=func.now()),
        )
    await db.flush()
    return (added, removed)


# ---------- BIND zone file parsing ----------

# Record types we can ingest from a BIND file. DNSSEC types (DNSKEY,
# RRSIG, NSEC, NSEC3*, DS, CDNSKEY, CDS, TLSA, SSHFP, etc.) are skipped
# in v1; #1 in the DNS roadmap signs zones internally from key
# material, so importing existing RRSIGs would create state we can't
# regenerate.
_SUPPORTED_IMPORT_TYPES = {
    "A", "AAAA", "CNAME", "MX", "TXT", "SRV", "NS", "CAA", "PTR",
}


class BindImportError(Exception):
    """Raised when the operator's zone file can't be parsed at all."""


def _bind_label_for(name, origin) -> str:
    """Translate a dnspython Name into the bare label DnsRecord stores
    (`@` at apex, otherwise the label relative to origin). Falls back
    to the fully qualified form if relativization fails."""
    if origin and name == origin:
        return "@"
    if origin:
        try:
            return name.relativize(origin).to_text()
        except Exception:
            return name.to_text(omit_final_dot=True)
    return name.to_text(omit_final_dot=True)


def _bind_soa_payload(rdata) -> dict:
    return {
        "mname": rdata.mname.to_text(omit_final_dot=True),
        "rname": rdata.rname.to_text(omit_final_dot=True),
        "refresh": int(rdata.refresh),
        "retry": int(rdata.retry),
        "expire": int(rdata.expire),
        "minimum": int(rdata.minimum),
    }


def _convert_rr(label: str, rtype: str, ttl: int, rdata) -> tuple[dict | None, str | None]:
    """Convert one non-SOA rdata into either a record dict or a warning
    string. Returns `(record, None)` on success, `(None, warning)` when
    the row should be skipped."""
    if rtype not in _SUPPORTED_IMPORT_TYPES:
        return None, f"skipped unsupported type {rtype} for {label}"
    data = _rdata_to_record_data(rtype, rdata)
    if data is None:
        return None, f"skipped malformed {rtype} record for {label}"
    return {
        "name": label, "type": rtype,
        "ttl": int(ttl) if ttl else None,
        "data": data,
    }, None


def parse_bind_zone(
    text: str,
    *,
    default_zone: str | None = None,
) -> dict:
    """Parse a BIND zone file and return a normalized payload:

        {
          "zone_name": "prod.dcim.mil.",
          "soa": {"mname": ..., "rname": ..., "refresh": ..., ...},
          "default_ttl": 300,
          "records": [ {"name": "leaf-01", "type": "A", "ttl": None,
                        "data": {...}}, ... ],
          "warnings": ["skipped DNSKEY at apex (line 12)", ...],
        }

    Records are normalized to the same JSON shapes the API/UI uses, so
    the import endpoint can hand them straight to the DB layer. Lines
    we can't ingest (unsupported types, $INCLUDE, $GENERATE) become
    warnings; structural errors (unbalanced parens, no SOA, malformed
    rdata) raise BindImportError.
    """
    # Imported lazily — keeps `dnspython` out of cold-path imports for
    # non-DNS code paths and out of the API server's startup budget.
    import dns.exception
    import dns.rdatatype
    import dns.zone

    try:
        z = dns.zone.from_text(
            text, origin=default_zone, check_origin=False, relativize=False,
        )
    except dns.exception.DNSException as e:
        raise BindImportError(f"zone parse failed: {e}") from e

    zone_name = z.origin.to_text() if z.origin else (default_zone or "")
    soa_payload: dict | None = None
    default_ttl: int | None = None
    records: list[dict] = []
    warnings: list[str] = []

    for name, ttl, rdata in z.iterate_rdatas():
        rtype = dns.rdatatype.to_text(rdata.rdtype)
        label = _bind_label_for(name, z.origin)
        if rtype == "SOA":
            soa_payload = _bind_soa_payload(rdata)
            if default_ttl is None:
                default_ttl = int(rdata.minimum)
            continue
        record, warning = _convert_rr(label, rtype, ttl, rdata)
        if record is not None:
            records.append(record)
        elif warning is not None:
            warnings.append(warning)

    if soa_payload is None:
        raise BindImportError("zone has no SOA record")
    return {
        "zone_name": zone_name,
        "soa": soa_payload,
        "default_ttl": default_ttl or 60,
        "records": records,
        "warnings": warnings,
    }


def _rdata_to_record_data(rtype: str, rdata) -> dict | None:
    """Translate a parsed dnspython rdata object into the JSON shape
    DnsRecord.data uses. Returns None if the rdata can't be coerced —
    caller emits a warning and skips the row."""
    try:
        if rtype in ("A", "AAAA"):
            return {"target": rdata.address}
        if rtype in ("CNAME", "NS", "PTR"):
            return {"target": rdata.target.to_text(omit_final_dot=True)}
        if rtype == "MX":
            return {
                "priority": int(rdata.preference),
                "target": rdata.exchange.to_text(omit_final_dot=True),
            }
        if rtype == "TXT":
            # dnspython stores TXT as a tuple of bytes-segments; join
            # and decode for the JSON column. Multi-segment TXT (>255
            # chars) collapse to one logical string.
            parts = [s.decode("utf-8", errors="replace") for s in rdata.strings]
            return {"text": "".join(parts)}
        if rtype == "SRV":
            return {
                "priority": int(rdata.priority),
                "weight": int(rdata.weight),
                "port": int(rdata.port),
                "target": rdata.target.to_text(omit_final_dot=True),
            }
        if rtype == "CAA":
            return {
                "flags": int(rdata.flags),
                "tag": rdata.tag.decode() if isinstance(rdata.tag, bytes) else rdata.tag,
                "value": (
                    rdata.value.decode("utf-8", errors="replace")
                    if isinstance(rdata.value, bytes)
                    else rdata.value
                ),
            }
    except Exception:
        return None
    return None
