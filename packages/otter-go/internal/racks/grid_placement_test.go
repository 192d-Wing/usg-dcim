package racks

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

func patchRack(t *testing.T, f *fakeQ, id uuid.UUID, body string) *int {
	t.Helper()
	rec := authtest.ServeRequest(mount(f), authtest.PrincipalWithCaps("*"),
		"PATCH", "/racks/"+id.String(), strings.NewReader(body))
	return &rec.Code
}

func placedRackFake(id uuid.UUID, captured *dbq.UpdateRackParams) *fakeQ {
	return &fakeQ{
		get: func(_ context.Context, _ uuid.UUID) (dbq.Rack, error) {
			return dbq.Rack{ID: id, SiteID: uuid.New(), UHeight: 42}, nil
		},
		update: func(_ context.Context, a dbq.UpdateRackParams) (dbq.Rack, error) {
			*captured = a
			return dbq.Rack{ID: a.ID}, nil
		},
	}
}

func TestUpdateRack_GridPlacement(t *testing.T) {
	id := uuid.New()
	var got dbq.UpdateRackParams
	code := patchRack(t, placedRackFake(id, &got), id,
		`{"grid_x": 3, "grid_y": 5, "grid_rotation": 90}`)
	if *code != http.StatusOK {
		t.Fatalf("status = %d", *code)
	}
	if !got.GridXSet || got.GridX == nil || *got.GridX != 3 {
		t.Errorf("grid_x: set=%v val=%v", got.GridXSet, got.GridX)
	}
	if !got.GridYSet || got.GridY == nil || *got.GridY != 5 {
		t.Errorf("grid_y: set=%v val=%v", got.GridYSet, got.GridY)
	}
	if got.GridRotation == nil || *got.GridRotation != 90 {
		t.Errorf("grid_rotation: %v", got.GridRotation)
	}
}

// Explicit nulls clear the placement (unplace from the floor plan);
// absent keys leave it untouched.
func TestUpdateRack_GridClearVsAbsent(t *testing.T) {
	id := uuid.New()
	var got dbq.UpdateRackParams
	code := patchRack(t, placedRackFake(id, &got), id, `{"grid_x": null, "grid_y": null}`)
	if *code != http.StatusOK {
		t.Fatalf("status = %d", *code)
	}
	if !got.GridXSet || got.GridX != nil || !got.GridYSet || got.GridY != nil {
		t.Errorf("nulls should clear: %+v", got)
	}

	got = dbq.UpdateRackParams{}
	code = patchRack(t, placedRackFake(id, &got), id, `{"name": "renamed"}`)
	if *code != http.StatusOK {
		t.Fatalf("status = %d", *code)
	}
	if got.GridXSet || got.GridYSet || got.GridRotation != nil {
		t.Errorf("absent keys must not touch placement: %+v", got)
	}
}

func TestUpdateRack_GridValidation(t *testing.T) {
	id := uuid.New()
	var got dbq.UpdateRackParams
	if code := patchRack(t, placedRackFake(id, &got), id, `{"grid_rotation": 45}`); *code != http.StatusBadRequest {
		t.Errorf("rotation 45: status = %d, want 400", *code)
	}
	if code := patchRack(t, placedRackFake(id, &got), id, `{"grid_x": -1}`); *code != http.StatusBadRequest {
		t.Errorf("negative grid_x: status = %d, want 400", *code)
	}
}
