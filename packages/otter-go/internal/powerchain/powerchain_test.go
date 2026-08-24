package powerchain

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func sidePtr(s string) *string { return &s }

func TestClassifyRedundancy_PduIsNa(t *testing.T) {
	sides, verdict := ClassifyRedundancy("pdu", []Connection{
		{PduSide: sidePtr("A")},
	})
	if verdict != "n/a" || len(sides) != 0 {
		t.Errorf("pdu kind = (%v, %q), want ([], n/a)", sides, verdict)
	}
}

func TestClassifyRedundancy_NoConnectionsIsUnpowered(t *testing.T) {
	sides, verdict := ClassifyRedundancy("server", nil)
	if verdict != "unpowered" || len(sides) != 0 {
		t.Errorf("got (%v, %q), want ([], unpowered)", sides, verdict)
	}
}

func TestClassifyRedundancy_TwoSidesIsRedundant(t *testing.T) {
	sides, verdict := ClassifyRedundancy("server", []Connection{
		{PduSide: sidePtr("A")},
		{PduSide: sidePtr("B")},
	})
	if verdict != "redundant" {
		t.Errorf("verdict = %q, want redundant", verdict)
	}
	if !reflect.DeepEqual(sides, []string{"A", "B"}) {
		t.Errorf("sides = %v, want [A B]", sides)
	}
}

func TestClassifyRedundancy_SingleSideIsSingle(t *testing.T) {
	sides, verdict := ClassifyRedundancy("server", []Connection{
		{PduSide: sidePtr("A")},
		{PduSide: sidePtr("A")},
	})
	if verdict != "single" {
		t.Errorf("verdict = %q, want single", verdict)
	}
	if !reflect.DeepEqual(sides, []string{"A"}) {
		t.Errorf("sides = %v, want [A]", sides)
	}
}

// nil PduSide on a connection is filtered out of the sides set —
// matches Python's `if c.get("pdu_side")` truthy check.
func TestClassifyRedundancy_NilSideFiltered(t *testing.T) {
	sides, verdict := ClassifyRedundancy("server", []Connection{
		{PduSide: nil},
	})
	if verdict != "single" {
		// One connection with no side → "single" (Python parity)
		t.Errorf("verdict = %q, want single", verdict)
	}
	if len(sides) != 0 {
		t.Errorf("sides = %v, want []", sides)
	}
}

// ---- Compute ----

type fakeQ struct {
	outlets []dbq.ListOutletsByPduIDsRow
	conns   []dbq.PowerConnection
	gotPdus []uuid.UUID
}

func (f *fakeQ) ListOutletsByPduIDs(_ context.Context, ids []uuid.UUID) ([]dbq.ListOutletsByPduIDsRow, error) {
	f.gotPdus = ids
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []dbq.ListOutletsByPduIDsRow
	for _, o := range f.outlets {
		if _, ok := want[o.PduAssetID]; ok {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeQ) ListPowerConnectionsByOutletIDs(_ context.Context, ids []uuid.UUID) ([]dbq.PowerConnection, error) {
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []dbq.PowerConnection
	for _, c := range f.conns {
		if _, ok := want[c.OutletID]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func TestCompute_EmptyAssets(t *testing.T) {
	res, err := Compute(context.Background(), &fakeQ{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.PerAsset == nil || len(res.PerAsset) != 0 {
		t.Errorf("per_asset should be empty map, not nil; got %+v", res.PerAsset)
	}
	if res.PDUs == nil || len(res.PDUs) != 0 {
		t.Errorf("pdus should be empty array, not nil; got %+v", res.PDUs)
	}
}

func TestCompute_PduWithRedundantServer(t *testing.T) {
	pduA, pduB := uuid.New(), uuid.New()
	server := uuid.New()
	o1, o2 := uuid.New(), uuid.New()
	sideA, sideB := "A", "B"
	assets := []dbq.Asset{
		{ID: pduA, Name: "pdu-a", Kind: "pdu", PduSide: &sideA, Mount: "rack-side", Face: "front"},
		{ID: pduB, Name: "pdu-b", Kind: "pdu", PduSide: &sideB, Mount: "rack-side", Face: "rear"},
		{ID: server, Name: "srv-1", Kind: "server", Mount: "rack-front", Face: "front"},
	}
	f := &fakeQ{
		outlets: []dbq.ListOutletsByPduIDsRow{
			{ID: o1, PduAssetID: pduA, Position: 1},
			{ID: o2, PduAssetID: pduB, Position: 1},
		},
		conns: []dbq.PowerConnection{
			{OutletID: o1, AssetID: server, PsuIndex: 0},
			{OutletID: o2, AssetID: server, PsuIndex: 1},
		},
	}
	res, err := Compute(context.Background(), f, assets)
	if err != nil {
		t.Fatal(err)
	}

	srv, ok := res.PerAsset[server.String()]
	if !ok {
		t.Fatalf("server entry missing from per_asset")
	}
	if srv.Redundancy != "redundant" {
		t.Errorf("redundancy = %q, want redundant", srv.Redundancy)
	}
	if !reflect.DeepEqual(srv.SidesCovered, []string{"A", "B"}) {
		t.Errorf("sides_covered = %v, want [A B]", srv.SidesCovered)
	}
	if len(srv.Connections) != 2 {
		t.Errorf("connections len = %d, want 2", len(srv.Connections))
	}

	pduEntry, ok := res.PerAsset[pduA.String()]
	if !ok || pduEntry.Redundancy != "n/a" {
		t.Errorf("PDU per_asset redundancy = %q, want n/a", pduEntry.Redundancy)
	}

	if len(res.PDUs) != 2 {
		t.Fatalf("pdus len = %d, want 2", len(res.PDUs))
	}
	// Each PDU has 1 outlet, used = 1.
	for _, p := range res.PDUs {
		if p.TotalOutlets != 1 || p.UsedOutlets != 1 {
			t.Errorf("pdu %s: total=%d used=%d, want 1/1", p.Name, p.TotalOutlets, p.UsedOutlets)
		}
	}
}

func TestCompute_UnpoweredServer(t *testing.T) {
	pdu := uuid.New()
	server := uuid.New()
	assets := []dbq.Asset{
		{ID: pdu, Name: "p", Kind: "pdu"},
		{ID: server, Name: "s", Kind: "server"},
	}
	res, _ := Compute(context.Background(), &fakeQ{
		outlets: []dbq.ListOutletsByPduIDsRow{{ID: uuid.New(), PduAssetID: pdu, Position: 1}},
	}, assets)
	srv := res.PerAsset[server.String()]
	if srv.Redundancy != "unpowered" {
		t.Errorf("redundancy = %q, want unpowered", srv.Redundancy)
	}
	if len(srv.Connections) != 0 {
		t.Errorf("connections should be empty; got %d", len(srv.Connections))
	}
}

func TestCompute_OneSideOnlyIsSingle(t *testing.T) {
	pdu := uuid.New()
	server := uuid.New()
	outletID := uuid.New()
	sideA := "A"
	assets := []dbq.Asset{
		{ID: pdu, Name: "p", Kind: "pdu", PduSide: &sideA},
		{ID: server, Name: "s", Kind: "server"},
	}
	res, _ := Compute(context.Background(), &fakeQ{
		outlets: []dbq.ListOutletsByPduIDsRow{{ID: outletID, PduAssetID: pdu, Position: 1}},
		conns:   []dbq.PowerConnection{{OutletID: outletID, AssetID: server, PsuIndex: 0}},
	}, assets)
	srv := res.PerAsset[server.String()]
	if srv.Redundancy != "single" {
		t.Errorf("redundancy = %q, want single", srv.Redundancy)
	}
}
