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

-- name: CreateRegionDeployment :one
-- Mirrors Python POST /region-deployments. Status defaults to
-- 'pending' via the DDL (region_deployments.status DEFAULT 'pending').
-- created_by is left NULL on insert — matches Python, which never
-- populates it; the authenticated principal is captured in the audit
-- log instead. config is validated as non-null at the handler layer
-- (see createReq.validate); we leave the column NOT NULL DEFAULT '{}'
-- enforcement to the DDL.
INSERT INTO region_deployments (site_id, name, config)
VALUES ($1, $2, $3::jsonb)
RETURNING id, site_id, name, status::text AS status, current_stage,
          last_error, config, kubeconfig_secret_ref, created_by,
          created_at, updated_at, started_at, finished_at;

-- name: CreateRegionDeploymentNode :one
-- Per-node INSERT called once per `nodes[]` entry inside the same tx
-- as CreateRegionDeployment. NULL primary_ip_v6/provisioning_ip_v6
-- are valid — they get filled in by the joining stage. mac is INET-
-- adjacent (macaddr type) so $4::macaddr keeps the cast explicit.
INSERT INTO region_deployment_nodes (
    deployment_id, hostname, mac, bmc_address, role,
    primary_ip_v6, provisioning_ip_v6, bmc_creds_secret_ref
)
VALUES (
    $1, $2, $3::macaddr, $4::inet,
    $5::region_deployment_node_role,
    NULLIF($6, '')::inet, NULLIF($7, '')::inet, $8
)
RETURNING id, deployment_id, hostname,
          mac::text AS mac,
          host(primary_ip_v6)      AS primary_ip_v6,
          host(provisioning_ip_v6) AS provisioning_ip_v6,
          host(bmc_address)        AS bmc_address,
          role::text AS role,
          status::text AS status,
          last_event, joined_at;

-- name: SetRegionDeploymentKubeconfigSecretRef :one
-- Conditional UPDATE for the kubeconfig callback: only flips the
-- column when the deployment is currently `provisioning` or `joining`
-- (matches Python regiondeploy.py's status gate). The CTE returns
-- the prior status regardless so the handler can distinguish 404
-- (no row), 422 (wrong stage), and success in one round-trip.
WITH cur AS (
    SELECT status::text AS prior_status
    FROM region_deployments
    WHERE id = $1
), upd AS (
    UPDATE region_deployments
    SET kubeconfig_secret_ref = $2, updated_at = NOW()
    WHERE id = $1
      AND status IN ('provisioning'::region_deployment_status,
                     'joining'::region_deployment_status)
    RETURNING site_id
)
SELECT cur.prior_status,
       (SELECT count(*) FROM upd)::bigint AS updated,
       (SELECT site_id FROM upd) AS site_id
FROM cur;

-- name: CreateRegionDeploymentEvent :one
-- Append a deploy event for the SSE backlog + history endpoint.
-- The kubeconfig callback emits two flavors: `info` on Secret-write
-- success, `error` on Secret-write failure (Python emits both via
-- rd_events.emit). NULL payload is valid — the column is JSONB.
INSERT INTO region_deployment_events (deployment_id, stage, level, message, payload)
VALUES ($1, $2, $3::region_deployment_event_level, $4, $5::jsonb)
RETURNING id, stage, level::text AS level, message, payload, created_at;
