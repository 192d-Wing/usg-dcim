"""Unit tests for find_matching_capability — the positional-glob
matcher that decides whether a held capability grants a requested
capability code. Pure-Python (no DB), so we exercise every wildcard
shape directly."""

from uuid import uuid4

from dcim.security.deps import find_matching_capability
from dcim.security.scope import Scope


def _g() -> Scope:
    """Distinct global scope per call so identity checks distinguish hits."""
    return Scope(is_global=True)


# ---------- exact match ----------

def test_exact_match_returns_scope():
    s = _g()
    assert find_matching_capability({"dns:zones:read": s}, "dns:zones:read") is s


def test_no_match_returns_none():
    assert find_matching_capability({"dns:zones:read": _g()}, "dns:zones:create") is None


def test_empty_caps_returns_none():
    assert find_matching_capability({}, "dns:zones:read") is None


# ---------- bare global wildcard ----------

def test_bare_star_grants_anything():
    s = _g()
    assert find_matching_capability({"*": s}, "anything:goes:here") is s
    assert find_matching_capability({"*": s}, "x") is s


def test_exact_match_beats_bare_star():
    """If both exact and `*` are held, prefer the exact match's scope.
    Same scope is returned in practice but the order matters when
    scopes differ between an exact and a wildcard cap."""
    site_scope = Scope(site_ids=frozenset({uuid4()}))
    star_scope = _g()
    out = find_matching_capability(
        {"dns:zones:read": site_scope, "*": star_scope},
        "dns:zones:read",
    )
    assert out is site_scope


# ---------- trailing-segment wildcard ----------

def test_trailing_action_wildcard():
    s = _g()
    caps = {"dns:zones:*": s}
    assert find_matching_capability(caps, "dns:zones:read") is s
    assert find_matching_capability(caps, "dns:zones:create") is s
    assert find_matching_capability(caps, "dns:zones:delete") is s


def test_trailing_wildcard_segment_count_must_match():
    """`dns:zones:*` matches three-segment codes only — not `dns:zones`."""
    caps = {"dns:zones:*": _g()}
    assert find_matching_capability(caps, "dns:zones") is None


def test_trailing_wildcard_does_not_cross_domain():
    caps = {"dns:zones:*": _g()}
    assert find_matching_capability(caps, "dns:records:read") is None


def test_trailing_domain_wildcard_two_segments():
    """`dns:*` is a two-segment pattern, so it grants `dns:<anything>` —
    but NOT three-segment codes like `dns:zones:read`."""
    s = _g()
    caps = {"dns:*": s}
    assert find_matching_capability(caps, "dns:something") is s
    assert find_matching_capability(caps, "dns:zones:read") is None


# ---------- middle-segment wildcard ----------

def test_middle_segment_wildcard():
    """`inventory:*:read` should match any resource under inventory
    when the action is `read`."""
    s = _g()
    caps = {"inventory:*:read": s}
    assert find_matching_capability(caps, "inventory:sites:read") is s
    assert find_matching_capability(caps, "inventory:racks:read") is s


def test_middle_wildcard_does_not_match_other_action():
    caps = {"inventory:*:read": _g()}
    assert find_matching_capability(caps, "inventory:sites:create") is None


# ---------- multiple wildcards ----------

def test_double_wildcard_in_one_pattern():
    """`dns:*:*` covers every action on every resource under dns."""
    s = _g()
    caps = {"dns:*:*": s}
    assert find_matching_capability(caps, "dns:zones:read") is s
    assert find_matching_capability(caps, "dns:records:delete") is s
    # but not a two-segment code
    assert find_matching_capability(caps, "dns:something") is None


# ---------- segment-count discipline ----------

def test_pattern_shorter_than_code_doesnt_match():
    caps = {"dns:zones": _g()}
    assert find_matching_capability(caps, "dns:zones:read") is None


def test_pattern_longer_than_code_doesnt_match():
    caps = {"dns:zones:read:extra": _g()}
    assert find_matching_capability(caps, "dns:zones:read") is None


# ---------- scope identity ----------

def test_matched_scope_is_returned_verbatim():
    """The matcher returns the held cap's Scope unchanged — call sites
    rely on this for `is_global` / `site_ids` membership checks."""
    sid = uuid4()
    scoped = Scope(site_ids=frozenset({sid}))
    out = find_matching_capability({"dns:zones:*": scoped}, "dns:zones:read")
    assert out is scoped
    assert out.site_ids == {sid}
    assert out.is_global is False
