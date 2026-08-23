// Package power serves /api/v1/power: outlet listing + connection
// connect/disconnect. Capability codes, audit-event shape, and 4xx
// error messages match Python's api/power.py so the cutover flips
// ingress without observable behavior change on the paths finch
// exercises. The connect response excludes updated_at to match
// Python's PowerConnectionOut; cord_length_m ships as a JSON string
// in both the connect response and listOutlets connected sub-object
// because pgx's NUMERIC scanner emits *string (pre-existing on the
// list path, kept here for consistency).
package power

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	GetPduAsset(ctx context.Context, id uuid.UUID) (dbq.GetPduAssetRow, error)
	ListOutletsByPdu(ctx context.Context, pduID uuid.UUID) ([]dbq.Outlet, error)
	ListPowerConnectionsByOutletIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.PowerConnection, error)

	// Mutations — outlet connect/disconnect. Pre-flight lookups
	// (GetOutletByID, GetAsset, GetPowerConnectionByOutlet) back
	// the 404/409 paths so error messages match Python's verbatim.
	GetOutletByID(ctx context.Context, id uuid.UUID) (dbq.Outlet, error)
	GetAsset(ctx context.Context, id uuid.UUID) (dbq.Asset, error)
	GetPowerConnectionByOutlet(ctx context.Context, outletID uuid.UUID) (dbq.PowerConnection, error)
	CreatePowerConnection(ctx context.Context, arg dbq.CreatePowerConnectionParams) (dbq.PowerConnection, error)
	DeleteOutletConnection(ctx context.Context, outletID uuid.UUID) error
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	// Python mounts the power router with prefix `/power`; preserved
	// here so the URL is `/api/v1/power/pdus/{id}/outlets`.
	r.Route("/power", func(r chi.Router) {
		r.With(auth.RequireCapability("power:outlets:read")).Get("/pdus/{pdu_id}/outlets", h.listOutlets)
		r.With(auth.RequireCapability("power:outlets:create")).Post("/outlets/{outlet_id}/connect", h.connect)
		r.With(auth.RequireCapability("power:outlets:delete")).Delete("/outlets/{outlet_id}/connect", h.disconnect)
	})
}

// outletWithConn matches the Python OutletOut.connected sub-object
// shape so finch parses identically through the cutover.
type connectedInfo struct {
	AssetID     uuid.UUID `json:"asset_id"`
	PsuIndex    int32     `json:"psu_index"`
	CordColor   *string   `json:"cord_color"`
	CordLengthM *string   `json:"cord_length_m"`
}

// int32PtrToStr / floatPtrToStr keep the wire shape stable: the
// Python API (and the hand-maintained dbq before sqlc regeneration)
// serialized max_amps / cord_length_m as decimal strings even though
// the columns are INTEGER / DOUBLE PRECISION.
func int32PtrToStr(v *int32) *string {
	if v == nil {
		return nil
	}
	s := strconv.FormatInt(int64(*v), 10)
	return &s
}

func floatPtrToStr(v *float64) *string {
	if v == nil {
		return nil
	}
	s := strconv.FormatFloat(*v, 'f', -1, 64)
	return &s
}

type outletWithConn struct {
	ID         uuid.UUID      `json:"id"`
	PduAssetID uuid.UUID      `json:"pdu_asset_id"`
	Position   int32          `json:"position"`
	Label      *string        `json:"label"`
	Phase      *string        `json:"phase"`
	MaxAmps    *string        `json:"max_amps"`
	Receptacle *string        `json:"receptacle"`
	Connected  *connectedInfo `json:"connected"`
}

func (h *Handler) listOutlets(w http.ResponseWriter, r *http.Request) {
	pduID, err := uuid.Parse(chi.URLParam(r, "pdu_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "pdu_id is not a uuid")
		return
	}

	asset, err := h.Q.GetPduAsset(r.Context(), pduID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "PDU not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if asset.Kind != "pdu" {
		// Matches Python: asset exists but isn't a PDU → 404 PDU not found.
		httpx.Error(w, http.StatusNotFound, "PDU not found")
		return
	}

	outlets, err := h.Q.ListOutletsByPdu(r.Context(), pduID)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	out := make([]outletWithConn, 0, len(outlets))
	if len(outlets) == 0 {
		httpx.JSON(w, http.StatusOK, out)
		return
	}

	ids := make([]uuid.UUID, len(outlets))
	for i, o := range outlets {
		ids[i] = o.ID
	}
	conns, err := h.Q.ListPowerConnectionsByOutletIDs(r.Context(), ids)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	byOutlet := make(map[uuid.UUID]dbq.PowerConnection, len(conns))
	for _, c := range conns {
		byOutlet[c.OutletID] = c
	}

	for _, o := range outlets {
		ow := outletWithConn{
			ID: o.ID, PduAssetID: o.PduAssetID, Position: o.Position,
			Label: o.Label, Phase: o.Phase, MaxAmps: int32PtrToStr(o.MaxAmps), Receptacle: o.Receptacle,
		}
		if c, ok := byOutlet[o.ID]; ok {
			ow.Connected = &connectedInfo{
				AssetID: c.AssetID, PsuIndex: c.PsuIndex,
				CordColor: c.CordColor, CordLengthM: floatPtrToStr(c.CordLengthM),
			}
		}
		out = append(out, ow)
	}
	httpx.JSON(w, http.StatusOK, out)
}
