"""Users, roles, scopes, API tokens. RBAC + ABAC."""

from __future__ import annotations

import enum
from uuid import UUID

from sqlalchemy import JSON, Boolean, DateTime, Enum, ForeignKey, Index, String, UniqueConstraint
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column, relationship

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class ScopeType(str, enum.Enum):
    """ABAC dimensions a role can be scoped on."""

    global_ = "global"
    region = "region"
    site = "site"
    site_group = "site_group"
    enclave = "enclave"
    organization = "organization"


class Permission(UUIDPrimaryKey, Timestamped, Base):
    """A capability a role can grant. e.g. inventory:read, power:control."""

    __tablename__ = "permissions"
    code: Mapped[str] = mapped_column(String(64), unique=True, nullable=False)
    description: Mapped[str | None] = mapped_column(String(255))


class Role(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "roles"
    name: Mapped[str] = mapped_column(String(64), unique=True, nullable=False)
    description: Mapped[str | None] = mapped_column(String(255))
    permission_codes: Mapped[list[str]] = mapped_column(JSON, default=list, nullable=False)
    is_system: Mapped[bool] = mapped_column(Boolean, default=False, nullable=False)


class User(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "users"
    __table_args__ = (Index("ix_users_email", "email", unique=True),)

    email: Mapped[str] = mapped_column(String(255), nullable=False)
    display_name: Mapped[str | None] = mapped_column(String(255))
    is_active: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    sso_subject: Mapped[str | None] = mapped_column(String(255))
    password_hash: Mapped[str | None] = mapped_column(String(255))  # break-glass only
    last_login_at: Mapped[str | None] = mapped_column(DateTime(timezone=True))

    role_assignments: Mapped[list[UserRole]] = relationship(back_populates="user")


class UserRole(UUIDPrimaryKey, Timestamped, Base):
    """Assignment of a Role to a User. Scope rows live in RoleScope."""

    __tablename__ = "user_roles"
    __table_args__ = (UniqueConstraint("user_id", "role_id", name="uq_user_role"),)

    user_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("users.id"), nullable=False)
    role_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("roles.id"), nullable=False)

    user: Mapped[User] = relationship(back_populates="role_assignments")
    scopes: Mapped[list[RoleScope]] = relationship(back_populates="assignment")


class RoleScope(UUIDPrimaryKey, Timestamped, Base):
    """Restricts a UserRole to specific targets. Empty set = whatever the role implies for global."""

    __tablename__ = "role_scopes"
    __table_args__ = (
        Index("ix_role_scopes_assignment", "assignment_id"),
        Index("ix_role_scopes_target", "scope_type", "target_id"),
    )

    assignment_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("user_roles.id"), nullable=False
    )
    scope_type: Mapped[ScopeType] = mapped_column(Enum(ScopeType, name="scope_type"), nullable=False)
    target_id: Mapped[str | None] = mapped_column(String(255))  # uuid or label depending on scope_type

    assignment: Mapped[UserRole] = relationship(back_populates="scopes")


class OidcRoleMapping(UUIDPrimaryKey, Timestamped, Base):
    """Map a role name asserted by the IdP (Keycloak realm role,
    Okta/ADFS group, etc.) to a DCIM Role, optionally with a scope
    binding so the grant is constrained to a region/site/etc.

    On each OIDC sign-in we look at the id_token claims, extract role
    strings via the configured claim paths, and look up rows here to
    decide which DCIM roles the user should hold. claim_source is a
    free-form label ("keycloak", "okta", "adfs", ...) — it lets admins
    document where the value comes from but doesn't affect matching.

    scope_dimension + scope_target describe the optional ABAC scope:
      * both NULL  → global (mapped role applies fleet-wide)
      * region     → scope_target matches Region.code
      * site       → scope_target matches Site.code
      * site_group → scope_target matches SiteGroup.code
      * enclave    → scope_target is the literal string on Site.enclave
      * organization → scope_target is Site.organization
    """

    __tablename__ = "oidc_role_mappings"
    __table_args__ = (UniqueConstraint("idp_role", name="uq_oidc_role_mapping_idp_role"),)

    idp_role: Mapped[str] = mapped_column(String(255), nullable=False)
    claim_source: Mapped[str] = mapped_column(String(64), default="keycloak", nullable=False)
    dcim_role_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("roles.id"), nullable=False
    )
    description: Mapped[str | None] = mapped_column(String(255))
    scope_dimension: Mapped[ScopeType | None] = mapped_column(
        Enum(ScopeType, name="scope_type", create_type=False), nullable=True,
    )
    scope_target: Mapped[str | None] = mapped_column(String(255))


class RevokedJti(Base):
    """Session-JWT deny list. Checked on every authenticated request;
    populated by /auth/logout, admin force-logout, and admin revoke.
    Rows are pruned past their `expires_at` by a periodic cleanup job."""

    __tablename__ = "revoked_jtis"

    jti: Mapped[str] = mapped_column(String(64), primary_key=True)
    user_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id", ondelete="CASCADE"),
    )
    revoked_at: Mapped[str] = mapped_column(DateTime(timezone=True), nullable=False)
    reason: Mapped[str | None] = mapped_column(String(64))
    expires_at: Mapped[str] = mapped_column(DateTime(timezone=True), nullable=False)


class ApiToken(UUIDPrimaryKey, Timestamped, Base):
    """Service-account or integration token. Scope is a copy of (a subset of) the owner's scope."""

    __tablename__ = "api_tokens"
    __table_args__ = (Index("ix_api_tokens_owner", "owner_user_id"),)

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    owner_user_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id"), nullable=False
    )
    token_hash: Mapped[str] = mapped_column(String(255), unique=True, nullable=False)
    permission_codes: Mapped[list[str]] = mapped_column(JSON, default=list, nullable=False)
    scope_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    expires_at: Mapped[str | None] = mapped_column(DateTime(timezone=True))
    last_used_at: Mapped[str | None] = mapped_column(DateTime(timezone=True))
    revoked: Mapped[bool] = mapped_column(Boolean, default=False, nullable=False)
