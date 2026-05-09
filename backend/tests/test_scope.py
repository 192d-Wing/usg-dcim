"""Unit tests for the ABAC Scope value object.

The DB-touching helpers (`scope_for_user`, `site_matches_scope`,
`filter_sites_in_scope`) are covered by the integration suite later. These
tests pin down the union semantics that compose multiple role assignments.
"""

from uuid import uuid4

from dcim.security.scope import Scope


def test_empty_scope_is_not_global():
    s = Scope()
    assert s.is_global is False
    assert s.region_ids == frozenset()
    assert s.site_ids == frozenset()
    assert s.enclaves == frozenset()


def test_union_propagates_global():
    s = Scope().union(Scope(is_global=True))
    assert s.is_global is True


def test_union_combines_dimensions():
    a, b, c = uuid4(), uuid4(), uuid4()
    left = Scope(region_ids=frozenset({a}), site_ids=frozenset({b}))
    right = Scope(region_ids=frozenset({c}), enclaves=frozenset({"il5"}))
    out = left.union(right)
    assert out.region_ids == {a, c}
    assert out.site_ids == {b}
    assert out.enclaves == {"il5"}
    assert out.is_global is False


def test_union_is_idempotent():
    a = uuid4()
    s = Scope(site_ids=frozenset({a}))
    assert s.union(s).site_ids == {a}


def test_global_scope_swallows_other_dimensions():
    """Operationally a global scope dominates — but the value object still
    preserves the per-dimension sets so a downstream caller can detect *why*
    the scope is global (e.g. for UI explanations)."""
    a = uuid4()
    out = Scope(is_global=True).union(Scope(site_ids=frozenset({a})))
    assert out.is_global is True
    assert out.site_ids == {a}
