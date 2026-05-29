"""Pin the alembic include_object exclusion for LIR-owned tables.

The phase-1 cleanup commit (fdd18d0) deleted models/lir.py because
the LIR module is Go-native and Python has no ORM consumer for
lir_pools/lir_requests/lir_allocations. Without an
include_object filter, autogenerate would compare the live DB to
Base.metadata, find those three tables 'in DB but not in metadata,'
and emit op.drop_table(...) — silent data loss on the next
`make migrate`.

These tests guard the exclusion against accidental removal: the
LIR_TABLES set must list the three tables, and include_object must
return False for both table-level and child-object lookups against
each.
"""

from __future__ import annotations

from types import SimpleNamespace

from dcim.migrations.include_filter import LIR_TABLES, include_object


def test_lir_tables_set_covers_all_three():
    assert frozenset({"lir_pools", "lir_requests", "lir_allocations"}) == LIR_TABLES


def test_table_in_lir_tables_is_excluded():
    for name in ("lir_pools", "lir_requests", "lir_allocations"):
        assert include_object(
            obj=SimpleNamespace(name=name),
            name=name,
            type_="table",
            _reflected=True,
            _compare_to=None,
        ) is False, f"{name} should be excluded"


def test_non_lir_table_is_included():
    # Regression guard: the filter must not over-exclude. Tables
    # outside LIR_TABLES (supernets, organizations, etc.) keep the
    # normal autogenerate behavior.
    for name in ("supernets", "organizations", "fabrics", "sites"):
        assert include_object(
            obj=SimpleNamespace(name=name),
            name=name,
            type_="table",
            _reflected=True,
            _compare_to=None,
        ) is True, f"{name} should NOT be excluded"


def test_column_on_lir_table_is_excluded():
    # autogenerate also visits child objects (columns, indexes,
    # constraints). When the parent table is a LIR table, the
    # filter must skip them too — otherwise autogenerate would
    # emit ADD COLUMN / DROP COLUMN ops against tables we
    # otherwise excluded.
    parent = SimpleNamespace(name="lir_pools")
    col = SimpleNamespace(name="ip_family", table=parent)
    assert include_object(
        obj=col, name="ip_family", type_="column",
        _reflected=True, _compare_to=None,
    ) is False


def test_index_on_non_lir_table_is_included():
    parent = SimpleNamespace(name="supernets")
    idx = SimpleNamespace(name="ix_supernets_fabric_vrf", table=parent)
    assert include_object(
        obj=idx, name="ix_supernets_fabric_vrf", type_="index",
        _reflected=True, _compare_to=None,
    ) is True


def test_object_with_no_table_attr_is_included():
    # Top-level Table objects don't carry a .table attribute. Those
    # must pass through the table-name check; non-LIR table names
    # return True.
    obj = SimpleNamespace(name="audit_log")
    assert include_object(
        obj=obj, name="audit_log", type_="table",
        _reflected=True, _compare_to=None,
    ) is True
