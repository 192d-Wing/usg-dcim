-- +goose Up

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE bgp_asn_kind AS ENUM (
                'public','private','documentation','reserved'
            );
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE tcp_ao_algorithm AS ENUM (
                'hmac-sha1-96','aes-128-cmac'
            );
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE bgp_policy_action AS ENUM ('permit','deny');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE address_family_v4v6 AS ENUM ('v4','v6');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

-- +goose StatementBegin
        DO $$ BEGIN
            CREATE TYPE bgp_community_kind AS ENUM ('standard','extended');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
-- +goose StatementEnd

        CREATE TABLE IF NOT EXISTS bgp_asns (
            id UUID PRIMARY KEY,
            asn INTEGER NOT NULL,
            name VARCHAR(128) NOT NULL,
            kind bgp_asn_kind NOT NULL DEFAULT 'private',
            organization VARCHAR(256),
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_bgp_asn UNIQUE (asn)
        );
CREATE INDEX IF NOT EXISTS ix_bgp_asns_kind ON bgp_asns (kind);

        CREATE TABLE IF NOT EXISTS tcp_ao_key_chains (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_tcp_ao_key_chain_name UNIQUE (name)
        );

        CREATE TABLE IF NOT EXISTS tcp_ao_keys (
            id UUID PRIMARY KEY,
            key_chain_id UUID NOT NULL REFERENCES tcp_ao_key_chains(id),
            key_id INTEGER NOT NULL,
            send_id INTEGER NOT NULL,
            recv_id INTEGER NOT NULL,
            algorithm tcp_ao_algorithm NOT NULL,
            secret VARCHAR(512) NOT NULL,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_tcp_ao_key_chain_keyid UNIQUE (key_chain_id, key_id)
        );
CREATE INDEX IF NOT EXISTS ix_tcp_ao_keys_chain ON tcp_ao_keys (key_chain_id);

        CREATE TABLE IF NOT EXISTS bgp_prefix_lists (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            family address_family_v4v6 NOT NULL,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_prefix_list_name_family UNIQUE (name, family)
        );

        CREATE TABLE IF NOT EXISTS bgp_prefix_list_entries (
            id UUID PRIMARY KEY,
            prefix_list_id UUID NOT NULL REFERENCES bgp_prefix_lists(id),
            seq INTEGER NOT NULL,
            action bgp_policy_action NOT NULL,
            prefix CIDR NOT NULL,
            ge INTEGER,
            le INTEGER,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_prefix_list_seq UNIQUE (prefix_list_id, seq)
        );
CREATE INDEX IF NOT EXISTS ix_prefix_list_entries_list ON bgp_prefix_list_entries (prefix_list_id);

        CREATE TABLE IF NOT EXISTS bgp_community_lists (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            kind bgp_community_kind NOT NULL DEFAULT 'standard',
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_community_list_name UNIQUE (name)
        );

        CREATE TABLE IF NOT EXISTS bgp_community_list_entries (
            id UUID PRIMARY KEY,
            community_list_id UUID NOT NULL REFERENCES bgp_community_lists(id),
            seq INTEGER NOT NULL,
            action bgp_policy_action NOT NULL,
            value VARCHAR(128) NOT NULL,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_community_list_seq UNIQUE (community_list_id, seq)
        );
CREATE INDEX IF NOT EXISTS ix_community_list_entries_list ON bgp_community_list_entries (community_list_id);

        CREATE TABLE IF NOT EXISTS bgp_route_maps (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_route_map_name UNIQUE (name)
        );

        CREATE TABLE IF NOT EXISTS bgp_route_map_entries (
            id UUID PRIMARY KEY,
            route_map_id UUID NOT NULL REFERENCES bgp_route_maps(id),
            seq INTEGER NOT NULL,
            action bgp_policy_action NOT NULL,
            match_prefix_list_id UUID REFERENCES bgp_prefix_lists(id),
            match_community_list_id UUID REFERENCES bgp_community_lists(id),
            match_as_path_regex VARCHAR(256),
            set_local_pref INTEGER,
            set_med INTEGER,
            set_community VARCHAR(256),
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_route_map_seq UNIQUE (route_map_id, seq)
        );
CREATE INDEX IF NOT EXISTS ix_route_map_entries_map ON bgp_route_map_entries (route_map_id);

-- +goose Down
DROP TABLE IF EXISTS bgp_route_map_entries;
DROP TABLE IF EXISTS bgp_route_maps;
DROP TABLE IF EXISTS bgp_community_list_entries;
DROP TABLE IF EXISTS bgp_community_lists;
DROP TABLE IF EXISTS bgp_prefix_list_entries;
DROP TABLE IF EXISTS bgp_prefix_lists;
DROP TABLE IF EXISTS tcp_ao_keys;
DROP TABLE IF EXISTS tcp_ao_key_chains;
DROP TABLE IF EXISTS bgp_asns;
DROP TYPE IF EXISTS bgp_community_kind;
DROP TYPE IF EXISTS address_family_v4v6;
DROP TYPE IF EXISTS bgp_policy_action;
DROP TYPE IF EXISTS tcp_ao_algorithm;
DROP TYPE IF EXISTS bgp_asn_kind;
