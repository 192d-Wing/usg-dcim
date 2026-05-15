"""dnstap reader for the collector's DNS observability loop.

CoreDNS's `dnstap` plugin streams every query handled by the auth
pod to a UNIX socket on the shared dns-state volume; this module
listens on that socket and decodes each query just far enough to
pull out the question name + type. The decoded `(name, type)` tuple
is fed back to the collector via a callback, which folds it into a
per-server top-K reservoir the metrics loop ships in its POST.

Why hand-rolled and not `dnstap-py` / `fstrm-py`:
  - dnstap-py last released in 2018, depends on a protobuf release
    line we'd otherwise drag into the collector for one message type.
  - The wire formats we need (fstrm control + data framing, dnstap
    Dnstap message, and the DNS question section) total under 200
    lines of straightforward decoding. Cheaper to maintain than the
    dependency surface of the third-party packages.

Bidirectional fstrm handshake (CoreDNS is the client, we listen):
  reader: <accept>                  writer: <connect>
  reader: <read READY>             writer: <send READY>
  reader: <send ACCEPT(ctype)>     writer: <read ACCEPT>
  reader: <read START>             writer: <send START(ctype)>
       --- data frames flow ---
  reader: <read STOP>              writer: <send STOP>
  reader: <send FINISH>            writer: <read FINISH>
"""

from __future__ import annotations

import asyncio
import os
import struct
from collections.abc import Awaitable, Callable
from pathlib import Path

import structlog

log = structlog.get_logger("collector.dnstap")

# Frame Streams control-frame types per draft-ietf-dnsop-dnstap-09 §3.
_CTL_ACCEPT = 0x01
_CTL_START = 0x02
_CTL_STOP = 0x03
_CTL_READY = 0x04
_CTL_FINISH = 0x05

_CTL_FIELD_CONTENT_TYPE = 0x01
_DNSTAP_CONTENT_TYPE = b"protobuf:dnstap.Dnstap"

# RFC 1035 question-section TYPE values plus the few DNSSEC + service
# types operators are likely to want to surface on the dashboard.
# Numeric fall-through ("17", "65535") covers anything not enumerated
# here so the top-K never drops a sample just because we don't have a
# pretty label for the type.
_RR_TYPES = {
    1: "A", 2: "NS", 5: "CNAME", 6: "SOA", 12: "PTR", 15: "MX",
    16: "TXT", 24: "SIG", 28: "AAAA", 33: "SRV", 35: "NAPTR", 41: "OPT",
    43: "DS", 46: "RRSIG", 47: "NSEC", 48: "DNSKEY", 50: "NSEC3",
    51: "NSEC3PARAM", 52: "TLSA", 65: "HTTPS", 257: "CAA",
}

OnQuery = Callable[[str, str], Awaitable[None] | None]


# --- low-level helpers --------------------------------------------------------


async def _read_exact(reader: asyncio.StreamReader, n: int) -> bytes:
    """Read exactly n bytes or raise EOFError on premature close."""
    buf = b""
    while len(buf) < n:
        chunk = await reader.read(n - len(buf))
        if not chunk:
            raise EOFError
        buf += chunk
    return buf


def _make_control_frame(payload: bytes) -> bytes:
    """Wrap a control payload in fstrm's `0x00000000 escape + length`
    prefix. Used when we send ACCEPT / FINISH back to CoreDNS."""
    return b"\x00\x00\x00\x00" + struct.pack(">I", len(payload)) + payload


def _make_accept_frame() -> bytes:
    """ACCEPT control frame carrying the `protobuf:dnstap.Dnstap`
    content-type field — tells CoreDNS we accept dnstap payloads."""
    field = (
        struct.pack(">I", _CTL_FIELD_CONTENT_TYPE)
        + struct.pack(">I", len(_DNSTAP_CONTENT_TYPE))
        + _DNSTAP_CONTENT_TYPE
    )
    return _make_control_frame(struct.pack(">I", _CTL_ACCEPT) + field)


def _make_finish_frame() -> bytes:
    return _make_control_frame(struct.pack(">I", _CTL_FINISH))


async def _read_frame(reader: asyncio.StreamReader) -> tuple[str, bytes]:
    """Decode the next fstrm frame. Returns `(kind, payload)` where
    kind is `'control'` or `'data'`."""
    length = struct.unpack(">I", await _read_exact(reader, 4))[0]
    if length == 0:
        ctrl_length = struct.unpack(">I", await _read_exact(reader, 4))[0]
        return ("control", await _read_exact(reader, ctrl_length))
    return ("data", await _read_exact(reader, length))


# --- protobuf decoder ---------------------------------------------------------


def _read_varint(data: bytes, pos: int) -> tuple[int, int]:
    """Decode a protobuf varint at `pos`. Returns `(value, new_pos)`."""
    result = 0
    shift = 0
    while pos < len(data):
        byte = data[pos]
        result |= (byte & 0x7F) << shift
        pos += 1
        if not (byte & 0x80):
            return result, pos
        shift += 7
        if shift >= 64:
            raise ValueError("varint too long")
    raise ValueError("varint truncated")


def _skip_field(data: bytes, pos: int, wire_type: int) -> int:
    """Skip a protobuf field we don't care about. Returns new pos."""
    if wire_type == 0:
        _, pos = _read_varint(data, pos)
    elif wire_type == 1:
        pos += 8
    elif wire_type == 2:
        length, pos = _read_varint(data, pos)
        pos += length
    elif wire_type == 5:
        pos += 4
    else:
        raise ValueError(f"unknown wire type {wire_type}")
    return pos


def _find_length_delimited(
    data: bytes, target_field: int,
) -> bytes | None:
    """Walk a protobuf message looking for a length-delimited field
    matching `target_field`. Returns the inner bytes or None.

    Used twice on each frame: once on the outer `Dnstap` message
    (target field 14 = `message`), once on the inner `Message`
    struct (target field 10 = `query_message`)."""
    pos = 0
    while pos < len(data):
        tag, pos = _read_varint(data, pos)
        field_num = tag >> 3
        wire_type = tag & 0x07
        if field_num == target_field and wire_type == 2:
            length, pos = _read_varint(data, pos)
            return data[pos:pos + length]
        pos = _skip_field(data, pos, wire_type)
    return None


def _extract_query_wire(frame: bytes) -> bytes | None:
    """Pull the raw DNS-wire-format query bytes out of a dnstap
    frame. Returns None when the frame is a response-only message or
    is malformed enough that we shouldn't trust the rest."""
    try:
        inner = _find_length_delimited(frame, target_field=14)
        if inner is None:
            return None
        return _find_length_delimited(inner, target_field=10)
    except (ValueError, IndexError):
        return None


# --- DNS wire format ---------------------------------------------------------


def _decode_dns_question(wire: bytes) -> tuple[str | None, str | None]:
    """Parse just the question section of a DNS message. Returns
    `(qname, qtype_label)` or `(None, None)` on malformed input.

    We deliberately don't use dnspython for this — the question
    section is fixed-shape (12-byte header + length-prefixed labels
    + 4 bytes type/class) and pulling in a 100KB dependency for
    that one decode would be silly."""
    if len(wire) < 12:
        return None, None
    pos = 12  # skip the DNS header
    labels: list[str] = []
    while pos < len(wire):
        length = wire[pos]
        pos += 1
        if length == 0:
            break
        if length & 0xC0:
            # Compression pointers don't appear in the question
            # section per RFC 1035 §4.1.4 — treat as malformed.
            return None, None
        if pos + length > len(wire):
            return None, None
        labels.append(wire[pos:pos + length].decode("ascii", "replace"))
        pos += length
    if pos + 4 > len(wire):
        return None, None
    qtype = struct.unpack(">H", wire[pos:pos + 2])[0]
    qname = (".".join(labels) + ".").lower() if labels else "."
    return qname, _RR_TYPES.get(qtype, str(qtype))


# --- per-connection handler --------------------------------------------------


async def _read_until_ready(reader: asyncio.StreamReader) -> bool:
    """Pump frames until we see a READY control frame. Returns True
    on success, False on EOF or unexpected payload."""
    kind, payload = await _read_frame(reader)
    if kind != "control" or len(payload) < 4:
        return False
    return struct.unpack(">I", payload[:4])[0] == _CTL_READY


async def _await_start(reader: asyncio.StreamReader) -> bool:
    """Read frames until START arrives. CoreDNS sends START with the
    same content_type field we already verified on its READY frame,
    so we don't re-check it here — we just need to advance past it
    before data frames begin."""
    kind, payload = await _read_frame(reader)
    if kind != "control" or len(payload) < 4:
        return False
    return struct.unpack(">I", payload[:4])[0] == _CTL_START


async def _dispatch_query(frame: bytes, on_query: OnQuery) -> None:
    """Decode one data frame and invoke the consumer callback. Any
    decode failure is silently dropped — a single torn frame
    shouldn't bring the dnstap loop down."""
    query_wire = _extract_query_wire(frame)
    if query_wire is None:
        return
    qname, qtype = _decode_dns_question(query_wire)
    if qname is None or qtype is None:
        return
    result = on_query(qname, qtype)
    if asyncio.iscoroutine(result):
        await result


async def _data_loop(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    on_query: OnQuery,
) -> None:
    """Read data frames until STOP arrives, then send FINISH back."""
    while True:
        kind, payload = await _read_frame(reader)
        if kind == "control":
            ctl_type = struct.unpack(">I", payload[:4])[0]
            if ctl_type == _CTL_STOP:
                writer.write(_make_finish_frame())
                await writer.drain()
                return
            # Unknown control frames mid-stream — ignore and keep
            # reading. The fstrm spec allows future extensions.
            continue
        await _dispatch_query(payload, on_query)


async def _handle_client(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    on_query: OnQuery,
) -> None:
    """One CoreDNS dnstap connection. Walks the bidirectional fstrm
    handshake then loops reading data frames until STOP/EOF."""
    peer = "unix"
    try:
        if not await _read_until_ready(reader):
            return
        writer.write(_make_accept_frame())
        await writer.drain()
        if not await _await_start(reader):
            return
        await _data_loop(reader, writer, on_query)
    except (EOFError, ConnectionResetError, asyncio.IncompleteReadError):
        pass
    except Exception as e:  # noqa: BLE001
        log.warning("dnstap_client_error", peer=peer, err=str(e))
    finally:
        writer.close()
        try:
            await writer.wait_closed()
        except Exception:  # noqa: BLE001
            pass


# --- public entrypoint -------------------------------------------------------


async def serve_dnstap(socket_path: str, on_query: OnQuery) -> None:
    """Run a UNIX-socket fstrm server forever. CoreDNS connects to
    us as a client; we invoke `on_query(name, type)` for every
    decoded DNS query.

    Removes a stale socket file from a previous run before binding
    — important when the collector restarts because Linux leaves the
    inode behind and `bind()` would EADDRINUSE otherwise."""
    Path(socket_path).parent.mkdir(parents=True, exist_ok=True)
    try:
        Path(socket_path).unlink()
    except FileNotFoundError:
        pass
    server = await asyncio.start_unix_server(
        lambda r, w: _handle_client(r, w, on_query),
        path=socket_path,
    )
    # Lock the socket down to owner-only. Both the collector and the
    # resolver (CoreDNS / Hickory) must run as the same UID — the
    # site-dns compose + k8s manifests do this by running both as root
    # in the shared dns-state volume. Operators running the resolver
    # as a different UID need to align it with the collector's UID
    # (or chown the socket post-bind via their orchestration).
    try:
        os.chmod(socket_path, 0o600)
    except OSError:
        pass
    log.info("dnstap_server_start", socket=socket_path)
    async with server:
        await server.serve_forever()
