-- +goose Up

        CREATE TABLE IF NOT EXISTS organizations (
            id UUID PRIMARY KEY,
            name VARCHAR(256) NOT NULL,
            arin_org_id VARCHAR(64),

            address_line1 VARCHAR(256) NOT NULL,
            address_line2 VARCHAR(256),
            city VARCHAR(128) NOT NULL,
            state_province VARCHAR(64),
            postal_code VARCHAR(32),
            country VARCHAR(2) NOT NULL,

            phone VARCHAR(64),
            email VARCHAR(256),

            admin_poc_name VARCHAR(128) NOT NULL,
            admin_poc_email VARCHAR(256) NOT NULL,
            admin_poc_phone VARCHAR(64),

            tech_poc_name VARCHAR(128) NOT NULL,
            tech_poc_email VARCHAR(256) NOT NULL,
            tech_poc_phone VARCHAR(64),

            abuse_poc_name VARCHAR(128) NOT NULL,
            abuse_poc_email VARCHAR(256) NOT NULL,
            abuse_poc_phone VARCHAR(64),

            noc_poc_name VARCHAR(128),
            noc_poc_email VARCHAR(256),
            noc_poc_phone VARCHAR(64),

            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
ALTER TABLE bgp_asns DROP COLUMN IF EXISTS organization;
ALTER TABLE bgp_asns ADD COLUMN IF NOT EXISTS organization_id UUID REFERENCES organizations(id);
ALTER TABLE tcp_ao_keys ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ;
ALTER TABLE tcp_ao_keys ADD COLUMN IF NOT EXISTS valid_to TIMESTAMPTZ;

-- +goose Down
ALTER TABLE tcp_ao_keys DROP COLUMN IF EXISTS valid_to;
ALTER TABLE tcp_ao_keys DROP COLUMN IF EXISTS valid_from;
ALTER TABLE bgp_asns DROP COLUMN IF EXISTS organization_id;
ALTER TABLE bgp_asns ADD COLUMN IF NOT EXISTS organization VARCHAR(256);
DROP TABLE IF EXISTS organizations;
