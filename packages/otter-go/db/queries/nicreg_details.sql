-- DoD NIC registration intake — typed detail rows (one table per template).
-- Each Create runs in the same tx as CreateNicRegistration (header). Each Get
-- is keyed by registration_id. Column shapes mirror
-- internal/nicreg/templates.json.

-- ---- organization ----

-- name: CreateNicRegOrganization :one
INSERT INTO nic_reg_organization (
    registration_id, agency, primary_org_poc, secondary_org_poc,
    organization_name, address_line1, address_line2, address_line3,
    address_line4, city, state_code, zip_code, org_mailbox, user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
) RETURNING *;

-- name: GetNicRegOrganization :one
SELECT * FROM nic_reg_organization WHERE registration_id = $1;

-- ---- user ----

-- name: CreateNicRegUser :one
INSERT INTO nic_reg_user (
    registration_id, last_name, first_name, middle_initial, name_suffix,
    title_rank, address_line1, address_line2, address_line3, address_line4,
    city, state_code, zip_code, email, email_secondary, commercial_phone,
    commercial_phone_ext, dsn_phone, dsn_phone_ext, fax, tld, user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21, $22
) RETURNING *;

-- name: GetNicRegUser :one
SELECT * FROM nic_reg_user WHERE registration_id = $1;

-- ---- host ----

-- name: CreateNicRegHost :one
INSERT INTO nic_reg_host (
    registration_id, agency, org_handle, primary_poc_handle,
    secondary_poc_handle, hostname, role_mailbox, ip_addresses, user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
) RETURNING *;

-- name: GetNicRegHost :one
SELECT * FROM nic_reg_host WHERE registration_id = $1;

-- ---- domain ----

-- name: CreateNicRegDomain :one
INSERT INTO nic_reg_domain (
    registration_id, agency, org_handle, tech_poc_handle, admin_poc_handle,
    zone_admin1, zone_admin2, domain_name, role_mailbox, dns_server_hostnames,
    mx_server_hostname, req_charter, req_firewalled, req_no_source_route,
    req_dns_exclusive, req_ups, req_subordinate_protect, req_diverse_paths,
    req_whois_registered, justification, user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21
) RETURNING *;

-- name: GetNicRegDomain :one
SELECT * FROM nic_reg_domain WHERE registration_id = $1;

-- ---- network ----

-- name: CreateNicRegNetwork :one
INSERT INTO nic_reg_network (
    registration_id, agency, org_handle, tech_poc_handle, admin_poc_handle,
    zone_admin, ip_version, network_aggregator, classification,
    customer_network_name, tactical_network, ccsd, niprnet_hub_identifier,
    ccs_platform, ccs_provider, ccs_region, network_number, cidr,
    hosts_initial, hosts_6mo, hosts_max, disn_transport, geophysical_location,
    num_48_requested, inaddr_hostname1, inaddr_ip1, inaddr_hostname2,
    inaddr_ip2, justification, user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30
) RETURNING *;

-- name: GetNicRegNetwork :one
SELECT * FROM nic_reg_network WHERE registration_id = $1;

-- ---- asn ----

-- name: CreateNicRegAsn :one
INSERT INTO nic_reg_asn (
    registration_id, agency, org_handle, tech_poc_handle, admin_poc_handle,
    as_number, network_aggregator, classification, customer_asn_name, igp,
    egp, site_premise_router, hub_router, num_routers, router_ips,
    num_networks, network_ips, justification, user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19
) RETURNING *;

-- name: GetNicRegAsn :one
SELECT * FROM nic_reg_asn WHERE registration_id = $1;

-- ---- dnskey ----

-- name: CreateNicRegDnskey :one
INSERT INTO nic_reg_dnskey (
    registration_id, domain_handle, start_date, end_date, ksk_value,
    user_comments
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: GetNicRegDnskey :one
SELECT * FROM nic_reg_dnskey WHERE registration_id = $1;
