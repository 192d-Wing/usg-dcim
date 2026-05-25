"""DhcpScopeTemplate — DRY for option-bundles across many scopes (PR 78).

Operators with N similar scopes (one per VLAN, per site, etc.) repeat
the same option-data (dns-servers, ntp-servers, domain-name) and the
same lease timers on every row. The template carries those once;
DhcpScope.template_id points at it; the push/diff/bundle path merges
template + scope before rendering (scope wins on conflict).

The merge contract lives in services/dhcp_push.merge_template_into_scope.
Schema choices that bound this migration:

  * Fabric-scoped (FK to fabrics). Templates aren't global because
    different fabrics often run different DNS/NTP fleets, and the
    ABAC scope already keys on fabric for the parent DhcpServer —
    same enforcement applies here.

  * Family-typed (ip_family 4 or 6). v4 options (router=3) and v6
    options (dns-servers=23) live in different code spaces; a
    template that mixed them would render invalid Kea config. The
    DhcpScope FK reference adds a logical "same family" rule the
    API enforces (DB can't easily express it since the family lives
    on two different rows).

  * `dhcp_scopes.valid_lifetime_seconds` becomes NULLABLE. NULL =
    "inherit from template if any; otherwise fall back to the
    renderer's hardcoded default." Existing rows keep their values;
    new rows can omit timers and lean on the template.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0055"
down_revision: str | None = "20260524_0054"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dhcp_scope_templates (
            id UUID PRIMARY KEY,
            fabric_id UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            name VARCHAR(128) NOT NULL,
            ip_family SMALLINT NOT NULL,
            options_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            valid_lifetime_seconds INTEGER,
            renew_timer_seconds INTEGER,
            rebind_timer_seconds INTEGER,
            preferred_lifetime_seconds INTEGER,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_dhcp_scope_template_family CHECK (ip_family IN (4, 6)),
            CONSTRAINT ck_dhcp_scope_template_v6_only CHECK (
                ip_family = 6 OR preferred_lifetime_seconds IS NULL
            ),
            CONSTRAINT uq_dhcp_scope_template_fabric_name UNIQUE (fabric_id, name)
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scope_templates_fabric "
        "ON dhcp_scope_templates (fabric_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scope_templates_family "
        "ON dhcp_scope_templates (ip_family)"
    )
    # FK on DhcpScope. ON DELETE SET NULL so a template drop doesn't
    # cascade-destroy scopes — they go back to their stored values.
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS template_id UUID "
        "REFERENCES dhcp_scope_templates(id) ON DELETE SET NULL"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_template "
        "ON dhcp_scopes (template_id) WHERE template_id IS NOT NULL"
    )
    # Loosen valid_lifetime_seconds — NULL = inherit. Existing rows
    # keep their 3600 default; new rows can omit and lean on the
    # template. Drop the DEFAULT too so the omission is unambiguous.
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ALTER COLUMN valid_lifetime_seconds DROP NOT NULL"
    )
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ALTER COLUMN valid_lifetime_seconds DROP DEFAULT"
    )


def downgrade() -> None:
    # Reverse loosening first so the NOT NULL re-add doesn't trip on
    # rows we orphaned post-template-drop.
    op.execute(
        "UPDATE dhcp_scopes SET valid_lifetime_seconds = 3600 "
        "WHERE valid_lifetime_seconds IS NULL"
    )
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ALTER COLUMN valid_lifetime_seconds SET DEFAULT 3600"
    )
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ALTER COLUMN valid_lifetime_seconds SET NOT NULL"
    )
    op.execute("DROP INDEX IF EXISTS ix_dhcp_scopes_template")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS template_id")
    op.execute("DROP TABLE IF EXISTS dhcp_scope_templates")
