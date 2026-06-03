-- +goose Up
DELETE FROM anycast_bgp_bindings;
DELETE FROM vrf_bgp_peers;
DELETE FROM bgp_peers;
ALTER TABLE bgp_peers DROP COLUMN IF EXISTS local_asn;
ALTER TABLE bgp_peers DROP COLUMN IF EXISTS peer_asn;
ALTER TABLE bgp_peers DROP COLUMN IF EXISTS md5_password;
ALTER TABLE bgp_peers ADD COLUMN local_asn_id UUID NOT NULL REFERENCES bgp_asns(id);
ALTER TABLE bgp_peers ADD COLUMN peer_asn_id UUID NOT NULL REFERENCES bgp_asns(id);
ALTER TABLE bgp_peers ADD COLUMN tcp_ao_key_chain_id UUID REFERENCES tcp_ao_key_chains(id);

-- +goose Down
DELETE FROM anycast_bgp_bindings;
DELETE FROM vrf_bgp_peers;
DELETE FROM bgp_peers;
ALTER TABLE bgp_peers DROP COLUMN IF EXISTS tcp_ao_key_chain_id;
ALTER TABLE bgp_peers DROP COLUMN IF EXISTS peer_asn_id;
ALTER TABLE bgp_peers DROP COLUMN IF EXISTS local_asn_id;
ALTER TABLE bgp_peers ADD COLUMN local_asn INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bgp_peers ADD COLUMN peer_asn INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bgp_peers ADD COLUMN md5_password VARCHAR(128);
