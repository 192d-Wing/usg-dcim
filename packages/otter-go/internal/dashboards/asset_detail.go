package dashboards

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// AssetDetailQuerier is the dbq slice /assets/{asset_id} needs.
type AssetDetailQuerier interface {
	GetAsset(ctx context.Context, id uuid.UUID) (dbq.Asset, error)
	ListAssetTelemetrySources(ctx context.Context, assetID uuid.UUID) ([]dbq.ListAssetTelemetrySourcesRow, error)
	ListAssetIPAddresses(ctx context.Context, assetID uuid.UUID) ([]dbq.ListAssetIPAddressesRow, error)
	ListRecentAssetAlerts(ctx context.Context, assetID uuid.UUID) ([]dbq.ListRecentAssetAlertsRow, error)
}

// notFoundResponse mirrors Python's `{"error": "not_found"}` body on
// the missing-asset path. Python returns 200; Go matches for wire
// parity even though 404 would be more conventional.
type notFoundResponse struct {
	Error string `json:"error"`
}

// assetDetailResponse is the asset-detail wire shape. Mirrors Python's
// api/dashboards.py asset_detail() return dict — same keys, same
// nesting, same field ordering inside each section.
type assetDetailResponse struct {
	Asset            assetIdentity     `json:"asset"`
	TelemetrySources []telemetrySource `json:"telemetry_sources"`
	IPAddresses      []ipAddressRow    `json:"ip_addresses"`
	RecentAlerts     []recentAlertRow  `json:"recent_alerts"`
}

type assetIdentity struct {
	ID             string  `json:"id"`
	SiteID         string  `json:"site_id"`
	RackID         *string `json:"rack_id"`
	Name           string  `json:"name"`
	Hostname       *string `json:"hostname"`
	Kind           string  `json:"kind"`
	Manufacturer   *string `json:"manufacturer"`
	Model          *string `json:"model"`
	Serial         *string `json:"serial"`
	Firmware       *string `json:"firmware"`
	MgmtIP         *string `json:"mgmt_ip"`
	MgmtProtocol   *string `json:"mgmt_protocol"`
	MgmtPort       *int32  `json:"mgmt_port"`
	RackPositionU  *int32  `json:"rack_position_u"`
	RackUnits      *int32  `json:"rack_units"`
	PortCount      *int32  `json:"port_count"`
	LifecycleState string  `json:"lifecycle_state"`
}

type telemetrySource struct {
	Metric              string   `json:"metric"`
	Unit                *string  `json:"unit"`
	SourceSystem        *string  `json:"source_system"`
	Freshness           string   `json:"freshness"`
	LastValue           *float64 `json:"last_value"`
	LastReadingAt       *string  `json:"last_reading_at"`
	LastSuccessAt       *string  `json:"last_success_at"`
	PollIntervalSeconds int32    `json:"poll_interval_seconds"`
}

type ipAddressRow struct {
	ID                 string  `json:"id"`
	SubnetID           string  `json:"subnet_id"`
	Address            string  `json:"address"`
	Role               string  `json:"role"`
	Status             string  `json:"status"`
	Source             string  `json:"source"`
	DnsName            *string `json:"dns_name"`
	Description        *string `json:"description"`
	DhcpLeaseExpiresAt *string `json:"dhcp_lease_expires_at"`
}

type recentAlertRow struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	State       string `json:"state"`
	Summary     string `json:"summary"`
	FirstSeenAt string `json:"first_seen_at"`
	LastSeenAt  string `json:"last_seen_at"`
}

func (h *Handler) assetDetail(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(AssetDetailQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "asset-detail requires full Querier")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "asset_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "asset_id is not a uuid")
		return
	}

	ctx := r.Context()
	asset, err := q.GetAsset(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Python parity: 200 + {"error": "not_found"} body, not 404.
			httpx.JSON(w, http.StatusOK, notFoundResponse{Error: "not_found"})
			return
		}
		mapErr(w, err)
		return
	}

	sources, err := q.ListAssetTelemetrySources(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	ips, err := q.ListAssetIPAddresses(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	alerts, err := q.ListRecentAssetAlerts(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}

	httpx.JSON(w, http.StatusOK, assembleAssetDetail(asset, sources, ips, alerts))
}

// assembleAssetDetail builds the wire-shape response from the four
// SQL slices. Extracted so the handler body stays linear + readable
// and the SonarLint cognitive-complexity gate is happy.
func assembleAssetDetail(
	asset dbq.Asset,
	sources []dbq.ListAssetTelemetrySourcesRow,
	ips []dbq.ListAssetIPAddressesRow,
	alerts []dbq.ListRecentAssetAlertsRow,
) assetDetailResponse {
	return assetDetailResponse{
		Asset:            buildAssetIdentity(asset),
		TelemetrySources: buildTelemetrySources(sources),
		IPAddresses:      buildIPAddressRows(ips),
		RecentAlerts:     buildRecentAlertRows(alerts),
	}
}

func buildAssetIdentity(a dbq.Asset) assetIdentity {
	ai := assetIdentity{
		ID:             a.ID.String(),
		SiteID:         a.SiteID.String(),
		Name:           a.Name,
		Hostname:       a.Hostname,
		Kind:           a.Kind,
		Manufacturer:   a.Manufacturer,
		Model:          a.Model,
		Serial:         a.Serial,
		Firmware:       a.Firmware,
		MgmtIP:         a.MgmtIP,
		MgmtProtocol:   a.MgmtProtocol,
		MgmtPort:       a.MgmtPort,
		RackPositionU:  a.RackPositionU,
		RackUnits:      a.RackUnits,
		PortCount:      a.PortCount,
		LifecycleState: a.LifecycleState,
	}
	if a.RackID != nil {
		s := a.RackID.String()
		ai.RackID = &s
	}
	return ai
}

func buildTelemetrySources(rows []dbq.ListAssetTelemetrySourcesRow) []telemetrySource {
	out := make([]telemetrySource, 0, len(rows))
	for _, r := range rows {
		out = append(out, telemetrySource{
			Metric:              r.Metric,
			Unit:                r.Unit,
			SourceSystem:        r.SourceSystem,
			Freshness:           r.Freshness,
			LastValue:           r.LastValue,
			LastReadingAt:       isoTime(r.LastReadingAt),
			LastSuccessAt:       isoTime(r.LastSuccessAt),
			PollIntervalSeconds: r.PollIntervalSeconds,
		})
	}
	return out
}

func buildIPAddressRows(rows []dbq.ListAssetIPAddressesRow) []ipAddressRow {
	out := make([]ipAddressRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ipAddressRow{
			ID:                 r.ID.String(),
			SubnetID:           r.SubnetID.String(),
			Address:            r.Address,
			Role:               r.Role,
			Status:             r.Status,
			Source:             r.Source,
			DnsName:            r.DnsName,
			Description:        r.Description,
			DhcpLeaseExpiresAt: isoTime(r.DhcpLeaseExpiresAt),
		})
	}
	return out
}

func buildRecentAlertRows(rows []dbq.ListRecentAssetAlertsRow) []recentAlertRow {
	out := make([]recentAlertRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, recentAlertRow{
			ID:          r.ID.String(),
			Severity:    r.Severity,
			State:       r.State,
			Summary:     r.Summary,
			FirstSeenAt: r.FirstSeenAt.UTC().Format(time.RFC3339Nano),
			LastSeenAt:  r.LastSeenAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

// isoTime mirrors Python's `.isoformat() if v else None`. The timestamp
// format is RFC3339Nano which a JS `new Date(...)` parses correctly,
// matching Python's ISO-8601 µs precision. nil → JSON null.
func isoTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}
