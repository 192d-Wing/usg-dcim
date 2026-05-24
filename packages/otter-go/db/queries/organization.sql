-- name: ListOrganizations :many
SELECT id, name, arin_org_id,
       address_line1, address_line2, city, state_province, postal_code, country,
       phone, email,
       admin_poc_name, admin_poc_email, admin_poc_phone,
       tech_poc_name,  tech_poc_email,  tech_poc_phone,
       abuse_poc_name, abuse_poc_email, abuse_poc_phone,
       noc_poc_name,   noc_poc_email,   noc_poc_phone,
       description, created_at, updated_at
FROM organizations
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountOrganizations :one
SELECT count(*)::bigint FROM organizations;

-- name: GetOrganization :one
SELECT id, name, arin_org_id,
       address_line1, address_line2, city, state_province, postal_code, country,
       phone, email,
       admin_poc_name, admin_poc_email, admin_poc_phone,
       tech_poc_name,  tech_poc_email,  tech_poc_phone,
       abuse_poc_name, abuse_poc_email, abuse_poc_phone,
       noc_poc_name,   noc_poc_email,   noc_poc_phone,
       description, created_at, updated_at
FROM organizations
WHERE id = $1;
