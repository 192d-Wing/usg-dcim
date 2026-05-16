// Package dns holds GET handlers for the core DNS resources: zones
// (list+get), records (list). Deferred to follow-up PRs:
// zones/{id}/preview, /keys, /ds-records (depend on the dns rendering
// service — non-trivial); servers, anycast-groups, forwarders,
// catalog-zones, dashboard, blocklists, views, health-checks, etc.
package dns

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListDnsZones(ctx context.Context, arg dbq.ListDnsZonesParams) ([]dbq.DnsZone, error)
	CountDnsZones(ctx context.Context, arg dbq.CountDnsZonesParams) (int64, error)
	GetDnsZone(ctx context.Context, id uuid.UUID) (dbq.DnsZone, error)
	ListDnsRecords(ctx context.Context, arg dbq.ListDnsRecordsParams) ([]dbq.DnsRecord, error)
	CountDnsRecords(ctx context.Context, arg dbq.CountDnsRecordsParams) (int64, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	// Python mounts the dns router with prefix `/dns`.
	r.Route("/dns", func(r chi.Router) {
		r.Get("/zones", h.listZones)
		r.Get("/zones/{id}", h.getZone)
		r.Get("/records", h.listRecords)
	})
}

type zonesPage struct {
	Items  []dbq.DnsZone `json:"items"`
	Total  int64         `json:"total"`
	Limit  int32         `json:"limit"`
	Offset int32         `json:"offset"`
}

func (h *Handler) listZones(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListDnsZonesParams{Limit: limit, Offset: offset, Kind: strPtr(q.Get("kind"))}
	for _, f := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"fabric_id", &params.FabricID},
		{"site_id", &params.SiteID},
	} {
		if v := q.Get(f.key); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				httpx.Error(w, http.StatusBadRequest, f.key+" is not a uuid")
				return
			}
			*f.dst = &id
		}
	}
	items, err := h.Q.ListDnsZones(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsZones(r.Context(), dbq.CountDnsZonesParams{
		FabricID: params.FabricID, SiteID: params.SiteID, Kind: params.Kind,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, zonesPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getZone(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	z, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, z)
}

type recordsPage struct {
	Items  []dbq.DnsRecord `json:"items"`
	Total  int64           `json:"total"`
	Limit  int32           `json:"limit"`
	Offset int32           `json:"offset"`
}

func (h *Handler) listRecords(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListDnsRecordsParams{
		Limit: limit, Offset: offset,
		Type: strPtr(q.Get("type")), Source: strPtr(q.Get("source")),
	}
	if v := q.Get("zone_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "zone_id is not a uuid")
			return
		}
		params.ZoneID = &id
	}
	items, err := h.Q.ListDnsRecords(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDnsRecords(r.Context(), dbq.CountDnsRecordsParams{
		ZoneID: params.ZoneID, Type: params.Type, Source: params.Source,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, recordsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseInt32(s string, def, lo, hi int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	v := int32(n)
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
