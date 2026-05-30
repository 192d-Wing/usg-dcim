package dashboards

import (
	"context"
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

// fakeFcQ stubs the ForecastQuerier slice. Embeds fakeQ so the other
// dashboard surfaces are still satisfied at the type-assert.
type fakeFcQ struct {
	fakeQ
	site            dbq.Site
	siteErr         error
	rack            dbq.Rack
	rackErr         error
	rackList        []dbq.Rack
	rackListParams  dbq.ListRacksForForecastParams
	siteRacks       []dbq.Rack
	rackAssets      []dbq.Asset
	siteAssets      []dbq.Asset
	assetsByRackIDs []dbq.Asset
	gotKwIDs        []uuid.UUID
	kwRows          []dbq.KwHistoryRow
	kwErr           error
}

func (f *fakeFcQ) GetRack(_ context.Context, _ uuid.UUID) (dbq.Rack, error) {
	return f.rack, f.rackErr
}
func (f *fakeFcQ) GetSite(_ context.Context, _ uuid.UUID) (dbq.Site, error) {
	return f.site, f.siteErr
}
func (f *fakeFcQ) ListAssetsByRackOrdered(_ context.Context, _ uuid.UUID) ([]dbq.Asset, error) {
	return f.rackAssets, nil
}
func (f *fakeFcQ) ListRacksForForecast(_ context.Context, arg dbq.ListRacksForForecastParams) ([]dbq.Rack, error) {
	f.rackListParams = arg
	return f.rackList, nil
}
func (f *fakeFcQ) ListAssetsByRackIDs(_ context.Context, _ []uuid.UUID) ([]dbq.Asset, error) {
	return f.assetsByRackIDs, nil
}
func (f *fakeFcQ) ListAssetsBySite(_ context.Context, _ uuid.UUID) ([]dbq.Asset, error) {
	return f.siteAssets, nil
}
func (f *fakeFcQ) ListRacksBySite(_ context.Context, _ uuid.UUID) ([]dbq.Rack, error) {
	return f.siteRacks, nil
}
func (f *fakeFcQ) ListKwHistorySamples(_ context.Context, arg dbq.ListKwHistorySamplesParams) ([]dbq.KwHistoryRow, error) {
	f.gotKwIDs = arg.AssetIDs
	return f.kwRows, f.kwErr
}

func mountFc(f *fakeFcQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: 600}).Mount(r)
	return r
}

func doFc(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	rec := authtest.ServeRequest(h, authtest.PrincipalWithCaps(capDashboardsRead), "GET", path, nil)
	return rec.Code, rec.Body.Bytes()
}

// ---- /forecast/racks (batch) ----

func TestForecastRacks_EmptyEncodesAsArray(t *testing.T) {
	code, body := doFc(t, mountFc(&fakeFcQ{}), "/dashboards/forecast/racks")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(string(body), `"racks":[]`) {
		t.Errorf("racks should encode as []; got %s", body)
	}
}

func TestForecastRacks_SiteFilterThreaded(t *testing.T) {
	site := uuid.New()
	f := &fakeFcQ{}
	doFc(t, mountFc(f), "/dashboards/forecast/racks?site_id="+site.String())
	if f.rackListParams.SiteID == nil || *f.rackListParams.SiteID != site {
		t.Errorf("site_id not threaded; got %+v", f.rackListParams.SiteID)
	}
	if f.rackListParams.Limit != 200 {
		t.Errorf("default limit = %d, want 200", f.rackListParams.Limit)
	}
}

func TestForecastRacks_LimitClamped(t *testing.T) {
	f := &fakeFcQ{}
	doFc(t, mountFc(f), "/dashboards/forecast/racks?limit=9999")
	if f.rackListParams.Limit != 1000 {
		t.Errorf("limit clamped to 1000; got %d", f.rackListParams.Limit)
	}
}

// Batch strips history. Returns one rack with empty history field.
func TestForecastRacks_StripsHistory(t *testing.T) {
	rkid := uuid.New()
	earlier := time.Now().UTC().Add(-7 * 24 * time.Hour)
	f := &fakeFcQ{
		rackList: []dbq.Rack{{ID: rkid, UHeight: 42, Name: "R", Code: "R"}},
		assetsByRackIDs: []dbq.Asset{
			{ID: uuid.New(), RackID: &rkid, Mount: "rack",
				RackPositionU: intPtrLocal(1), RackUnits: intPtrLocal(1), CreatedAt: earlier},
			{ID: uuid.New(), RackID: &rkid, Mount: "rack",
				RackPositionU: intPtrLocal(2), RackUnits: intPtrLocal(1), CreatedAt: earlier.Add(24 * time.Hour)},
		},
	}
	_, body := doFc(t, mountFc(f), "/dashboards/forecast/racks?limit=10")
	if !strings.Contains(string(body), `"history":null`) {
		// Batch sets History=nil → JSON null (Python uses .pop on the dict)
		t.Errorf("batch should strip history (null); got %s", body)
	}
}

// ---- /forecast/racks/{rack_id} ----

func TestForecastRack_NotFoundReturns200(t *testing.T) {
	f := &fakeFcQ{rackErr: pgx.ErrNoRows}
	code, body := doFc(t, mountFc(f), "/dashboards/forecast/racks/"+uuid.New().String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(string(body), `"error":"not_found"`) {
		t.Errorf("missing not_found body; got %s", body)
	}
}

func TestForecastRack_BadUUIDIs400(t *testing.T) {
	code, _ := doFc(t, mountFc(&fakeFcQ{}), "/dashboards/forecast/racks/not-a-uuid")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

// No PDU assets → kw_forecast: null (Python None parity).
func TestForecastRack_NoPdusYieldsNullKw(t *testing.T) {
	rkid := uuid.New()
	f := &fakeFcQ{
		rack: dbq.Rack{ID: rkid, UHeight: 42, Name: "r", Code: "r"},
	}
	_, body := doFc(t, mountFc(f), "/dashboards/forecast/racks/"+rkid.String())
	if !strings.Contains(string(body), `"kw_forecast":null`) {
		t.Errorf("kw_forecast should be null when no PDUs; got %s", body)
	}
}

// add_units > 0 returns the what-if shape.
func TestForecastRack_WhatIfShape(t *testing.T) {
	rkid := uuid.New()
	f := &fakeFcQ{
		rack: dbq.Rack{ID: rkid, UHeight: 42, Name: "r", Code: "r"},
	}
	_, body := doFc(t, mountFc(f), "/dashboards/forecast/racks/"+rkid.String()+"?add_units=4")
	if !strings.Contains(string(body), `"what_if_add_units":4`) {
		t.Errorf("what_if_add_units missing or wrong; got %s", body)
	}
}

// kw_days param is threaded into the SQL window. Also covers the case
// where ListKwHistorySamples returns rows → kw_forecast is populated.
func TestForecastRack_KwHappyPath(t *testing.T) {
	rkid := uuid.New()
	pduID := uuid.New()
	maxKw := "10"
	now := time.Now().UTC()
	f := &fakeFcQ{
		rack: dbq.Rack{ID: rkid, UHeight: 42, MaxKw: &maxKw, Name: "r", Code: "r"},
		rackAssets: []dbq.Asset{
			{ID: pduID, RackID: &rkid, Kind: "pdu"},
		},
		kwRows: []dbq.KwHistoryRow{
			{Day: now.Add(-4 * 24 * time.Hour), Metric: "pdu.input.kw", AvgV: floatPtrLocal(5.0)},
			{Day: now.Add(-3 * 24 * time.Hour), Metric: "pdu.input.kw", AvgV: floatPtrLocal(6.0)},
			{Day: now.Add(-2 * 24 * time.Hour), Metric: "pdu.input.kw", AvgV: floatPtrLocal(7.0)},
		},
	}
	code, body := doFc(t, mountFc(f), "/dashboards/forecast/racks/"+rkid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if len(f.gotKwIDs) != 1 || f.gotKwIDs[0] != pduID {
		t.Errorf("kw query PDU IDs = %v, want [%s]", f.gotKwIDs, pduID)
	}
	if !strings.Contains(string(body), `"kw_forecast":{`) {
		t.Errorf("kw_forecast missing or null; got %s", body)
	}
}

// SQL error on kw fetch degrades — kw_forecast renders with samples:0
// and band "unknown" rather than 500ing.
func TestForecastRack_KwDegradesOnSqlError(t *testing.T) {
	rkid := uuid.New()
	pduID := uuid.New()
	f := &fakeFcQ{
		rack:       dbq.Rack{ID: rkid, UHeight: 42, Name: "r", Code: "r"},
		rackAssets: []dbq.Asset{{ID: pduID, RackID: &rkid, Kind: "pdu"}},
		kwErr:      errFake,
	}
	code, body := doFc(t, mountFc(f), "/dashboards/forecast/racks/"+rkid.String())
	if code != http.StatusOK {
		t.Fatalf("status should still 200; got %d", code)
	}
	if !strings.Contains(string(body), `"samples":0`) {
		t.Errorf("degraded kw should have samples:0; got %s", body)
	}
}

// ---- /forecast/sites/{site_id} ----

func TestForecastSite_NotFoundReturns200(t *testing.T) {
	f := &fakeFcQ{siteErr: pgx.ErrNoRows}
	code, body := doFc(t, mountFc(f), "/dashboards/forecast/sites/"+uuid.New().String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(string(body), `"error":"not_found"`) {
		t.Errorf("body wrong: %s", body)
	}
}

func TestForecastSite_BadUUIDIs400(t *testing.T) {
	code, _ := doFc(t, mountFc(&fakeFcQ{}), "/dashboards/forecast/sites/not-a-uuid")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

// Site with one rack populates the rollup fields.
func TestForecastSite_HappyPath(t *testing.T) {
	sid, rid := uuid.New(), uuid.New()
	earlier := time.Now().UTC().Add(-30 * 24 * time.Hour)
	f := &fakeFcQ{
		site:      dbq.Site{ID: sid, Name: "S", Code: "S", LifecycleState: "active"},
		siteRacks: []dbq.Rack{{ID: rid, SiteID: sid, UHeight: 4}},
		siteAssets: []dbq.Asset{
			// Fill rack → critical band
			{ID: uuid.New(), RackID: &rid, Mount: "rack",
				RackPositionU: intPtrLocal(1), RackUnits: intPtrLocal(4), CreatedAt: earlier},
		},
	}
	code, body := doFc(t, mountFc(f), "/dashboards/forecast/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	if !strings.Contains(string(body), `"rack_count":1`) {
		t.Errorf("rack_count missing; got %s", body)
	}
	if !strings.Contains(string(body), `"racks_critical":1`) {
		t.Errorf("critical band expected (rack is full); got %s", body)
	}
}

// ---- cap-gate ----

func TestForecast_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &fakeFcQ{}, CollectorStaleSeconds: 600}).Mount(r)
	rec := authtest.ServeRequest(r, authtest.PrincipalWithCaps("inventory:sites:read"),
		"GET", "/dashboards/forecast/racks", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func floatPtrLocal(v float64) *float64 { return &v }
