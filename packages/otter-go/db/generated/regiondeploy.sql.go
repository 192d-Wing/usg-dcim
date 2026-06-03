// Hand-edited sqlc-style generated bindings for region_deployments
// read-side queries. Drift-checked against db/queries/regiondeploy.sql
// by CI's otter-go sqlc drift check.
package dbq

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ---- Models ----

type RegionDeployment struct {
	ID                  uuid.UUID       `json:"id"`
	SiteID              uuid.UUID       `json:"site_id"`
	Name                string          `json:"name"`
	Status              string          `json:"status"`
	CurrentStage        *string         `json:"current_stage"`
	LastError           *string         `json:"last_error"`
	Config              json.RawMessage `json:"config"`
	KubeconfigSecretRef *string         `json:"kubeconfig_secret_ref"`
	CreatedBy           *uuid.UUID      `json:"created_by"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	StartedAt           *time.Time      `json:"started_at"`
	FinishedAt          *time.Time      `json:"finished_at"`
}

// RegionDeploymentSummary is the list-view row — drops config blob
// and the *_error/_secret_ref noise the table doesn't need.
type RegionDeploymentSummary struct {
	ID           uuid.UUID  `json:"id"`
	SiteID       uuid.UUID  `json:"site_id"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	CurrentStage *string    `json:"current_stage"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

type RegionDeploymentNode struct {
	ID               uuid.UUID  `json:"id"`
	DeploymentID     uuid.UUID  `json:"deployment_id"`
	Hostname         string     `json:"hostname"`
	Mac              string     `json:"mac"`
	PrimaryIpV6      *string    `json:"primary_ip_v6"`
	ProvisioningIpV6 *string    `json:"provisioning_ip_v6"`
	BmcAddress       string     `json:"bmc_address"`
	Role             string     `json:"role"`
	Status           string     `json:"status"`
	LastEvent        *string    `json:"last_event"`
	JoinedAt         *time.Time `json:"joined_at"`
}

type RegionDeploymentService struct {
	ID           uuid.UUID `json:"id"`
	DeploymentID uuid.UUID `json:"deployment_id"`
	Service      string    `json:"service"`
	ChartVersion *string   `json:"chart_version"`
	Status       string    `json:"status"`
	LastError    *string   `json:"last_error"`
}

type RegionDeploymentEvent struct {
	ID        int64           `json:"id"`
	Stage     string          `json:"stage"`
	Level     string          `json:"level"`
	Message   string          `json:"message"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

// ---- Queries ----

const listRegionDeployments = `-- name: ListRegionDeployments :many
SELECT id, site_id, name, status::text AS status, current_stage,
       created_at, started_at, finished_at
FROM region_deployments
WHERE ($3::uuid[] IS NULL OR site_id = ANY($3::uuid[]))
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2
`

type ListRegionDeploymentsParams struct {
	Limit        int32       `json:"limit"`
	Offset       int32       `json:"offset"`
	ScopeSiteIds []uuid.UUID `json:"scope_site_ids"`
}

func (q *Queries) ListRegionDeployments(ctx context.Context, arg ListRegionDeploymentsParams) ([]RegionDeploymentSummary, error) {
	rows, err := q.db.Query(ctx, listRegionDeployments, arg.Limit, arg.Offset, arg.ScopeSiteIds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RegionDeploymentSummary
	for rows.Next() {
		var s RegionDeploymentSummary
		if err := rows.Scan(&s.ID, &s.SiteID, &s.Name, &s.Status, &s.CurrentStage,
			&s.CreatedAt, &s.StartedAt, &s.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

const countRegionDeployments = `-- name: CountRegionDeployments :one
SELECT count(*)::bigint
FROM region_deployments
WHERE ($1::uuid[] IS NULL OR site_id = ANY($1::uuid[]))
`

func (q *Queries) CountRegionDeployments(ctx context.Context, scopeSiteIds []uuid.UUID) (int64, error) {
	row := q.db.QueryRow(ctx, countRegionDeployments, scopeSiteIds)
	var n int64
	err := row.Scan(&n)
	return n, err
}

const getRegionDeployment = `-- name: GetRegionDeployment :one
SELECT id, site_id, name, status::text AS status, current_stage,
       last_error, config, kubeconfig_secret_ref, created_by,
       created_at, updated_at, started_at, finished_at
FROM region_deployments
WHERE id = $1
`

func (q *Queries) GetRegionDeployment(ctx context.Context, id uuid.UUID) (RegionDeployment, error) {
	row := q.db.QueryRow(ctx, getRegionDeployment, id)
	var d RegionDeployment
	err := row.Scan(&d.ID, &d.SiteID, &d.Name, &d.Status, &d.CurrentStage,
		&d.LastError, &d.Config, &d.KubeconfigSecretRef, &d.CreatedBy,
		&d.CreatedAt, &d.UpdatedAt, &d.StartedAt, &d.FinishedAt)
	return d, err
}

const listRegionDeploymentNodes = `-- name: ListRegionDeploymentNodes :many
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
ORDER BY hostname
`

func (q *Queries) ListRegionDeploymentNodes(ctx context.Context, deploymentID uuid.UUID) ([]RegionDeploymentNode, error) {
	rows, err := q.db.Query(ctx, listRegionDeploymentNodes, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RegionDeploymentNode
	for rows.Next() {
		var n RegionDeploymentNode
		if err := rows.Scan(&n.ID, &n.DeploymentID, &n.Hostname,
			&n.Mac, &n.PrimaryIpV6, &n.ProvisioningIpV6, &n.BmcAddress,
			&n.Role, &n.Status, &n.LastEvent, &n.JoinedAt); err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	return items, rows.Err()
}

const listRegionDeploymentServices = `-- name: ListRegionDeploymentServices :many
SELECT id, deployment_id, service::text AS service, chart_version,
       status::text AS status, last_error
FROM region_deployment_services
WHERE deployment_id = $1
ORDER BY service
`

func (q *Queries) ListRegionDeploymentServices(ctx context.Context, deploymentID uuid.UUID) ([]RegionDeploymentService, error) {
	rows, err := q.db.Query(ctx, listRegionDeploymentServices, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RegionDeploymentService
	for rows.Next() {
		var s RegionDeploymentService
		if err := rows.Scan(&s.ID, &s.DeploymentID, &s.Service, &s.ChartVersion,
			&s.Status, &s.LastError); err != nil {
			return nil, err
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

const listRegionDeploymentEvents = `-- name: ListRegionDeploymentEvents :many
SELECT id, stage, level::text AS level, message, payload, created_at
FROM region_deployment_events
WHERE deployment_id = $1
  AND id > $2
ORDER BY id ASC
LIMIT $3
`

type ListRegionDeploymentEventsParams struct {
	DeploymentID uuid.UUID `json:"deployment_id"`
	Since        int64     `json:"since"`
	Limit        int32     `json:"limit"`
}

func (q *Queries) ListRegionDeploymentEvents(ctx context.Context, arg ListRegionDeploymentEventsParams) ([]RegionDeploymentEvent, error) {
	rows, err := q.db.Query(ctx, listRegionDeploymentEvents, arg.DeploymentID, arg.Since, arg.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []RegionDeploymentEvent
	for rows.Next() {
		var e RegionDeploymentEvent
		if err := rows.Scan(&e.ID, &e.Stage, &e.Level, &e.Message,
			&e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, e)
	}
	return items, rows.Err()
}

const abortRegionDeployment = `-- name: AbortRegionDeployment :one
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
FROM cur
`

// AbortRegionDeploymentRow distinguishes the three cases the handler
// must surface: row missing (pgx.ErrNoRows from QueryRow.Scan when cur
// is empty), terminal-state refusal (Updated == 0), and success.
type AbortRegionDeploymentRow struct {
	PriorStatus string `json:"prior_status"`
	Updated     int64  `json:"updated"`
}

func (q *Queries) AbortRegionDeployment(ctx context.Context, id uuid.UUID) (AbortRegionDeploymentRow, error) {
	row := q.db.QueryRow(ctx, abortRegionDeployment, id)
	var r AbortRegionDeploymentRow
	err := row.Scan(&r.PriorStatus, &r.Updated)
	return r, err
}

const createRegionDeployment = `-- name: CreateRegionDeployment :one
INSERT INTO region_deployments (site_id, name, config)
VALUES ($1, $2, $3::jsonb)
RETURNING id, site_id, name, status::text AS status, current_stage,
          last_error, config, kubeconfig_secret_ref, created_by,
          created_at, updated_at, started_at, finished_at
`

type CreateRegionDeploymentParams struct {
	SiteID uuid.UUID       `json:"site_id"`
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

func (q *Queries) CreateRegionDeployment(ctx context.Context, arg CreateRegionDeploymentParams) (RegionDeployment, error) {
	row := q.db.QueryRow(ctx, createRegionDeployment, arg.SiteID, arg.Name, arg.Config)
	var d RegionDeployment
	err := row.Scan(&d.ID, &d.SiteID, &d.Name, &d.Status, &d.CurrentStage,
		&d.LastError, &d.Config, &d.KubeconfigSecretRef, &d.CreatedBy,
		&d.CreatedAt, &d.UpdatedAt, &d.StartedAt, &d.FinishedAt)
	return d, err
}

const createRegionDeploymentNode = `-- name: CreateRegionDeploymentNode :one
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
          last_event, joined_at
`

type CreateRegionDeploymentNodeParams struct {
	DeploymentID      uuid.UUID `json:"deployment_id"`
	Hostname          string    `json:"hostname"`
	Mac               string    `json:"mac"`
	BmcAddress        string    `json:"bmc_address"`
	Role              string    `json:"role"`
	PrimaryIpV6       string    `json:"primary_ip_v6"`
	ProvisioningIpV6  string    `json:"provisioning_ip_v6"`
	BmcCredsSecretRef *string   `json:"bmc_creds_secret_ref"`
}

func (q *Queries) CreateRegionDeploymentNode(ctx context.Context, arg CreateRegionDeploymentNodeParams) (RegionDeploymentNode, error) {
	row := q.db.QueryRow(ctx, createRegionDeploymentNode,
		arg.DeploymentID, arg.Hostname, arg.Mac, arg.BmcAddress, arg.Role,
		arg.PrimaryIpV6, arg.ProvisioningIpV6, arg.BmcCredsSecretRef)
	var n RegionDeploymentNode
	err := row.Scan(&n.ID, &n.DeploymentID, &n.Hostname,
		&n.Mac, &n.PrimaryIpV6, &n.ProvisioningIpV6, &n.BmcAddress,
		&n.Role, &n.Status, &n.LastEvent, &n.JoinedAt)
	return n, err
}
