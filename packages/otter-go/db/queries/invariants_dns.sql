-- name: GetZoneFrozenByRecord :one
-- Used by record PATCH/DELETE handlers to check the frozen flag of the
-- record's parent zone without having to round-trip through GetDnsRecord
-- + GetDnsZone. Returns the zone id + frozen flag.
SELECT z.id AS zone_id, z.frozen
FROM dns_records r
JOIN dns_zones z ON z.id = r.zone_id
WHERE r.id = sqlc.arg(record_id);
