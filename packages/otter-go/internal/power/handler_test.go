package power

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type fakeQ struct {
	asset       dbq.GetPduAssetRow
	assetErr    error
	outlets     []dbq.Outlet
	conns       []dbq.PowerConnection
	outletErr   error
	assetGetErr error
	connByO     *dbq.PowerConnection
}

func (f *fakeQ) GetPduAsset(_ context.Context, id uuid.UUID) (dbq.GetPduAssetRow, error) {
	if f.assetErr != nil {
		return dbq.GetPduAssetRow{}, f.assetErr
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
func (f *fakeQ) GetOutletByID(_ context.Context, id uuid.UUID) (dbq.Outlet, error) {
	if f.outletErr != nil {
		return dbq.Outlet{}, f.outletErr
	}
	return dbq.Outlet{ID: id}, nil
}
func (f *fakeQ) GetAsset(_ context.Context, id uuid.UUID) (dbq.Asset, error) {
	if f.assetGetErr != nil {
		return dbq.Asset{}, f.assetGetErr
	}
	return dbq.Asset{ID: id}, nil
}
func (f *fakeQ) GetPowerConnectionByOutlet(_ context.Context, outletID uuid.UUID) (dbq.PowerConnection, error) {
	if f.connByO != nil {
		return *f.connByO, nil
	}
	return dbq.PowerConnection{OutletID: outletID}, pgx.ErrNoRows
}
func (f *fakeQ) CreatePowerConnection(_ context.Context, a dbq.CreatePowerConnectionParams) (dbq.PowerConnection, error) {
	return dbq.PowerConnection{ID: uuid.New(), OutletID: a.OutletID, AssetID: a.AssetID, PsuIndex: a.PsuIndex}, nil
}
func (f *fakeQ) DeleteOutletConnection(_ context.Context, _ uuid.UUID) error { return nil }

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", p, nil)
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"*"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// TestRouteCapabilityCodes locks the catalog-advertised cap names
// onto each route. Refactors that swap to non-catalog names (e.g.
// the old `inventory:power-connections:*`) regress finch's UI
// gating (power-chain-panel.tsx:86 checks `power:outlets:create`)
// and silently break role assignments that grant `power:outlets:*`.
func TestRouteCapabilityCodes(t *testing.T) {
	cases := []struct{ method, path, requiredCap string }{
		{"GET", "/power/pdus/" + uuid.New().String() + "/outlets", "power:outlets:read"},
		{"POST", "/power/outlets/" + uuid.New().String() + "/connect", "power:outlets:create"},
		{"DELETE", "/power/outlets/" + uuid.New().String() + "/connect", "power:outlets:delete"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.requiredCap, func(t *testing.T) {
			// principal lacks the required cap → 403
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"unrelated:cap"}})
			mount(&fakeQ{asset: dbq.GetPduAssetRow{Kind: "pdu"}}).ServeHTTP(rec, req.WithContext(ctx))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s without %s: got %d (want 403)", tc.method, tc.path, tc.requiredCap, rec.Code)
			}
			// same request with ONLY that cap → not 403 (route reached;
			// downstream may 4xx but cap gate passes)
			rec = httptest.NewRecorder()
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			ctx = auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{tc.requiredCap}})
			mount(&fakeQ{asset: dbq.GetPduAssetRow{Kind: "pdu"}}).ServeHTTP(rec, req.WithContext(ctx))
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s %s with %s: got 403 (cap gate should pass)", tc.method, tc.path, tc.requiredCap)
			}
		})
	}
}

func TestListOutlets_PduNotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{assetErr: pgx.ErrNoRows}), "/power/pdus/"+uuid.New().String()+"/outlets")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListOutlets_NotAPdu(t *testing.T) {
	rec := do(t, mount(&fakeQ{asset: dbq.GetPduAssetRow{Kind: "server"}}), "/power/pdus/"+uuid.New().String()+"/outlets")
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
	rec := do(t, mount(&fakeQ{asset: dbq.GetPduAssetRow{Kind: "pdu"}}), "/power/pdus/"+uuid.New().String()+"/outlets")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	// Body must be [] not null — finch iterates over it directly.
	if rec.Body.String() == "null\n" || rec.Body.String() == "null" {
		t.Errorf("empty outlets should be [], got %q", rec.Body.String())
	}
}

func mutate(t *testing.T, f *fakeQ, method, p, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, p, strings.NewReader(body))
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"*"}})
	mount(f).ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestConnect_OutletNotFound(t *testing.T) {
	f := &fakeQ{outletErr: pgx.ErrNoRows}
	rec := mutate(t, f, "POST", "/power/outlets/"+uuid.New().String()+"/connect",
		`{"asset_id":"`+uuid.New().String()+`"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "outlet not found") {
		t.Fatalf("got %d %s, want 404 outlet not found", rec.Code, rec.Body.String())
	}
}

func TestConnect_AssetNotFound(t *testing.T) {
	f := &fakeQ{assetGetErr: pgx.ErrNoRows}
	rec := mutate(t, f, "POST", "/power/outlets/"+uuid.New().String()+"/connect",
		`{"asset_id":"`+uuid.New().String()+`"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "asset not found") {
		t.Fatalf("got %d %s, want 404 asset not found", rec.Code, rec.Body.String())
	}
}

func TestConnect_AlreadyConnectedFriendly(t *testing.T) {
	existing := &dbq.PowerConnection{OutletID: uuid.New(), AssetID: uuid.New()}
	f := &fakeQ{connByO: existing}
	rec := mutate(t, f, "POST", "/power/outlets/"+uuid.New().String()+"/connect",
		`{"asset_id":"`+uuid.New().String()+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "disconnect it first") {
		t.Fatalf("got %d %s, want 409 friendly message", rec.Code, rec.Body.String())
	}
}

func TestConnect_BadAssetID(t *testing.T) {
	rec := mutate(t, &fakeQ{}, "POST", "/power/outlets/"+uuid.New().String()+"/connect", `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "asset_id required") {
		t.Fatalf("got %d %s, want 400 missing asset_id", rec.Code, rec.Body.String())
	}
}

// Regression: a body that fails decoding (here cord_length_m as a JSON
// string where the struct wants a number) used to share the missing-
// field branch and 400 with the misleading "asset_id required".
func TestConnect_WireTypeMismatch_Honest400(t *testing.T) {
	rec := mutate(t, &fakeQ{}, "POST", "/power/outlets/"+uuid.New().String()+"/connect",
		`{"asset_id":"`+uuid.New().String()+`","cord_length_m":"3"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s, want 400", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid request body") {
		t.Errorf("want decode-error message, got %q", body)
	}
	if strings.Contains(body, "asset_id required") {
		t.Errorf("misleading field-validation message leaked through: %q", body)
	}
}

func TestConnect_OK(t *testing.T) {
	rec := mutate(t, &fakeQ{}, "POST", "/power/outlets/"+uuid.New().String()+"/connect",
		`{"asset_id":"`+uuid.New().String()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestDisconnect_NoConnection(t *testing.T) {
	rec := mutate(t, &fakeQ{}, "DELETE", "/power/outlets/"+uuid.New().String()+"/connect", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no connection on this outlet") {
		t.Fatalf("got %d %s, want 404 no connection", rec.Code, rec.Body.String())
	}
}

func TestDisconnect_OK(t *testing.T) {
	existing := &dbq.PowerConnection{OutletID: uuid.New(), AssetID: uuid.New()}
	rec := mutate(t, &fakeQ{connByO: existing}, "DELETE", "/power/outlets/"+uuid.New().String()+"/connect", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListOutlets_ConnectionMergedIntoOutlet(t *testing.T) {
	pid := uuid.New()
	oid := uuid.New()
	aid := uuid.New()
	cordColor := "blue"
	f := &fakeQ{
		asset: dbq.GetPduAssetRow{Kind: "pdu"},
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
