-- Region Deploy read-side queries. Lifecycle / mutation queries land
-- in a separate PR once the orchestrator (arq → Go scheduler) port
-- is on deck — these read paths stay self-contained.

-- name: ListRegionDeployments :many
-- Summary view for the list table: enough for the operator's
-- triage glance, no config blob. Site-scope filter via the
-- ScopedSiteFilter slice; NULL = unscoped/global.
SELECT id, site_id, name, status::text AS status, current_stage,
       created_at, started_at, finished_at
FROM region_deployments
WHERE (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id = ANY(sqlc.narg(scope_site_ids)::uuid[]))
-- id DESC tiebreaker keeps page boundaries stable when two rows
-- share the same created_at (e.g. bulk-seeded deploys).
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2;

-- name: CountRegionDeployments :one
SELECT count(*)::bigint
FROM region_deployments
WHERE (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id = ANY(sqlc.narg(scope_site_ids)::uuid[]));

-- name: GetRegionDeployment :one
-- Detail row including config JSONB. Nodes + services are pulled
-- with separate queries; matches Python's selectinload pattern.
SELECT id, site_id, name, status::text AS status, current_stage,
       last_error, config, kubeconfig_secret_ref, created_by,
       created_at, updated_at, started_at, finished_at
FROM region_deployments
WHERE id = $1;

-- name: ListRegionDeploymentNodes :many
-- mac/inet columns rendered as text so pgx scans them as plain
-- strings (it has no first-class scanner for MACADDR + INET when
-- the row also needs uuid/text fields next to them). host() on
-- inet drops the /128 netmask Python's str(IPAddress) would emit.
SELECT id, deployment_id, hostname,
       mac::text AS mac,
       host(primary_ip_v6)      AS primary_ip_v6,
       host(provisioning_ip_v6) AS provisioning_ip_v6,
       host(bmc_address)        AS bmc_address,
       role::text AS role,
       status::text AS status,
       last_event, joined_at
FROM region_deployment_nodes
WHERE deployment_id = $1
ORDER BY hostname;

-- name: ListRegionDeploymentServices :many
SELECT id, deployment_id, service::text AS service, chart_version,
       status::text AS status, last_error
FROM region_deployment_services
WHERE deployment_id = $1
ORDER BY service;

-- name: ListRegionDeploymentEvents :many
-- Cursor-by-id history. The SSE handler (separate PR) uses this
-- same shape for catch-up on reconnect.
SELECT id, stage, level::text AS level, message, payload, created_at
FROM region_deployment_events
WHERE deployment_id = $1
  AND id > $2
ORDER BY id ASC
LIMIT $3;

-- name: AbortRegionDeployment :one
-- Conditional UPDATE: only flips to 'aborted' when the current
-- status is not already terminal. The CTE returns the prior status
-- regardless so the handler can distinguish 404 (no row), 422
-- (terminal — ready/aborted), and the success path with a single
-- round-trip. When the deployment id is missing, `cur` is empty so
-- the outer `SELECT ... FROM cur` returns 0 rows and pgx's
-- QueryRow.Scan reports pgx.ErrNoRows; the handler treats that as
-- 404 (caller raced a delete).
WITH cur AS (
    SELECT status::text AS prior_status
    FROM region_deployments
    WHERE id = $1
), upd AS (
    UPDATE region_deployments
    SET status = 'aborted'::region_deployment_status, updated_at = NOW()
    WHERE id = $1
      AND status NOT IN ('ready'::region_deployment_status,
                         'aborted'::region_deployment_status)
    RETURNING 1
)
SELECT cur.prior_status, (SELECT count(*) FROM upd)::bigint AS updated
FROM cur;
