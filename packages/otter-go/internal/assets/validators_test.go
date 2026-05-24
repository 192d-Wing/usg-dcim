package assets

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// placementFakeQ overrides the GetRack + ListRackAssetsForPlacement
// methods on the package-default fakeQ so the placement validator
// can be exercised in isolation.
type placementFakeQ struct {
	fakeQ
	uHeight int32
	placed  []dbq.RackPlacementRow
}

func (p *placementFakeQ) GetRack(_ context.Context, id uuid.UUID) (dbq.Rack, error) {
	return dbq.Rack{ID: id, UHeight: p.uHeight}, nil
}
func (p *placementFakeQ) ListRackAssetsForPlacement(_ context.Context, _ dbq.ListRackAssetsForPlacementParams) ([]dbq.RackPlacementRow, error) {
	return p.placed, nil
}

func u(n int32) *int32 { return &n }

func TestUGridFit_Overflow(t *testing.T) {
	h := &Handler{Q: &placementFakeQ{uHeight: 42}}
	err := h.validateUGridFit(context.Background(), uuid.New(), uuid.Nil, 40, 5, "front")
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Errorf("expected overflow error, got %v", err)
	}
}

func TestUGridFit_Underflow(t *testing.T) {
	h := &Handler{Q: &placementFakeQ{uHeight: 42}}
	err := h.validateUGridFit(context.Background(), uuid.New(), uuid.Nil, 0, 1, "front")
	if err == nil {
		t.Error("position 0 should overflow")
	}
}

func TestUGridFit_NoCollisionOK(t *testing.T) {
	h := &Handler{Q: &placementFakeQ{
		uHeight: 42,
		placed: []dbq.RackPlacementRow{
			{ID: uuid.New(), Name: "occupies-u10-12", RackPositionU: u(10), RackUnits: u(3)},
		},
	}}
	// Requesting U20-U21 is clear of U10-U12.
	if err := h.validateUGridFit(context.Background(), uuid.New(), uuid.Nil, 20, 2, "front"); err != nil {
		t.Errorf("clear placement rejected: %v", err)
	}
}

func TestUGridFit_Collides(t *testing.T) {
	h := &Handler{Q: &placementFakeQ{
		uHeight: 42,
		placed: []dbq.RackPlacementRow{
			{ID: uuid.New(), Name: "router-A", RackPositionU: u(10), RackUnits: u(3)},
		},
	}}
	// Requesting U11-U12 overlaps router-A's U10-U12.
	err := h.validateUGridFit(context.Background(), uuid.New(), uuid.Nil, 11, 2, "front")
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Errorf("expected collision error, got %v", err)
	}
}

func TestUGridFit_DefaultsUnitsToOne(t *testing.T) {
	h := &Handler{Q: &placementFakeQ{uHeight: 42}}
	if err := h.validateUGridFit(context.Background(), uuid.New(), uuid.Nil, 1, 0, "front"); err != nil {
		t.Errorf("units=0 should default to 1: %v", err)
	}
}
