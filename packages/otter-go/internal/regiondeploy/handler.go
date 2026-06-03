// Package regiondeploy holds the otter-go handlers for
// /api/v1/region-deployments. Reads (list/get/events) + abort + create
// + kubeconfig callback landed. Preflight/start/SSE follow in later
// PRs — start still needs the arq → Go scheduler equivalent. The
// callback handler intentionally does NOT publish to Redis pubsub —
// that lands with the SSE port; persisting the event row alone is
// enough for the GET /events history endpoint to surface it.
package regiondeploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const (
	capRead   = "infrastructure:region-deployments:read"
	capAbort  = "infrastructure:region-deployments:abort"
	capCreate = "infrastructure:region-deployments:create"
	notFound  = "region deployment not found"
	badIDMsg  = "id is not a uuid"
)

type Querier interface {
	ListRegionDeployments(ctx context.Context, arg dbq.ListRegionDeploymentsParams) ([]dbq.RegionDeploymentSummary, error)
	CountRegionDeployments(ctx context.Context, scopeSiteIds []uuid.UUID) (int64, error)
	GetRegionDeployment(ctx context.Context, id uuid.UUID) (dbq.RegionDeployment, error)
	ListRegionDeploymentNodes(ctx context.Context, deploymentID uuid.UUID) ([]dbq.RegionDeploymentNode, error)
	ListRegionDeploymentServices(ctx context.Context, deploymentID uuid.UUID) ([]dbq.RegionDeploymentService, error)
	ListRegionDeploymentEvents(ctx context.Context, arg dbq.ListRegionDeploymentEventsParams) ([]dbq.RegionDeploymentEvent, error)
	AbortRegionDeployment(ctx context.Context, id uuid.UUID) (dbq.AbortRegionDeploymentRow, error)
	CreateRegionDeployment(ctx context.Context, arg dbq.CreateRegionDeploymentParams) (dbq.RegionDeployment, error)
	CreateRegionDeploymentNode(ctx context.Context, arg dbq.CreateRegionDeploymentNodeParams) (dbq.RegionDeploymentNode, error)
	SetRegionDeploymentKubeconfigSecretRef(ctx context.Context, arg dbq.SetRegionDeploymentKubeconfigSecretRefParams) (dbq.SetRegionDeploymentKubeconfigSecretRefRow, error)
	CreateRegionDeploymentEvent(ctx context.Context, arg dbq.CreateRegionDeploymentEventParams) (dbq.RegionDeploymentEvent, error)
	// Site-scope expansion for the list filter + per-row ABAC.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
}

// TxBeginner is the slim subset of *pgxpool.Pool the create handler
// uses to wrap the deployment + nodes inserts in a single tx — partial
// failure rolls back atomically (no orphan deployment row when a node
// insert violates the uq_rdn_deployment_mac unique constraint). Tests
// pass nil to fall back to autocommit-per-insert via h.Q; production
// wires *pgxpool.Pool. Mirrors the bgp.Handler pattern from PR #205.
type TxBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder // used by abort + create + kubeconfig callback; reads stay no-op
	// Pool is optional. When nil, create runs the deployment + node
	// inserts via h.Q (autocommit-per-insert; existing tests work
	// unchanged). When set, both inserts run inside a single tx so
	// partial failure rolls back atomically.
	Pool TxBeginner
	// CallbackSecret is the server-side HMAC key Python's
	// settings.regiondeploy_callback_secret carries (env
	// DCIM_REGIONDEPLOY_CALLBACK_SECRET). Empty → callback handler
	// returns 503 (fail-closed; no plaintext fallback by design).
	CallbackSecret string
	// K8s is the in-pod Secret writer the callback uses. nil → the
	// handler still records kubeconfig_secret_ref + writes an error
	// event (matches Python's OSError/RuntimeError branch). In prod
	// main.go wires NewInPodK8sClient.
	K8s k8sSecretWriter
}

func (h *Handler) Mount(r chi.Router) {
	// Read paths gated by the matching :read capability — see sites/Mount
	// for why ScopedSiteFilter alone doesn't keep cap-less principals out.
	r.With(auth.RequireCapability(capRead)).Get("/region-deployments", h.list)
	r.With(auth.RequireCapability(capRead)).Get("/region-deployments/{id}", h.get)
	r.With(auth.RequireCapability(capRead)).Get("/region-deployments/{id}/events", h.listEvents)
	r.With(auth.RequireCapability(capAbort)).Post("/region-deployments/{id}/abort", h.abort)
	r.With(auth.RequireCapability(capCreate)).Post("/region-deployments", h.create)
	// Kubeconfig callback is NOT gated by a capability — the in-cluster
	// Workflow action that calls it has no DCIM session/API token. The
	// per-deployment HMAC bearer token is the auth here.
	r.Post("/region-deployments/{id}/kubeconfig/callback", h.kubeconfigCallback)
}

// detailOut mirrors Python's RegionDeploymentOut: the row plus nodes
// + services arrays. Nested types match the Pydantic shapes the wire
// already commits to.
type detailOut struct {
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
	Nodes               []nodeOut       `json:"nodes"`
	Services            []serviceOut    `json:"services"`
}

type nodeOut struct {
	ID               uuid.UUID  `json:"id"`
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

type serviceOut struct {
	ID           uuid.UUID `json:"id"`
	Service      string    `json:"service"`
	ChartVersion *string   `json:"chart_version"`
	Status       string    `json:"status"`
	LastError    *string   `json:"last_error"`
}

type listResponse = httpx.Page[dbq.RegionDeploymentSummary]

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	limit, offset := httpx.PageBounds(r.URL.Query())
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, capRead)
	if err != nil {
		writeMapped(w, err)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.RegionDeploymentSummary](limit, offset))
		return
	}
	items, err := h.Q.ListRegionDeployments(r.Context(), dbq.ListRegionDeploymentsParams{
		Limit: limit, Offset: offset, ScopeSiteIds: scopeSiteIds,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	total, err := h.Q.CountRegionDeployments(r.Context(), scopeSiteIds)
	if err != nil {
		writeMapped(w, err)
		return
	}
	if items == nil {
		items = []dbq.RegionDeploymentSummary{}
	}
	httpx.JSON(w, http.StatusOK, listResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, badIDMsg)
		return
	}
	row, err := h.Q.GetRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, row.SiteID, capRead); serr != nil {
		writeMapped(w, serr)
		return
	}
	nodes, err := h.Q.ListRegionDeploymentNodes(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	services, err := h.Q.ListRegionDeploymentServices(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	out := detailOut{
		ID: row.ID, SiteID: row.SiteID, Name: row.Name, Status: row.Status,
		CurrentStage: row.CurrentStage, LastError: row.LastError,
		Config: defaultConfig(row.Config), KubeconfigSecretRef: row.KubeconfigSecretRef,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		Nodes:    toNodeOuts(nodes),
		Services: toServiceOuts(services),
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, badIDMsg)
		return
	}
	// Scope-check existence + ownership BEFORE returning events — an
	// out-of-scope principal must not learn anything about the
	// deployment via its event stream.
	row, err := h.Q.GetRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, row.SiteID, capRead); serr != nil {
		writeMapped(w, serr)
		return
	}
	q := r.URL.Query()
	since, err := parseInt64Query(q, "since", 0, 0)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "since must be a non-negative integer")
		return
	}
	limit, err := parseInt32Query(q, "limit", 500, 1, 5000)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "limit must be 1-5000")
		return
	}
	items, err := h.Q.ListRegionDeploymentEvents(r.Context(), dbq.ListRegionDeploymentEventsParams{
		DeploymentID: id, Since: since, Limit: limit,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	if items == nil {
		items = []dbq.RegionDeploymentEvent{}
	}
	httpx.JSON(w, http.StatusOK, items)
}

// abort mirrors Python's POST /{id}/abort: refuse the transition if
// the deployment already finished (`ready`) or is already aborted,
// otherwise flip status to `aborted` and return the reloaded detail
// row. Power-off of in-flight nodes via Rufio happens inside the
// orchestrator's abort-handling stage — the API only sets the flag.
func (h *Handler) abort(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, badIDMsg)
		return
	}
	// Scope-check before the conditional UPDATE: an out-of-scope
	// principal must not be able to mutate (or even confirm existence
	// of) a deployment outside their fabric/site grants.
	pre, err := h.Q.GetRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, pre.SiteID, capAbort); serr != nil {
		writeMapped(w, serr)
		return
	}
	res, err := h.Q.AbortRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Race: row was deleted between the scope-check and the
			// conditional update. Surface as 404 to match the read paths.
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	if res.Updated == 0 {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"deployment is "+res.PriorStatus+"; cannot abort")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "region_deployment.abort", TargetType: "region_deployment",
		TargetID: id.String(), SiteID: &pre.SiteID,
	})
	// Reload to return the same shape as GET /{id} — handler.get's
	// pattern, factored out into reloadDetail so abort/start/create
	// later can share it.
	out, err := h.reloadDetail(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// reloadDetail rebuilds the detailOut for a deployment after a
// mutation. Mirrors the read path in handler.get; kept as a method
// so create/start (later PRs) can reuse the same projection.
func (h *Handler) reloadDetail(ctx context.Context, id uuid.UUID) (detailOut, error) {
	row, err := h.Q.GetRegionDeployment(ctx, id)
	if err != nil {
		return detailOut{}, err
	}
	nodes, err := h.Q.ListRegionDeploymentNodes(ctx, id)
	if err != nil {
		return detailOut{}, err
	}
	services, err := h.Q.ListRegionDeploymentServices(ctx, id)
	if err != nil {
		return detailOut{}, err
	}
	return detailOut{
		ID: row.ID, SiteID: row.SiteID, Name: row.Name, Status: row.Status,
		CurrentStage: row.CurrentStage, LastError: row.LastError,
		Config: defaultConfig(row.Config), KubeconfigSecretRef: row.KubeconfigSecretRef,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
		Nodes:    toNodeOuts(nodes),
		Services: toServiceOuts(services),
	}, nil
}

// createReq mirrors Python's RegionDeploymentCreate. Field tags use
// JSON snake_case so the wire is identical. Validation is hand-rolled
// (chi has no Pydantic equivalent) — see validate() below.
type createReq struct {
	SiteID uuid.UUID       `json:"site_id"`
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
	Nodes  []createNodeReq `json:"nodes"`
}

type createNodeReq struct {
	Hostname          string  `json:"hostname"`
	Mac               string  `json:"mac"`
	BmcAddress        string  `json:"bmc_address"`
	Role              string  `json:"role"`
	PrimaryIpV6       *string `json:"primary_ip_v6"`
	ProvisioningIpV6  *string `json:"provisioning_ip_v6"`
	BmcCredsSecretRef *string `json:"bmc_creds_secret_ref"`
}

// validNodeRoles is the trio Python's RegionDeploymentNodeRole accepts
// at create time. Other roles get rejected with 400 here rather than
// surfacing as a pg invalid_enum_value at INSERT time.
var validNodeRoles = map[string]struct{}{
	"control_plane": {}, "worker": {}, "edge": {},
}

const isRequired = "is required"

func nodeErr(i int, field, suffix string) string {
	return "nodes[" + strconv.Itoa(i) + "]." + field + " " + suffix
}

func (req *createReq) validate() (string, bool) {
	if req.SiteID == uuid.Nil {
		return "site_id " + isRequired, false
	}
	if req.Name == "" {
		return "name " + isRequired, false
	}
	// Match Python's Pydantic `config: dict = Field(default_factory=dict)`:
	// omitted → {}, literal JSON null → 422. Without this guard, json.RawMessage
	// would pass the 4 bytes `null` to SQL where 'null'::jsonb is a valid
	// (but wrong) value — COALESCE($3::jsonb, '{}') doesn't catch JSON null,
	// only SQL NULL.
	if len(req.Config) == 0 {
		req.Config = json.RawMessage("{}")
	} else if string(req.Config) == "null" {
		return "config cannot be null (omit or send {})", false
	}
	for i, n := range req.Nodes {
		if n.Hostname == "" {
			return nodeErr(i, "hostname", isRequired), false
		}
		if n.Mac == "" {
			return nodeErr(i, "mac", isRequired), false
		}
		if n.BmcAddress == "" {
			return nodeErr(i, "bmc_address", isRequired), false
		}
		if _, ok := validNodeRoles[n.Role]; !ok {
			return nodeErr(i, "role", "must be one of control_plane|worker|edge"), false
		}
	}
	return "", true
}

// nodeInserter is the slim sub-interface create's per-node insert loop
// uses. Lets the in-tx path swap in dbq.New(tx) without dragging the
// rest of the Querier surface through the type system.
type nodeInserter interface {
	CreateRegionDeployment(ctx context.Context, arg dbq.CreateRegionDeploymentParams) (dbq.RegionDeployment, error)
	CreateRegionDeploymentNode(ctx context.Context, arg dbq.CreateRegionDeploymentNodeParams) (dbq.RegionDeploymentNode, error)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if msg, ok := req.validate(); !ok {
		httpx.Error(w, http.StatusUnprocessableEntity, msg)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, req.SiteID, capCreate); serr != nil {
		writeMapped(w, serr)
		return
	}
	created, nodes, err := h.runCreate(r.Context(), req)
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "region_deployment.create", TargetType: "region_deployment",
		TargetID: created.ID.String(), SiteID: &created.SiteID,
	})
	httpx.JSON(w, http.StatusCreated, detailOut{
		ID: created.ID, SiteID: created.SiteID, Name: created.Name, Status: created.Status,
		CurrentStage: created.CurrentStage, LastError: created.LastError,
		Config:              defaultConfig(created.Config),
		KubeconfigSecretRef: created.KubeconfigSecretRef,
		CreatedBy:           created.CreatedBy, CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt,
		StartedAt: created.StartedAt, FinishedAt: created.FinishedAt,
		Nodes:    toNodeOuts(nodes),
		Services: []serviceOut{},
	})
}

// runCreate inserts the deployment row + each node. When h.Pool is set
// (production) the inserts run inside a single tx so partial failure
// (e.g. nodes[2] hits a uq_rdn_deployment_mac duplicate) rolls back
// the deployment too. When nil (most existing tests) inserts autocommit
// one-by-one — sufficient for the unit-test fakeQ which never fails.
func (h *Handler) runCreate(ctx context.Context, req createReq) (dbq.RegionDeployment, []dbq.RegionDeploymentNode, error) {
	run := func(q nodeInserter) (dbq.RegionDeployment, []dbq.RegionDeploymentNode, error) {
		created, err := q.CreateRegionDeployment(ctx, dbq.CreateRegionDeploymentParams{
			SiteID: req.SiteID, Name: req.Name, Config: req.Config,
		})
		if err != nil {
			return dbq.RegionDeployment{}, nil, err
		}
		nodes := make([]dbq.RegionDeploymentNode, 0, len(req.Nodes))
		for _, n := range req.Nodes {
			out, err := q.CreateRegionDeploymentNode(ctx, dbq.CreateRegionDeploymentNodeParams{
				DeploymentID: created.ID, Hostname: n.Hostname, Mac: n.Mac,
				BmcAddress: n.BmcAddress, Role: n.Role,
				PrimaryIpV6:       deref(n.PrimaryIpV6),
				ProvisioningIpV6:  deref(n.ProvisioningIpV6),
				BmcCredsSecretRef: n.BmcCredsSecretRef,
			})
			if err != nil {
				return dbq.RegionDeployment{}, nil, err
			}
			nodes = append(nodes, out)
		}
		return created, nodes, nil
	}
	if h.Pool == nil {
		return run(h.Q)
	}
	tx, err := h.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return dbq.RegionDeployment{}, nil, err
	}
	// Rollback on a fresh background context (5s) so cleanup runs
	// even when the request ctx is already cancelled by client
	// disconnect — same pattern as bgp.rotateInsert.
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()
	created, nodes, err := run(dbq.New(tx))
	if err != nil {
		return dbq.RegionDeployment{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return dbq.RegionDeployment{}, nil, err
	}
	return created, nodes, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func toNodeOuts(in []dbq.RegionDeploymentNode) []nodeOut {
	out := make([]nodeOut, len(in))
	for i, n := range in {
		out[i] = nodeOut{
			ID: n.ID, Hostname: n.Hostname, Mac: n.Mac,
			PrimaryIpV6: n.PrimaryIpV6, ProvisioningIpV6: n.ProvisioningIpV6,
			BmcAddress: n.BmcAddress, Role: n.Role, Status: n.Status,
			LastEvent: n.LastEvent, JoinedAt: n.JoinedAt,
		}
	}
	return out
}

func toServiceOuts(in []dbq.RegionDeploymentService) []serviceOut {
	out := make([]serviceOut, len(in))
	for i, s := range in {
		out[i] = serviceOut{
			ID: s.ID, Service: s.Service, ChartVersion: s.ChartVersion,
			Status: s.Status, LastError: s.LastError,
		}
	}
	return out
}

// defaultConfig returns the input verbatim if non-empty, else `{}` so
// the JSON encoder serializes an object literal (matches Pydantic's
// `default_factory=dict`).
func defaultConfig(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	return raw
}

func parseInt64Query(q map[string][]string, key string, def, minV int64) (int64, error) {
	v, ok := q[key]
	if !ok || len(v) == 0 || v[0] == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil {
		return 0, err
	}
	if n < minV {
		return 0, errors.New("below min")
	}
	return n, nil
}

func parseInt32Query(q map[string][]string, key string, def, minV, maxV int32) (int32, error) {
	v, ok := q[key]
	if !ok || len(v) == 0 || v[0] == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v[0], 10, 32)
	if err != nil {
		return 0, err
	}
	if int32(n) < minV || int32(n) > maxV {
		return 0, errors.New("out of range")
	}
	return int32(n), nil
}

func writeMapped(w http.ResponseWriter, err error) {
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}
