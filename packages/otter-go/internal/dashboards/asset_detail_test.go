package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeAdQ stubs the AssetDetailQuerier slice. Embeds fakeQ so the
// enterprise + free-space + sites-at-risk methods are still satisfied
// for the type-assert in the handler.
type fakeAdQ struct {
	fakeQ
	asset      dbq.Asset
	assetErr   error
	sources    []dbq.AssetTelemetrySourceRow
	ips        []dbq.AssetIPAddressRow
	alerts     []dbq.RecentAssetAlertRow
	gotAssetID uuid.UUID
	sourcesErr error
}

func (f *fakeAdQ) GetAsset(_ context.Context, id uuid.UUID) (dbq.Asset, error) {
	f.gotAssetID = id
	return f.asset, f.assetErr
}
func (f *fakeAdQ) ListAssetTelemetrySources(_ context.Context, _ uuid.UUID) ([]dbq.AssetTelemetrySourceRow, error) {
	return f.sources, f.sourcesErr
}
func (f *fakeAdQ) ListAssetIPAddresses(_ context.Context, _ uuid.UUID) ([]dbq.AssetIPAddressRow, error) {
	return f.ips, nil
}
func (f *fakeAdQ) ListRecentAssetAlerts(_ context.Context, _ uuid.UUID) ([]dbq.RecentAssetAlertRow, error) {
	return f.alerts, nil
}

func mountAd(f *fakeAdQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: 600}).Mount(r)
	return r
}

func doAd(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	rec := authtest.ServeRequest(h, authtest.PrincipalWithCaps(capDashboardsRead), "GET", path, nil)
	return rec.Code, rec.Body.Bytes()
}

func TestAssetDetail_HappyPath(t *testing.T) {
	aid, sid, rid := uuid.New(), uuid.New(), uuid.New()
	subID := uuid.New()
	alertID := uuid.New()
	ipID := uuid.New()
	hostname := "switch-1"
	mgmtIP := "10.0.0.5"
	mgmtPort := int32(443)
	t1 := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	lv := 1.5
	f := &fakeAdQ{
		asset: dbq.Asset{
			ID: aid, SiteID: sid, RackID: &rid,
			Name: "switch-1", Hostname: &hostname,
			Kind: "switch", Manufacturer: nil,
			MgmtIP: &mgmtIP, MgmtPort: &mgmtPort,
			LifecycleState: "active",
		},
		sources: []dbq.AssetTelemetrySourceRow{
			{Metric: "cpu.load", Freshness: "current", LastValue: &lv,
				LastReadingAt: &t1, LastSuccessAt: &t1, PollIntervalSeconds: 60},
		},
		ips: []dbq.AssetIPAddressRow{
			{ID: ipID, SubnetID: subID, Address: "10.0.0.5",
				Role: "host", Status: "active", Source: "manual"},
		},
		alerts: []dbq.RecentAssetAlertRow{
			{ID: alertID, Severity: "major", State: "firing",
				Summary: "interface down", FirstSeenAt: t1, LastSeenAt: t1},
		},
	}
	code, body := doAd(t, mountAd(f), "/dashboards/assets/"+aid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if f.gotAssetID != aid {
		t.Errorf("GetAsset arg = %s, want %s", f.gotAssetID, aid)
	}
	var resp assetDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Asset.ID != aid.String() {
		t.Errorf("asset.id = %q, want %s", resp.Asset.ID, aid)
	}
	if resp.Asset.RackID == nil || *resp.Asset.RackID != rid.String() {
		t.Errorf("asset.rack_id = %v, want %s", resp.Asset.RackID, rid)
	}
	if resp.Asset.Kind != "switch" {
		t.Errorf("asset.kind = %q, want switch", resp.Asset.Kind)
	}
	if len(resp.TelemetrySources) != 1 || resp.TelemetrySources[0].Metric != "cpu.load" {
		t.Errorf("telemetry_sources wrong: %+v", resp.TelemetrySources)
	}
	if resp.TelemetrySources[0].LastValue == nil || *resp.TelemetrySources[0].LastValue != 1.5 {
		t.Errorf("last_value: %+v", resp.TelemetrySources[0].LastValue)
	}
	if len(resp.IPAddresses) != 1 || resp.IPAddresses[0].Address != "10.0.0.5" {
		t.Errorf("ip_addresses wrong: %+v", resp.IPAddresses)
	}
	if len(resp.RecentAlerts) != 1 || resp.RecentAlerts[0].Severity != "major" {
		t.Errorf("recent_alerts wrong: %+v", resp.RecentAlerts)
	}
}

// Missing asset → 200 + {"error": "not_found"} (Python parity, not 404).
func TestAssetDetail_NotFoundReturns200WithErrorBody(t *testing.T) {
	f := &fakeAdQ{assetErr: pgx.ErrNoRows}
	code, body := doAd(t, mountAd(f), "/dashboards/assets/"+uuid.New().String())
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (Python parity)", code)
	}
	var resp notFoundResponse
	_ = json.Unmarshal(body, &resp)
	if resp.Error != "not_found" {
		t.Errorf("error = %q, want not_found", resp.Error)
	}
}

// Bad UUID in path → 400.
func TestAssetDetail_BadUUIDIs400(t *testing.T) {
	code, _ := doAd(t, mountAd(&fakeAdQ{}), "/dashboards/assets/not-a-uuid")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

// Empty joined surfaces encode as [] not null.
func TestAssetDetail_EmptyJoinedSurfacesAreArrays(t *testing.T) {
	aid := uuid.New()
	f := &fakeAdQ{asset: dbq.Asset{ID: aid, SiteID: uuid.New(), Kind: "server", LifecycleState: "active"}}
	code, body := doAd(t, mountAd(f), "/dashboards/assets/"+aid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Spot-check JSON encoding for the three buckets.
	for _, sub := range []string{`"telemetry_sources":[]`, `"ip_addresses":[]`, `"recent_alerts":[]`} {
		if !strings.Contains(string(body), sub) {
			t.Errorf("expected %s in body; got %s", sub, body)
		}
	}
}

// rack_id field encodes as null when the asset isn't racked.
func TestAssetDetail_NilRackIDEncodesAsNull(t *testing.T) {
	aid := uuid.New()
	f := &fakeAdQ{asset: dbq.Asset{ID: aid, SiteID: uuid.New(), RackID: nil, Kind: "rack-pdu", LifecycleState: "active"}}
	_, body := doAd(t, mountAd(f), "/dashboards/assets/"+aid.String())
	if !strings.Contains(string(body), `"rack_id":null`) {
		t.Errorf("rack_id should encode as null; got %s", body)
	}
}

func TestAssetDetail_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &fakeAdQ{}, CollectorStaleSeconds: 600}).Mount(r)
	rec := authtest.ServeRequest(r, authtest.PrincipalWithCaps("inventory:sites:read"),
		"GET", "/dashboards/assets/"+uuid.New().String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// DB error on a joined surface 500s and doesn't leak partial response.
func TestAssetDetail_JoinedSurfaceErrorIs500(t *testing.T) {
	aid := uuid.New()
	f := &fakeAdQ{
		asset:      dbq.Asset{ID: aid, SiteID: uuid.New(), Kind: "server", LifecycleState: "active"},
		sourcesErr: errFake,
	}
	code, _ := doAd(t, mountAd(f), "/dashboards/assets/"+aid.String())
	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
}
