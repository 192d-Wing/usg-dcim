-- +goose Up
-- DoD NIC registration intake (end-user / internal DoD customer registration).
--
-- Header + typed-detail pattern: nic_registrations carries the shared
-- lifecycle (org being tracked, requester, status, the NIC's push_to_arin
-- decision); one detail table per template type holds the typed fields.
-- Field shapes are the canonical schema in
-- packages/otter-go/internal/nicreg/templates.json.
--
-- First cut is form-capture only: data is captured and a NIC reviewer can
-- approve/reject and record whether the registration should flow upstream to
-- ARIN. Acting on push_to_arin (the existing internal/lir/arin worker) and
-- rendering the NIC template text are later milestones.

CREATE TABLE IF NOT EXISTS nic_registrations (
    id UUID PRIMARY KEY,
    template_type VARCHAR(32) NOT NULL,
    action_type CHAR(1) NOT NULL,
    -- The internal DoD customer this registration is tracked under.
    organization_id UUID NOT NULL REFERENCES organizations(id),
    requester_user_id UUID NOT NULL REFERENCES users(id),
    status VARCHAR(32) NOT NULL DEFAULT 'draft',
    -- NIC's approval-time decision: should this registration flow upstream
    -- to ARIN? NULL until decided; only meaningful for ARIN-eligible types
    -- (network, asn) but recorded uniformly so the worker bridge added later
    -- needs no schema change.
    push_to_arin BOOLEAN,
    submitted_at TIMESTAMPTZ,
    decided_at TIMESTAMPTZ,
    decided_by_user_id UUID REFERENCES users(id),
    decision_notes VARCHAR(2048),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ck_nicreg_template_type CHECK (
        template_type IN (
            'organization', 'user', 'host', 'domain',
            'network', 'asn', 'dnskey'
        )
    ),
    CONSTRAINT ck_nicreg_action_type CHECK (action_type IN ('N', 'M', 'D', 'R')),
    CONSTRAINT ck_nicreg_status CHECK (
        status IN ('draft', 'submitted', 'approved', 'rejected', 'cancelled')
    ),
    -- draft/submitted/cancelled carry no decision; approved/rejected always
    -- record who/when. push_to_arin may only be set on a decided row.
    CONSTRAINT ck_nicreg_decision_consistency CHECK (
        (status IN ('draft', 'submitted', 'cancelled'))
        OR (decided_at IS NOT NULL AND decided_by_user_id IS NOT NULL)
    ),
    CONSTRAINT ck_nicreg_arin_only_when_decided CHECK (
        push_to_arin IS NULL OR status IN ('approved', 'rejected')
    )
);
CREATE INDEX IF NOT EXISTS ix_nicreg_org ON nic_registrations (organization_id);
CREATE INDEX IF NOT EXISTS ix_nicreg_requester ON nic_registrations (requester_user_id);
CREATE INDEX IF NOT EXISTS ix_nicreg_status ON nic_registrations (status);
CREATE INDEX IF NOT EXISTS ix_nicreg_type ON nic_registrations (template_type);
CREATE INDEX IF NOT EXISTS ix_nicreg_created_at ON nic_registrations (created_at DESC);

CREATE TABLE IF NOT EXISTS nic_reg_organization (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    agency VARCHAR(64) NOT NULL,
    primary_org_poc VARCHAR(128),
    secondary_org_poc VARCHAR(128),
    organization_name VARCHAR(128) NOT NULL,
    address_line1 VARCHAR(128) NOT NULL,
    address_line2 VARCHAR(128),
    address_line3 VARCHAR(128),
    address_line4 VARCHAR(128),
    city VARCHAR(128) NOT NULL,
    state_code VARCHAR(16) NOT NULL,
    zip_code VARCHAR(16),
    org_mailbox VARCHAR(256),
    user_comments TEXT
);

CREATE TABLE IF NOT EXISTS nic_reg_user (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    last_name VARCHAR(128) NOT NULL,
    first_name VARCHAR(128) NOT NULL,
    middle_initial VARCHAR(8),
    name_suffix VARCHAR(32),
    title_rank VARCHAR(64),
    address_line1 VARCHAR(128) NOT NULL,
    address_line2 VARCHAR(128),
    address_line3 VARCHAR(128),
    address_line4 VARCHAR(128),
    city VARCHAR(128) NOT NULL,
    state_code VARCHAR(16) NOT NULL,
    zip_code VARCHAR(16) NOT NULL,
    email VARCHAR(256) NOT NULL,
    email_secondary VARCHAR(256),
    commercial_phone VARCHAR(32) NOT NULL,
    commercial_phone_ext VARCHAR(16),
    dsn_phone VARCHAR(32),
    dsn_phone_ext VARCHAR(16),
    fax VARCHAR(32),
    tld VARCHAR(64),
    user_comments TEXT
);

CREATE TABLE IF NOT EXISTS nic_reg_host (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    agency VARCHAR(64),
    org_handle VARCHAR(128) NOT NULL,
    primary_poc_handle VARCHAR(128) NOT NULL,
    secondary_poc_handle VARCHAR(128) NOT NULL,
    hostname VARCHAR(256) NOT NULL,
    role_mailbox VARCHAR(256),
    ip_addresses TEXT[] NOT NULL,
    user_comments TEXT
);

CREATE TABLE IF NOT EXISTS nic_reg_domain (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    agency VARCHAR(64),
    org_handle VARCHAR(128) NOT NULL,
    tech_poc_handle VARCHAR(128) NOT NULL,
    admin_poc_handle VARCHAR(128) NOT NULL,
    zone_admin1 VARCHAR(128),
    zone_admin2 VARCHAR(128),
    domain_name VARCHAR(256) NOT NULL,
    role_mailbox VARCHAR(256),
    dns_server_hostnames TEXT[] NOT NULL,
    mx_server_hostname VARCHAR(256),
    req_charter BOOLEAN NOT NULL DEFAULT FALSE,
    req_firewalled BOOLEAN NOT NULL DEFAULT FALSE,
    req_no_source_route BOOLEAN NOT NULL DEFAULT FALSE,
    req_dns_exclusive BOOLEAN NOT NULL DEFAULT FALSE,
    req_ups BOOLEAN NOT NULL DEFAULT FALSE,
    req_subordinate_protect BOOLEAN NOT NULL DEFAULT FALSE,
    req_diverse_paths BOOLEAN NOT NULL DEFAULT FALSE,
    req_whois_registered BOOLEAN NOT NULL DEFAULT FALSE,
    justification TEXT,
    user_comments TEXT
);

CREATE TABLE IF NOT EXISTS nic_reg_network (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    agency VARCHAR(64),
    org_handle VARCHAR(128) NOT NULL,
    tech_poc_handle VARCHAR(128) NOT NULL,
    admin_poc_handle VARCHAR(128) NOT NULL,
    zone_admin VARCHAR(128),
    ip_version VARCHAR(8) NOT NULL,
    network_aggregator VARCHAR(32) NOT NULL,
    classification VARCHAR(32) NOT NULL,
    customer_network_name VARCHAR(128) NOT NULL,
    tactical_network VARCHAR(8),
    ccsd VARCHAR(64),
    niprnet_hub_identifier VARCHAR(64),
    ccs_platform VARCHAR(64),
    ccs_provider VARCHAR(64),
    ccs_region VARCHAR(64),
    -- IPv4 registration data
    network_number VARCHAR(64),
    cidr SMALLINT,
    hosts_initial INTEGER,
    hosts_6mo INTEGER,
    hosts_max INTEGER,
    -- IPv6 registration data
    disn_transport VARCHAR(8),
    geophysical_location VARCHAR(128),
    num_48_requested INTEGER,
    -- IN-ADDR DNS servers
    inaddr_hostname1 VARCHAR(256),
    inaddr_ip1 VARCHAR(64),
    inaddr_hostname2 VARCHAR(256),
    inaddr_ip2 VARCHAR(64),
    justification TEXT,
    user_comments TEXT,
    CONSTRAINT ck_nicreg_network_ip_version CHECK (ip_version IN ('ipv4', 'ipv6'))
);

CREATE TABLE IF NOT EXISTS nic_reg_asn (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    agency VARCHAR(64),
    org_handle VARCHAR(128) NOT NULL,
    tech_poc_handle VARCHAR(128) NOT NULL,
    admin_poc_handle VARCHAR(128) NOT NULL,
    as_number BIGINT,
    network_aggregator VARCHAR(32) NOT NULL,
    classification VARCHAR(32) NOT NULL,
    customer_asn_name VARCHAR(128) NOT NULL,
    igp VARCHAR(64),
    egp VARCHAR(64),
    site_premise_router VARCHAR(128),
    hub_router VARCHAR(128),
    num_routers INTEGER,
    router_ips TEXT,
    num_networks INTEGER,
    network_ips TEXT,
    justification TEXT NOT NULL,
    user_comments TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS nic_reg_dnskey (
    registration_id UUID PRIMARY KEY REFERENCES nic_registrations(id) ON DELETE CASCADE,
    domain_handle VARCHAR(256) NOT NULL,
    start_date DATE NOT NULL,
    end_date DATE NOT NULL,
    ksk_value TEXT,
    user_comments TEXT
);

-- +goose Down
DROP TABLE IF EXISTS nic_reg_dnskey;
DROP TABLE IF EXISTS nic_reg_asn;
DROP TABLE IF EXISTS nic_reg_network;
DROP TABLE IF EXISTS nic_reg_domain;
DROP TABLE IF EXISTS nic_reg_host;
DROP TABLE IF EXISTS nic_reg_user;
DROP TABLE IF EXISTS nic_reg_organization;
DROP TABLE IF EXISTS nic_registrations;
