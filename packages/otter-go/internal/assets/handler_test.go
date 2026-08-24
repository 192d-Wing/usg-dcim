package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	last     dbq.ListAssetsParams
	seeded   []dbq.SeedPduOutletsParams
	seedFail bool
}

func (f *fakeQ) SeedPduOutlets(_ context.Context, a dbq.SeedPduOutletsParams) (int64, error) {
	if f.seedFail {
		return 0, pgx.ErrTxClosed
	}
	f.seeded = append(f.seeded, a)
	return int64(a.OutletCount), nil
}

func (f *fakeQ) ListAssets(_ context.Context, a dbq.ListAssetsParams) ([]dbq.Asset, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountAssets(_ context.Context, _ dbq.CountAssetsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetAsset(_ context.Context, _ uuid.UUID) (dbq.Asset, error) {
	return dbq.Asset{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateAsset(_ context.Context, a dbq.CreateAssetParams) (dbq.Asset, error) {
	return dbq.Asset{ID: uuid.New(), SiteID: a.SiteID, Name: a.Name, Kind: a.Kind,
		Face: a.Face, Mount: a.Mount, LifecycleState: a.LifecycleState}, nil
}
func (f *fakeQ) UpdateAsset(_ context.Context, a dbq.UpdateAssetParams) (dbq.Asset, error) {
	return dbq.Asset{ID: a.ID}, nil
}
func (f *fakeQ) SetAssetDecommissioned(_ context.Context, id uuid.UUID) (dbq.Asset, error) {
	return dbq.Asset{ID: id, LifecycleState: "decommissioned"}, nil
}
func (f *fakeQ) FindAssetByManufacturerSerial(_ context.Context, _ dbq.FindAssetByManufacturerSerialParams) (dbq.Asset, error) {
	return dbq.Asset{}, pgx.ErrNoRows
}
func (f *fakeQ) CountConsumerPowerDrops(_ context.Context, _ uuid.UUID) (int64, error) { return 0, nil }
func (f *fakeQ) CountPduPowerDrops(_ context.Context, _ uuid.UUID) (int64, error)      { return 0, nil }
func (f *fakeQ) ListDownstreamAssetNames(_ context.Context, _ uuid.UUID) ([]string, error) {
	return nil, nil
}
func (f *fakeQ) DeleteConsumerPowerConnections(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeQ) DeletePduPowerConnections(_ context.Context, _ uuid.UUID) error      { return nil }
func (f *fakeQ) GetRack(_ context.Context, id uuid.UUID) (dbq.Rack, error) {
	return dbq.Rack{ID: id, UHeight: 42}, nil
}
func (f *fakeQ) ListRackAssetsForPlacement(_ context.Context, _ dbq.ListRackAssetsForPlacementParams) ([]dbq.ListRackAssetsForPlacementRow, error) {
	return nil, nil
}
func (f *fakeQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (f *fakeQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// PR 62 — scope expansion. Default returns nil (no expansion).
func (f *fakeQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	// Wildcard principal — inventory:assets:read gate added in the
	// inventory cutover blocks otherwise-anonymous test traffic.
	req := authtest.Request("GET", p, authtest.PrincipalWithCaps("*"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListAssets_AllFiltersThreaded(t *testing.T) {
	sid, rid := uuid.New(), uuid.New()
	f := &fakeQ{}
	url := "/assets?" +
		"site_id=" + sid.String() +
		"&rack_id=" + rid.String() +
		"&kind=server&lifecycle_state=active&serial=ABC123&hostname=host1&limit=10"
	rec := do(t, mount(f), url)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.SiteID == nil || *f.last.SiteID != sid {
		t.Errorf("site_id")
	}
	if f.last.RackID == nil || *f.last.RackID != rid {
		t.Errorf("rack_id")
	}
	for _, c := range []struct{ got *string; want, name string }{
		{f.last.Kind, "server", "kind"},
		{f.last.LifecycleState, "active", "lifecycle_state"},
		{f.last.Serial, "ABC123", "serial"},
		{f.last.Hostname, "host1", "hostname"},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s not threaded: %v", c.name, c.got)
		}
	}
	if f.last.Limit != 10 {
		t.Errorf("limit: %d", f.last.Limit)
	}
}

func TestListAssets_BadUUIDs(t *testing.T) {
	for _, p := range []string{"/assets?site_id=x", "/assets?rack_id=x"} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestGetAsset_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/assets/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGetAsset_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/assets/x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

// doCreate POSTs an asset create with a global-scope principal and
// returns the recorder.
func doCreate(t *testing.T, f *fakeQ, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := authtest.Request("POST", "/assets", authtest.PrincipalWithCaps("*"), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mount(f).ServeHTTP(rec, req)
	return rec
}

func TestCreateAsset_PduSeedsOutlets(t *testing.T) {
	f := &fakeQ{}
	rec := doCreate(t, f, map[string]any{
		"site_id": uuid.New(), "name": "pdu-1", "kind": "pdu",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.seeded) != 1 {
		t.Fatalf("SeedPduOutlets calls = %d, want 1", len(f.seeded))
	}
	if f.seeded[0].OutletCount != 24 {
		t.Errorf("outlet count = %d, want 24", f.seeded[0].OutletCount)
	}
	if f.seeded[0].PduAssetID == uuid.Nil {
		t.Error("seed called with nil pdu_asset_id")
	}
}

func TestCreateAsset_NonPduDoesNotSeed(t *testing.T) {
	f := &fakeQ{}
	rec := doCreate(t, f, map[string]any{
		"site_id": uuid.New(), "name": "srv-1", "kind": "server",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.seeded) != 0 {
		t.Errorf("SeedPduOutlets calls = %d, want 0 for non-PDU", len(f.seeded))
	}
}

func TestCreateAsset_PduSeedFailureSurfaces(t *testing.T) {
	f := &fakeQ{seedFail: true}
	rec := doCreate(t, f, map[string]any{
		"site_id": uuid.New(), "name": "pdu-2", "kind": "pdu",
	})
	if rec.Code == http.StatusCreated {
		t.Fatalf("expected error status when outlet seeding fails, got 201")
	}
}
