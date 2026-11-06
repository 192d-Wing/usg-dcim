# Protocol v1 fixture corpus

Shared, language-neutral conformance corpus for console protocol v1. Every
implementation of the protocol (Rust `console-protocol`, Rust `console-sdk`,
TypeScript `sdk-js`, and anything vendored from it) must pass every entry in
`MANIFEST.json`. A fixture added without all implementations passing fails CI.

## Rules

- **Frozen.** Never edit or delete an existing fixture; deployed peers send
  exactly these bytes. Add a new file (and a manifest entry) instead.
- `sdk_v1/` holds only canonical, valid messages that round-trip exactly.
- `sdk_v1_cases/` holds edge cases: missing optionals, extra fields, unknown
  types, malformed and oversize messages, and the `*.canonical.json` files
  that missing-optional cases normalize to.
- `tools_v1/` holds HTTP JSON bodies (gateway API, assertion claims).

## Reading a fixture

The message is the file's contents (UTF-8) with trailing whitespace
(space, tab, CR, LF) removed. Nothing else is trimmed or rewritten. Sizes are
measured in **UTF-8 bytes** of that string (oversize fixtures are ASCII, so
bytes equal JS string length).

## MANIFEST.json (`format` = `usg-ops-console.protocol-fixtures`, `format_version` 1)

Top level:

| key | meaning |
|-----|---------|
| `format`, `format_version` | Identify this schema. A consumer that sees an unknown `format_version` must fail. |
| `protocol` | Protocol version the corpus pins (`1`). |
| `limits.max_message_bytes` | `MAX_MESSAGE_BYTES` (16384). |
| `limits.max_notify_chars` | `MAX_NOTIFY_CHARS` (500); display truncation, not a decode rule. |
| `fixtures` | Array of entries, below. |

Each entry:

| key | type | meaning |
|-----|------|---------|
| `file` | string | Path relative to this directory. |
| `kind` | `"sdk-message"` \| `"http-json"` | postMessage bridge message, or an HTTP/JWT JSON body. |
| `direction` | string | `sdk-message`: `"shell-to-tool"` or `"tool-to-shell"` (selects the decoder). `http-json`: informational (`gateway-to-shell`, `shell-to-gateway`, `gateway-to-tool`, `gateway-to-upstream`, `gateway-to-client`). |
| `type` | string \| null | `sdk-message`: the variant tag the decode must yield (null when not decoded). `http-json`: the schema name (Rust type name, e.g. `ToolsResponse`). |
| `case` | string | `canonical`, `missing-optional`, `extra-field`, `unknown-type`, `malformed`, `oversize`, `at-limit`, `notify-over-cap`. Informational grouping. |
| `valid` | bool | `true` when a conforming receiver accepts the input without error (including `ignore`). |
| `expect` | string | Required outcome, below. |
| `canonical` | string, optional | For `decode`: file whose JSON the decoded value must re-encode to. Absent means the fixture itself. |
| `description` | string | Human note. |

`expect` values, applied with the decoder for `direction`:

| expect | requirement |
|--------|-------------|
| `decode` | Decodes to a message of variant `type`; re-encoding it yields JSON **semantically equal** (object key order ignored, `null` kept) to `canonical` (or the fixture). |
| `ignore` | Decoder returns "nothing" (Rust `Ok(None)`, TS `null`/no callback): unknown, missing or non-string `type`. Not an error. |
| `malformed` | Decoder reports an error: not JSON, not a JSON object, or a known `type` with a bad body. |
| `too_large` | Rejected for size **before** parsing or type dispatch (input bytes > `max_message_bytes`). |

## Integer fields

Integer fields (`protocol`, `count`, and every other `u32`/`u64` in the Rust
types) must be encoded as JSON **integer literals** (`3`, never `3.0` or
`3e0`). serde rejects `3.0` for an integer field, but JavaScript's
`JSON.parse` cannot tell `3` from `3.0`, so a JS decoder (sdk-js) accepts it.
That asymmetry is deliberate tolerance on the receive side only: encoders in
every implementation must emit integer literals. sdk-js guarantees this by
re-validating integer fields with `Number.isInteger` (and range) before
`JSON.stringify`, which always prints integral numbers without a fraction.

Order of checks every decoder must follow: size cap, then JSON parse, then
object check, then `type` dispatch (unknown -> ignore), then body validation.

Consumers should also assert that every file in `sdk_v1/`, `sdk_v1_cases/`
and `tools_v1/` (other than this README) is either an entry `file` or some
entry's `canonical`, so nothing is silently skipped.
