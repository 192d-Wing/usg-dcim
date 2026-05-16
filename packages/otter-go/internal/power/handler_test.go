package power

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	asset     dbq.AssetKindRow
	assetErr  error
	outlets   []dbq.Outlet
	conns     []dbq.PowerConnection
}

func (f *fakeQ) GetPduAsset(_ context.Context, id uuid.UUID) (dbq.AssetKindRow, error) {
	if f.assetErr != nil {
		return dbq.AssetKindRow{}, f.assetErr
	}
	f.asset.ID = id
	return f.asset, nil
}
func (f *fakeQ) ListOutletsByPdu(_ context.Context, _ uuid.UUID) ([]dbq.Outlet, error) {
	return f.outlets, nil
}
func (f *fakeQ) ListPowerConnectionsByOutletIDs(_ context.Context, _ []uuid.UUID) ([]dbq.PowerConnection, error) {
	return f.conns, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
	return rec
}

func TestListOutlets_PduNotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{assetErr: pgx.ErrNoRows}), "/power/pdus/"+uuid.New().String()+"/outlets")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListOutlets_NotAPdu(t *testing.T) {
	rec := do(t, mount(&fakeQ{asset: dbq.AssetKindRow{Kind: "server"}}), "/power/pdus/"+uuid.New().String()+"/outlets")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 (non-PDU asset)", rec.Code)
	}
}

func TestListOutlets_BadPduID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/power/pdus/not-uuid/outlets")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListOutlets_EmptyReturnsEmptyArray(t *testing.T) {
	rec := do(t, mount(&fakeQ{asset: dbq.AssetKindRow{Kind: "pdu"}}), "/power/pdus/"+uuid.New().String()+"/outlets")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	// Body must be [] not null — finch iterates over it directly.
	if rec.Body.String() == "null\n" || rec.Body.String() == "null" {
		t.Errorf("empty outlets should be [], got %q", rec.Body.String())
	}
}

func TestListOutlets_ConnectionMergedIntoOutlet(t *testing.T) {
	pid := uuid.New()
	oid := uuid.New()
	aid := uuid.New()
	cordColor := "blue"
	f := &fakeQ{
		asset: dbq.AssetKindRow{Kind: "pdu"},
		outlets: []dbq.Outlet{
			{ID: oid, PduAssetID: pid, Position: 1},
			{ID: uuid.New(), PduAssetID: pid, Position: 2}, // unconnected
		},
		conns: []dbq.PowerConnection{
			{OutletID: oid, AssetID: aid, PsuIndex: 0, CordColor: &cordColor},
		},
	}
	rec := do(t, mount(f), "/power/pdus/"+pid.String()+"/outlets")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var got []outletWithConn
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 outlets, got %d", len(got))
	}
	if got[0].Connected == nil || got[0].Connected.AssetID != aid {
		t.Errorf("outlet 0 connected info wrong: %+v", got[0].Connected)
	}
	if got[1].Connected != nil {
		t.Errorf("outlet 1 should be unconnected, got %+v", got[1].Connected)
	}
}
