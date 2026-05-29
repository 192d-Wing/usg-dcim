"""audit_log: index (site_id, action) for the ABAC-scoped action dropdown.

Background: PR 177 added scope_site_ids ABAC filtering to
ListAuditLog / CountAuditLog / ListAuditActions so a regional admin
no longer sees fleet-wide audit events. ListAuditActions is the LIST
DISTINCT that drives the action-filter dropdown in finch; under the
new filter it becomes:

    SELECT DISTINCT action
    FROM audit_log
    WHERE site_id = ANY($scope_site_ids)
    ORDER BY action

Existing indexes (action, occurred_at), (site_id, occurred_at),
(actor_user_id, occurred_at), (target_type, target_id) don't cover
this predicate well. For a scoped caller on a large audit_log
(>10M rows) the plan becomes index-scan on (site_id, occurred_at)
+ HashAggregate on action — substantially slower than the previous
global DISTINCT plan, which could loose-index-scan (action,
occurred_at).

This migration adds:

    CREATE INDEX ix_audit_log_site_action ON audit_log (site_id, action);

That lets a scoped DISTINCT use an index-only scan on (site_id, action)
with no aggregate step, and keeps the dropdown query under control
as the audit_log grows.

NULL-site rows: this index does NOT cover them (B-tree indexes
include NULLs as a separate group). That's fine — by design scoped
callers never see NULL-site rows (the WHERE drops them), and the
existing (action, occurred_at) index still serves the global dropdown
path.

Downgrade drops the index (no schema change).
"""
from __future__ import annotations

from alembic import op

revision = "20260529_0066"
down_revision = "20260528_0065"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.create_index(
        "ix_audit_log_site_action",
        "audit_log",
        ["site_id", "action"],
    )


def downgrade() -> None:
    op.drop_index("ix_audit_log_site_action", table_name="audit_log")
