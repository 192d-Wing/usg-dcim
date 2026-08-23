package dashboards

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/capacity"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
	"github.com/usg-dcim/packages/otter-go/internal/powerchain"
)

// RackDetailQuerier is the dbq slice /racks/{rack_id} needs.
type RackDetailQuerier interface {
	GetRack(ctx context.Context, id uuid.UUID) (dbq.Rack, error)
	ListAssetsByRackOrdered(ctx context.Context, rackID uuid.UUID) ([]dbq.Asset, error)
	ListOpenAlertsByAssetIDs(ctx context.Context, assetIDs []uuid.UUID) ([]dbq.ListOpenAlertsByAssetIDsRow, error)
	ListAssetFreshnessByIDs(ctx context.Context, assetIDs []uuid.UUID) ([]dbq.ListAssetFreshnessByIDsRow, error)
	capacity.Querier
	powerchain.Querier
}

// rackDetailResponse mirrors Python's api/dashboards.py::rack_detail
// return dict byte-for-byte.
type rackDetailResponse struct {
	Rack       rackEntity            `json:"rack"`
	Capacity   capacity.RackCapacity `json:"capacity"`
	PowerChain powerchain.Result     `json:"power_chain"`
	Assets     []rackAssetRow        `json:"assets"`
}

type rackEntity struct {
	ID      string   `json:"id"`
	SiteID  string   `json:"site_id"`
	RowID   string   `json:"row_id"`
	Name    string   `json:"name"`
	Code    string   `json:"code"`
	UHeight int32    `json:"u_height"`
	MaxKw   *float64 `json:"max_kw"`
	Serial  *string  `json:"serial"`
}

type rackAssetRow struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Hostname       *string          `json:"hostname"`
	Kind           string           `json:"kind"`
	Manufacturer   *string          `json:"manufacturer"`
	Model          *string          `json:"model"`
	Serial         *string          `json:"serial"`
	RackPositionU  *int32           `json:"rack_position_u"`
	RackUnits      int32            `json:"rack_units"`
	Face           string           `json:"face"`
	Mount          string           `json:"mount"`
	PduSide        *string          `json:"pdu_side"`
	PsuCount       *int32           `json:"psu_count"`
	PortCount      *int32           `json:"port_count"`
	LifecycleState string           `json:"lifecycle_state"`
	OpenAlerts     int64            `json:"open_alerts"`
	Freshness      map[string]int64 `json:"freshness"`
	Redundancy     *string          `json:"redundancy"`
}

func (h *Handler) rackDetail(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(RackDetailQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "rack-detail requires full Querier")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "rack_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "rack_id is not a uuid")
		return
	}
	ctx := r.Context()

	rack, err := q.GetRack(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.JSON(w, http.StatusOK, notFoundResponse{Error: "not_found"})
			return
		}
		mapErr(w, err)
		return
	}

	assets, err := q.ListAssetsByRackOrdered(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	assetIDs := assetIDsOf(assets)

	openAlerts, freshness, err := loadAssetKpis(ctx, q, assetIDs)
	if err != nil {
		mapErr(w, err)
		return
	}

	cap, err := capacity.ComputeRackCapacity(ctx, q, rack, assets)
	if err != nil {
		mapErr(w, err)
		return
	}
	pc, err := powerchain.Compute(ctx, q, assets)
	if err != nil {
		mapErr(w, err)
		return
	}

	httpx.JSON(w, http.StatusOK, rackDetailResponse{
		Rack:       buildRackEntity(rack),
		Capacity:   cap,
		PowerChain: pc,
		Assets:     buildRackAssetRows(assets, openAlerts, freshness, pc),
	})
}

func assetIDsOf(assets []dbq.Asset) []uuid.UUID {
	out := make([]uuid.UUID, len(assets))
	for i, a := range assets {
		out[i] = a.ID
	}
	return out
}

// loadAssetKpis runs the two grouped lookups (open-alerts + telemetry
// freshness) and pivots them into per-asset maps. Short-circuits on
// empty input so we don't issue ANY('{}') queries.
func loadAssetKpis(ctx context.Context, q RackDetailQuerier, ids []uuid.UUID) (
	map[uuid.UUID]int64, map[uuid.UUID]map[string]int64, error,
) {
	openAlerts := map[uuid.UUID]int64{}
	freshness := map[uuid.UUID]map[string]int64{}
	if len(ids) == 0 {
		return openAlerts, freshness, nil
	}
	alerts, err := q.ListOpenAlertsByAssetIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range alerts {
		// asset_id can't be NULL here — the query filters on
		// asset_id = ANY($1) — but the column is nullable so the
		// generated field is a pointer.
		if r.AssetID != nil {
			openAlerts[*r.AssetID] = r.N
		}
	}
	freshRows, err := q.ListAssetFreshnessByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range freshRows {
		m, ok := freshness[r.AssetID]
		if !ok {
			m = map[string]int64{}
			freshness[r.AssetID] = m
		}
		m[r.Freshness] = r.N
	}
	return openAlerts, freshness, nil
}

func buildRackEntity(r dbq.Rack) rackEntity {
	return rackEntity{
		ID:      r.ID.String(),
		SiteID:  r.SiteID.String(),
		RowID:   r.RowID.String(),
		Name:    r.Name,
		Code:    r.Code,
		UHeight: r.UHeight,
		MaxKw:   parseFloat(r.MaxKw),
		Serial:  r.Serial,
	}
}

func buildRackAssetRows(
	assets []dbq.Asset,
	openAlerts map[uuid.UUID]int64,
	freshness map[uuid.UUID]map[string]int64,
	pc powerchain.Result,
) []rackAssetRow {
	out := make([]rackAssetRow, 0, len(assets))
	for _, a := range assets {
		out = append(out, buildRackAssetRow(a, openAlerts, freshness, pc))
	}
	return out
}

func buildRackAssetRow(
	a dbq.Asset,
	openAlerts map[uuid.UUID]int64,
	freshness map[uuid.UUID]map[string]int64,
	pc powerchain.Result,
) rackAssetRow {
	rackUnits := int32(1)
	if a.RackUnits != nil && *a.RackUnits > 0 {
		rackUnits = *a.RackUnits
	}
	fresh, ok := freshness[a.ID]
	if !ok {
		fresh = map[string]int64{}
	}
	row := rackAssetRow{
		ID:             a.ID.String(),
		Name:           a.Name,
		Hostname:       a.Hostname,
		Kind:           a.Kind,
		Manufacturer:   a.Manufacturer,
		Model:          a.Model,
		Serial:         a.Serial,
		RackPositionU:  a.RackPositionU,
		RackUnits:      rackUnits,
		Face:           a.Face,
		Mount:          a.Mount,
		PduSide:        a.PduSide,
		PsuCount:       a.PsuCount,
		PortCount:      a.PortCount,
		LifecycleState: a.LifecycleState,
		OpenAlerts:     openAlerts[a.ID],
		Freshness:      fresh,
	}
	if entry, ok := pc.PerAsset[a.ID.String()]; ok {
		v := entry.Redundancy
		row.Redundancy = &v
	}
	return row
}
