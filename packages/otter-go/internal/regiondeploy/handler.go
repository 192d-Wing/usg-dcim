// Package regiondeploy holds the otter-go handlers for
// /api/v1/region-deployments. Read-only slice in this PR
// (list/get/events). Lifecycle (POST/PATCH/start/abort) and the SSE
// stream are separate follow-ups — they need the Go scheduler / arq
// equivalent before they can move.
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
	capRead  = "infrastructure:region-deployments:read"
	notFound = "region deployment not found"
)

type Querier interface {
	ListRegionDeployments(ctx context.Context, arg dbq.ListRegionDeploymentsParams) ([]dbq.RegionDeploymentSummary, error)
	CountRegionDeployments(ctx context.Context, scopeSiteIds []uuid.UUID) (int64, error)
	GetRegionDeployment(ctx context.Context, id uuid.UUID) (dbq.RegionDeployment, error)
	ListRegionDeploymentNodes(ctx context.Context, deploymentID uuid.UUID) ([]dbq.RegionDeploymentNode, error)
	ListRegionDeploymentServices(ctx context.Context, deploymentID uuid.UUID) ([]dbq.RegionDeploymentService, error)
	ListRegionDeploymentEvents(ctx context.Context, arg dbq.ListRegionDeploymentEventsParams) ([]dbq.RegionDeploymentEvent, error)
	// Site-scope expansion for the list filter + per-row ABAC.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder // unused on reads; carried for symmetry with other handlers
}

func (h *Handler) Mount(r chi.Router) {
	// Read paths gated by the matching :read capability — see sites/Mount
	// for why ScopedSiteFilter alone doesn't keep cap-less principals out.
	r.With(auth.RequireCapability(capRead)).Get("/region-deployments", h.list)
	r.With(auth.RequireCapability(capRead)).Get("/region-deployments/{id}", h.get)
	r.With(auth.RequireCapability(capRead)).Get("/region-deployments/{id}/events", h.listEvents)
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
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
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
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
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
