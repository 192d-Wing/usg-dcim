-- +goose Up
CREATE TYPE region_deployment_status AS ENUM (
    'pending', 'preflight', 'provisioning', 'joining',
    'cni', 'apps', 'verify', 'ready', 'failed', 'aborted'
);
CREATE TYPE region_deployment_node_role AS ENUM (
    'control_plane', 'worker', 'edge'
);
CREATE TYPE region_deployment_node_status AS ENUM (
    'pending', 'pxe_boot', 'installing', 'joining', 'ready', 'failed'
);
CREATE TYPE region_deployment_event_level AS ENUM (
    'info', 'warn', 'error'
);
CREATE TYPE region_deployment_service_kind AS ENUM (
    'dns_auth', 'dns_recursive', 'dhcp', 'collector'
);
CREATE TYPE region_deployment_service_status AS ENUM (
    'pending', 'installing', 'ready', 'failed'
);

CREATE TABLE region_deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
    name VARCHAR(128) NOT NULL,
    status region_deployment_status NOT NULL
        DEFAULT 'pending'::region_deployment_status,
    current_stage VARCHAR(64),
    last_error TEXT,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    kubeconfig_secret_ref VARCHAR(255),
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX ix_region_deployments_site ON region_deployments (site_id);
CREATE INDEX ix_region_deployments_status ON region_deployments (status);

CREATE TABLE region_deployment_nodes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL
        REFERENCES region_deployments(id) ON DELETE CASCADE,
    hostname VARCHAR(255) NOT NULL,
    mac MACADDR NOT NULL,
    primary_ip_v6 INET,
    provisioning_ip_v6 INET,
    bmc_address INET NOT NULL,
    bmc_creds_secret_ref VARCHAR(255),
    role region_deployment_node_role NOT NULL,
    status region_deployment_node_status NOT NULL
        DEFAULT 'pending'::region_deployment_node_status,
    last_event TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    joined_at TIMESTAMPTZ,
    CONSTRAINT uq_rdn_deployment_mac UNIQUE (deployment_id, mac),
    CONSTRAINT uq_rdn_deployment_hostname UNIQUE (deployment_id, hostname)
);
CREATE INDEX ix_region_deployment_nodes_deployment
    ON region_deployment_nodes (deployment_id);

CREATE TABLE region_deployment_events (
    id BIGSERIAL PRIMARY KEY,
    deployment_id UUID NOT NULL
        REFERENCES region_deployments(id) ON DELETE CASCADE,
    stage VARCHAR(64) NOT NULL,
    level region_deployment_event_level NOT NULL
        DEFAULT 'info'::region_deployment_event_level,
    message TEXT NOT NULL,
    payload JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Composite index for SSE catch-up:
--   WHERE deployment_id = ? AND id > ? ORDER BY id
CREATE INDEX ix_region_deployment_events_deployment_id
    ON region_deployment_events (deployment_id, id);

CREATE TABLE region_deployment_services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    deployment_id UUID NOT NULL
        REFERENCES region_deployments(id) ON DELETE CASCADE,
    service region_deployment_service_kind NOT NULL,
    chart_version VARCHAR(64),
    values_override JSONB NOT NULL DEFAULT '{}'::jsonb,
    status region_deployment_service_status NOT NULL
        DEFAULT 'pending'::region_deployment_service_status,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_rds_deployment_service UNIQUE (deployment_id, service)
);
CREATE INDEX ix_region_deployment_services_deployment
    ON region_deployment_services (deployment_id);

-- +goose Down
DROP INDEX IF EXISTS ix_region_deployment_services_deployment;
DROP TABLE IF EXISTS region_deployment_services;
DROP INDEX IF EXISTS ix_region_deployment_events_deployment_id;
DROP TABLE IF EXISTS region_deployment_events;
DROP INDEX IF EXISTS ix_region_deployment_nodes_deployment;
DROP TABLE IF EXISTS region_deployment_nodes;
DROP INDEX IF EXISTS ix_region_deployments_status;
DROP INDEX IF EXISTS ix_region_deployments_site;
DROP TABLE IF EXISTS region_deployments;
DROP TYPE IF EXISTS region_deployment_service_status;
DROP TYPE IF EXISTS region_deployment_service_kind;
DROP TYPE IF EXISTS region_deployment_event_level;
DROP TYPE IF EXISTS region_deployment_node_status;
DROP TYPE IF EXISTS region_deployment_node_role;
DROP TYPE IF EXISTS region_deployment_status;
