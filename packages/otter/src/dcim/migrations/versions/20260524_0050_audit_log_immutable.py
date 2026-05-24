"""Make audit_log append-only via row-level triggers.

PR 64 — Phase 4 hardening. The audit_log table is the system's
forensic record of every mutation. Today the schema allows arbitrary
UPDATE / DELETE, which means a compromised app-layer credential can
silently rewrite history. This migration installs BEFORE UPDATE and
BEFORE DELETE triggers that raise an `insufficient_privilege`
exception (SQLSTATE 42501) — converting the audit_log into an
append-only record at the database level, regardless of the calling
role's GRANTs.

The original roadmap entry called for revoking UPDATE / DELETE from
"the app role." This repo doesn't yet differentiate an app vs admin
role at the connection level (the app connects as the database
owner). A trigger-based approach is universal: it fires regardless
of the connecting role, so the same policy applies whether the app
is using an unprivileged role today or migrates to one later. The
only ways to bypass are (a) connecting as a superuser and either
disabling the trigger or dropping it, or (b) ALTER TABLE DISABLE
TRIGGER — both of which are themselves DDL events that pg_audit /
pgaudit would capture if enabled at the cluster level.

For WORM compliance the operational pattern is to ship audit_log
rows to an external store on a schedule (S3 with object-lock, an
external SIEM, etc.). That export pipeline lives outside the
database; this migration is purely the on-database half of the
defense.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0050"
down_revision: str | None = "20260523_0049"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Single guard function reused by both triggers. The ERRCODE
    # mirrors a permission denial so app-layer error handling
    # surfaces these the same way it would surface a missing GRANT
    # in a role-separated deployment.
    op.execute(
        """
        CREATE OR REPLACE FUNCTION audit_log_no_update_delete()
        RETURNS trigger AS $$
        BEGIN
            RAISE EXCEPTION
                'audit_log is append-only — % rejected on row %', TG_OP, OLD
                USING ERRCODE = 'insufficient_privilege';
        END;
        $$ LANGUAGE plpgsql;
        """
    )
    op.execute(
        """
        CREATE TRIGGER audit_log_immutable_update
            BEFORE UPDATE ON audit_log
            FOR EACH ROW EXECUTE FUNCTION audit_log_no_update_delete();
        """
    )
    op.execute(
        """
        CREATE TRIGGER audit_log_immutable_delete
            BEFORE DELETE ON audit_log
            FOR EACH ROW EXECUTE FUNCTION audit_log_no_update_delete();
        """
    )


def downgrade() -> None:
    # Drop triggers before the function so the dependency unwinds
    # cleanly. The function might persist on a partial-downgrade
    # failure — IF EXISTS keeps that idempotent.
    op.execute("DROP TRIGGER IF EXISTS audit_log_immutable_delete ON audit_log")
    op.execute("DROP TRIGGER IF EXISTS audit_log_immutable_update ON audit_log")
    op.execute("DROP FUNCTION IF EXISTS audit_log_no_update_delete()")
