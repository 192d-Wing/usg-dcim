-- +goose Up
ALTER TABLE bgp_asns ALTER COLUMN asn TYPE BIGINT;

        INSERT INTO bgp_asns (id, asn, name, kind, description)
        VALUES (
            gen_random_uuid(),
            4200000000,
            'DCIM DNS anycast',
            'private',
            'Originating AS for all DNS recursive anycast announcements.'
        )
        ON CONFLICT (asn) DO NOTHING;

-- +goose Down
DELETE FROM bgp_asns WHERE asn = 4200000000;
ALTER TABLE bgp_asns ALTER COLUMN asn TYPE INTEGER;
