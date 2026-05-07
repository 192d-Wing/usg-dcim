"""Schema bootstrap — all tables, indexes, and enum types.

Revision ID: 20260507_0002
Revises: 20260507_0001
Create Date: 2026-05-07
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260507_0002"
down_revision: str | None = "20260507_0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("DO $$ BEGIN CREATE TYPE alert_severity AS ENUM ('info', 'warning', 'minor', 'major', 'critical'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("DO $$ BEGIN CREATE TYPE alert_state AS ENUM ('firing', 'acknowledged', 'suppressed', 'resolved'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("DO $$ BEGIN CREATE TYPE asset_kind AS ENUM ('server', 'switch', 'router', 'pdu', 'ups', 'crac', 'sensor', 'storage', 'chassis', 'blade', 'other'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("DO $$ BEGIN CREATE TYPE collector_status AS ENUM ('pending', 'healthy', 'degraded', 'stale', 'unreachable', 'decommissioned'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("DO $$ BEGIN CREATE TYPE freshness_state AS ENUM ('current', 'stale', 'estimated', 'manual', 'unknown'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("DO $$ BEGIN CREATE TYPE lifecycle_state AS ENUM ('planned', 'staged', 'active', 'maintenance', 'decommissioned', 'retired'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("DO $$ BEGIN CREATE TYPE scope_type AS ENUM ('global', 'region', 'site', 'site_group', 'enclave', 'organization'); EXCEPTION WHEN duplicate_object THEN NULL; END $$;")
    op.execute("""
CREATE TABLE permissions (
	code VARCHAR(64) NOT NULL, 
	description VARCHAR(255), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	UNIQUE (code)
)
""")
    op.execute("""
CREATE TABLE regions (
	name VARCHAR(128) NOT NULL, 
	code VARCHAR(32) NOT NULL, 
	description VARCHAR(512), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	UNIQUE (name), 
	UNIQUE (code)
)
""")
    op.execute("""
CREATE TABLE roles (
	name VARCHAR(64) NOT NULL, 
	description VARCHAR(255), 
	permission_codes JSON NOT NULL, 
	is_system BOOLEAN NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	UNIQUE (name)
)
""")
    op.execute("""
CREATE TABLE site_groups (
	name VARCHAR(128) NOT NULL, 
	kind VARCHAR(32) NOT NULL, 
	description VARCHAR(512), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	UNIQUE (name)
)
""")
    op.execute("""
CREATE TABLE users (
	email VARCHAR(255) NOT NULL, 
	display_name VARCHAR(255), 
	is_active BOOLEAN NOT NULL, 
	sso_subject VARCHAR(255), 
	password_hash VARCHAR(255), 
	last_login_at TIMESTAMP WITH TIME ZONE, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id)
)
""")
    op.execute("""CREATE UNIQUE INDEX ix_users_email ON users (email)""")
    op.execute("""
CREATE TABLE api_tokens (
	name VARCHAR(128) NOT NULL, 
	owner_user_id UUID NOT NULL, 
	token_hash VARCHAR(255) NOT NULL, 
	permission_codes JSON NOT NULL, 
	scope_json JSON NOT NULL, 
	expires_at TIMESTAMP WITH TIME ZONE, 
	last_used_at TIMESTAMP WITH TIME ZONE, 
	revoked BOOLEAN NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(owner_user_id) REFERENCES users (id), 
	UNIQUE (token_hash)
)
""")
    op.execute("""CREATE INDEX ix_api_tokens_owner ON api_tokens (owner_user_id)""")
    op.execute("""
CREATE TABLE sites (
	region_id UUID NOT NULL, 
	name VARCHAR(128) NOT NULL, 
	code VARCHAR(32) NOT NULL, 
	address VARCHAR(512), 
	latitude NUMERIC(9, 6), 
	longitude NUMERIC(9, 6), 
	timezone VARCHAR(64), 
	majcom VARCHAR(64), 
	organization VARCHAR(128), 
	mission_owner VARCHAR(128), 
	enclave VARCHAR(64), 
	classification VARCHAR(32), 
	lifecycle_state lifecycle_state NOT NULL, 
	metadata_json JSON NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_site_region_code UNIQUE (region_id, code), 
	FOREIGN KEY(region_id) REFERENCES regions (id)
)
""")
    op.execute("""CREATE INDEX ix_sites_majcom ON sites (majcom)""")
    op.execute("""CREATE INDEX ix_sites_enclave ON sites (enclave)""")
    op.execute("""CREATE INDEX ix_sites_region ON sites (region_id)""")
    op.execute("""CREATE INDEX ix_sites_organization ON sites (organization)""")
    op.execute("""CREATE INDEX ix_sites_lifecycle ON sites (lifecycle_state)""")
    op.execute("""CREATE INDEX ix_sites_mission_owner ON sites (mission_owner)""")
    op.execute("""
CREATE TABLE user_roles (
	user_id UUID NOT NULL, 
	role_id UUID NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_user_role UNIQUE (user_id, role_id), 
	FOREIGN KEY(user_id) REFERENCES users (id), 
	FOREIGN KEY(role_id) REFERENCES roles (id)
)
""")
    op.execute("""
CREATE TABLE alert_rules (
	name VARCHAR(128) NOT NULL, 
	description VARCHAR(512), 
	metric VARCHAR(128) NOT NULL, 
	operator VARCHAR(8) NOT NULL, 
	threshold FLOAT NOT NULL, 
	duration_seconds INTEGER NOT NULL, 
	severity alert_severity NOT NULL, 
	site_scope_id UUID, 
	asset_filter_json JSON NOT NULL, 
	enabled BOOLEAN NOT NULL, 
	runbook_url VARCHAR(512), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(site_scope_id) REFERENCES sites (id)
)
""")
    op.execute("""CREATE INDEX ix_alert_rules_metric ON alert_rules (metric)""")
    op.execute("""CREATE INDEX ix_alert_rules_site ON alert_rules (site_scope_id)""")
    op.execute("""
CREATE TABLE audit_log (
	occurred_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	actor_user_id UUID, 
	actor_token_id UUID, 
	actor_label VARCHAR(255), 
	actor_ip VARCHAR(64), 
	action VARCHAR(64) NOT NULL, 
	target_type VARCHAR(64), 
	target_id VARCHAR(64), 
	site_id UUID, 
	request_id VARCHAR(64), 
	success BOOLEAN NOT NULL, 
	diff_json JSON NOT NULL, 
	metadata_json JSON NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(actor_user_id) REFERENCES users (id), 
	FOREIGN KEY(actor_token_id) REFERENCES api_tokens (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id)
)
""")
    op.execute("""CREATE INDEX ix_audit_action_ts ON audit_log (action, occurred_at)""")
    op.execute("""CREATE INDEX ix_audit_target ON audit_log (target_type, target_id)""")
    op.execute("""CREATE INDEX ix_audit_user_ts ON audit_log (actor_user_id, occurred_at)""")
    op.execute("""CREATE INDEX ix_audit_site_ts ON audit_log (site_id, occurred_at)""")
    op.execute("""
CREATE TABLE buildings (
	site_id UUID NOT NULL, 
	name VARCHAR(128) NOT NULL, 
	code VARCHAR(32) NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_building_site_code UNIQUE (site_id, code), 
	FOREIGN KEY(site_id) REFERENCES sites (id)
)
""")
    op.execute("""CREATE INDEX ix_buildings_site ON buildings (site_id)""")
    op.execute("""
CREATE TABLE circuits (
	site_id UUID NOT NULL, 
	label VARCHAR(128) NOT NULL, 
	provider VARCHAR(128), 
	bandwidth_mbps INTEGER, 
	purpose VARCHAR(128), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id)
)
""")
    op.execute("""CREATE INDEX ix_circuits_site ON circuits (site_id)""")
    op.execute("""
CREATE TABLE collectors (
	site_id UUID NOT NULL, 
	name VARCHAR(128) NOT NULL, 
	version VARCHAR(32), 
	mtls_fingerprint VARCHAR(128), 
	enrollment_token_hash VARCHAR(255), 
	status collector_status NOT NULL, 
	capabilities JSON NOT NULL, 
	last_seen_at TIMESTAMP WITH TIME ZONE, 
	last_ingest_at TIMESTAMP WITH TIME ZONE, 
	buffered_samples INTEGER NOT NULL, 
	enabled BOOLEAN NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	UNIQUE (mtls_fingerprint)
)
""")
    op.execute("""CREATE INDEX ix_collectors_status ON collectors (status)""")
    op.execute("""CREATE INDEX ix_collectors_site ON collectors (site_id)""")
    op.execute("""
CREATE TABLE maintenance_windows (
	name VARCHAR(128) NOT NULL, 
	site_id UUID, 
	asset_filter_json JSON NOT NULL, 
	starts_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	ends_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	created_by VARCHAR(255), 
	reason VARCHAR(512), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id)
)
""")
    op.execute("""CREATE INDEX ix_mw_site ON maintenance_windows (site_id)""")
    op.execute("""CREATE INDEX ix_mw_window ON maintenance_windows (starts_at, ends_at)""")
    op.execute("""
CREATE TABLE role_scopes (
	assignment_id UUID NOT NULL, 
	scope_type scope_type NOT NULL, 
	target_id VARCHAR(255), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(assignment_id) REFERENCES user_roles (id)
)
""")
    op.execute("""CREATE INDEX ix_role_scopes_target ON role_scopes (scope_type, target_id)""")
    op.execute("""CREATE INDEX ix_role_scopes_assignment ON role_scopes (assignment_id)""")
    op.execute("""
CREATE TABLE site_group_memberships (
	site_id UUID NOT NULL, 
	group_id UUID NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_site_group_member UNIQUE (site_id, group_id), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(group_id) REFERENCES site_groups (id)
)
""")
    op.execute("""
CREATE TABLE collector_heartbeats (
	collector_id UUID NOT NULL, 
	received_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	queue_depth INTEGER NOT NULL, 
	last_error VARCHAR(512), 
	metrics_json JSON NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(collector_id) REFERENCES collectors (id)
)
""")
    op.execute("""CREATE INDEX ix_heartbeats_collector_ts ON collector_heartbeats (collector_id, received_at)""")
    op.execute("""
CREATE TABLE rooms (
	building_id UUID NOT NULL, 
	name VARCHAR(128) NOT NULL, 
	code VARCHAR(32) NOT NULL, 
	floor_area_sqft INTEGER, 
	design_kw NUMERIC(10, 2), 
	design_cooling_tons NUMERIC(10, 2), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_room_building_code UNIQUE (building_id, code), 
	FOREIGN KEY(building_id) REFERENCES buildings (id)
)
""")
    op.execute("""CREATE INDEX ix_rooms_building ON rooms (building_id)""")
    op.execute("""
CREATE TABLE rows (
	room_id UUID NOT NULL, 
	name VARCHAR(64) NOT NULL, 
	code VARCHAR(32) NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_row_room_code UNIQUE (room_id, code), 
	FOREIGN KEY(room_id) REFERENCES rooms (id)
)
""")
    op.execute("""CREATE INDEX ix_rows_room ON rows (room_id)""")
    op.execute("""
CREATE TABLE racks (
	site_id UUID NOT NULL, 
	row_id UUID NOT NULL, 
	name VARCHAR(64) NOT NULL, 
	code VARCHAR(32) NOT NULL, 
	u_height INTEGER NOT NULL, 
	max_kw NUMERIC(8, 2), 
	max_weight_lbs INTEGER, 
	serial VARCHAR(128), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_rack_row_code UNIQUE (row_id, code), 
	CONSTRAINT ck_rack_u_height CHECK (u_height > 0 AND u_height <= 60), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(row_id) REFERENCES rows (id)
)
""")
    op.execute("""CREATE INDEX ix_racks_row ON racks (row_id)""")
    op.execute("""CREATE INDEX ix_racks_site ON racks (site_id)""")
    op.execute("""
CREATE TABLE assets (
	site_id UUID NOT NULL, 
	rack_id UUID, 
	parent_asset_id UUID, 
	name VARCHAR(255) NOT NULL, 
	hostname VARCHAR(255), 
	kind asset_kind NOT NULL, 
	manufacturer VARCHAR(128), 
	model VARCHAR(128), 
	serial VARCHAR(128), 
	firmware VARCHAR(64), 
	rack_position_u INTEGER, 
	rack_units INTEGER, 
	mgmt_ip VARCHAR(64), 
	mgmt_protocol VARCHAR(16), 
	mgmt_port INTEGER, 
	mgmt_credentials_ref VARCHAR(128), 
	lifecycle_state lifecycle_state NOT NULL, 
	install_date VARCHAR(32), 
	warranty_expires VARCHAR(32), 
	metadata_json JSON NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_asset_serial_manufacturer UNIQUE (serial, manufacturer), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(rack_id) REFERENCES racks (id), 
	FOREIGN KEY(parent_asset_id) REFERENCES assets (id)
)
""")
    op.execute("""CREATE INDEX ix_assets_lifecycle ON assets (lifecycle_state)""")
    op.execute("""CREATE INDEX ix_assets_serial ON assets (serial)""")
    op.execute("""CREATE INDEX ix_assets_hostname ON assets (hostname)""")
    op.execute("""CREATE INDEX ix_assets_kind ON assets (kind)""")
    op.execute("""CREATE INDEX ix_assets_site ON assets (site_id)""")
    op.execute("""CREATE INDEX ix_assets_rack ON assets (rack_id)""")
    op.execute("""
CREATE TABLE alerts (
	rule_id UUID, 
	site_id UUID NOT NULL, 
	asset_id UUID, 
	collector_id UUID, 
	severity alert_severity NOT NULL, 
	state alert_state NOT NULL, 
	dedupe_key VARCHAR(255) NOT NULL, 
	correlation_key VARCHAR(255), 
	summary VARCHAR(512) NOT NULL, 
	detail VARCHAR(2048), 
	first_seen_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	last_seen_at TIMESTAMP WITH TIME ZONE NOT NULL, 
	acked_by VARCHAR(255), 
	acked_at TIMESTAMP WITH TIME ZONE, 
	resolved_at TIMESTAMP WITH TIME ZONE, 
	labels_json JSON NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(rule_id) REFERENCES alert_rules (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(asset_id) REFERENCES assets (id), 
	FOREIGN KEY(collector_id) REFERENCES collectors (id)
)
""")
    op.execute("""CREATE INDEX ix_alerts_severity ON alerts (severity)""")
    op.execute("""CREATE INDEX ix_alerts_dedupe ON alerts (dedupe_key)""")
    op.execute("""CREATE INDEX ix_alerts_state ON alerts (state)""")
    op.execute("""CREATE INDEX ix_alerts_site ON alerts (site_id)""")
    op.execute("""
CREATE TABLE cables (
	site_id UUID NOT NULL, 
	a_asset_id UUID NOT NULL, 
	a_port VARCHAR(64), 
	b_asset_id UUID NOT NULL, 
	b_port VARCHAR(64), 
	medium VARCHAR(32), 
	color VARCHAR(16), 
	length_m NUMERIC(6, 2), 
	label VARCHAR(128), 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(a_asset_id) REFERENCES assets (id), 
	FOREIGN KEY(b_asset_id) REFERENCES assets (id)
)
""")
    op.execute("""CREATE INDEX ix_cables_a_end ON cables (a_asset_id)""")
    op.execute("""CREATE INDEX ix_cables_site ON cables (site_id)""")
    op.execute("""CREATE INDEX ix_cables_b_end ON cables (b_asset_id)""")
    op.execute("""
CREATE TABLE power_feeds (
	site_id UUID NOT NULL, 
	rack_id UUID, 
	name VARCHAR(128) NOT NULL, 
	side VARCHAR(8), 
	voltage INTEGER, 
	amperage INTEGER, 
	phase VARCHAR(8), 
	upstream_pdu_id UUID, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(rack_id) REFERENCES racks (id), 
	FOREIGN KEY(upstream_pdu_id) REFERENCES assets (id)
)
""")
    op.execute("""CREATE INDEX ix_power_feeds_site ON power_feeds (site_id)""")
    op.execute("""CREATE INDEX ix_power_feeds_rack ON power_feeds (rack_id)""")
    op.execute("""
CREATE TABLE telemetry_sources (
	site_id UUID NOT NULL, 
	asset_id UUID NOT NULL, 
	collector_id UUID, 
	metric VARCHAR(128) NOT NULL, 
	unit VARCHAR(32), 
	source_system VARCHAR(64), 
	last_success_at TIMESTAMP WITH TIME ZONE, 
	last_failure_at TIMESTAMP WITH TIME ZONE, 
	last_reading_at TIMESTAMP WITH TIME ZONE, 
	last_value FLOAT, 
	freshness freshness_state NOT NULL, 
	poll_interval_seconds INTEGER NOT NULL, 
	id UUID NOT NULL, 
	created_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	updated_at TIMESTAMP WITH TIME ZONE DEFAULT now() NOT NULL, 
	PRIMARY KEY (id), 
	CONSTRAINT uq_telem_source_asset_metric UNIQUE (asset_id, metric), 
	FOREIGN KEY(site_id) REFERENCES sites (id), 
	FOREIGN KEY(asset_id) REFERENCES assets (id), 
	FOREIGN KEY(collector_id) REFERENCES collectors (id)
)
""")
    op.execute("""CREATE INDEX ix_telem_source_collector ON telemetry_sources (collector_id)""")
    op.execute("""CREATE INDEX ix_telem_source_site ON telemetry_sources (site_id)""")
    op.execute("""CREATE INDEX ix_telem_source_freshness ON telemetry_sources (freshness)""")


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS telemetry_sources CASCADE")
    op.execute("DROP TABLE IF EXISTS power_feeds CASCADE")
    op.execute("DROP TABLE IF EXISTS cables CASCADE")
    op.execute("DROP TABLE IF EXISTS alerts CASCADE")
    op.execute("DROP TABLE IF EXISTS assets CASCADE")
    op.execute("DROP TABLE IF EXISTS racks CASCADE")
    op.execute("DROP TABLE IF EXISTS rows CASCADE")
    op.execute("DROP TABLE IF EXISTS rooms CASCADE")
    op.execute("DROP TABLE IF EXISTS collector_heartbeats CASCADE")
    op.execute("DROP TABLE IF EXISTS site_group_memberships CASCADE")
    op.execute("DROP TABLE IF EXISTS role_scopes CASCADE")
    op.execute("DROP TABLE IF EXISTS maintenance_windows CASCADE")
    op.execute("DROP TABLE IF EXISTS collectors CASCADE")
    op.execute("DROP TABLE IF EXISTS circuits CASCADE")
    op.execute("DROP TABLE IF EXISTS buildings CASCADE")
    op.execute("DROP TABLE IF EXISTS audit_log CASCADE")
    op.execute("DROP TABLE IF EXISTS alert_rules CASCADE")
    op.execute("DROP TABLE IF EXISTS user_roles CASCADE")
    op.execute("DROP TABLE IF EXISTS sites CASCADE")
    op.execute("DROP TABLE IF EXISTS api_tokens CASCADE")
    op.execute("DROP TABLE IF EXISTS users CASCADE")
    op.execute("DROP TABLE IF EXISTS site_groups CASCADE")
    op.execute("DROP TABLE IF EXISTS roles CASCADE")
    op.execute("DROP TABLE IF EXISTS regions CASCADE")
    op.execute("DROP TABLE IF EXISTS permissions CASCADE")
    op.execute("DROP TYPE IF EXISTS alert_severity CASCADE")
    op.execute("DROP TYPE IF EXISTS alert_state CASCADE")
    op.execute("DROP TYPE IF EXISTS asset_kind CASCADE")
    op.execute("DROP TYPE IF EXISTS collector_status CASCADE")
    op.execute("DROP TYPE IF EXISTS freshness_state CASCADE")
    op.execute("DROP TYPE IF EXISTS lifecycle_state CASCADE")
    op.execute("DROP TYPE IF EXISTS scope_type CASCADE")
