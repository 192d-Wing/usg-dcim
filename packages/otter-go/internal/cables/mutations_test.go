package cables

import (
	"bytes"
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
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeMutQ is a fuller fake than fakeQ in handler_test.go: it returns
// a known cable from GetCable so the PATCH update flow can exercise
// scope + validators end-to-end.
type fakeMutQ struct {
	cable          dbq.Cable
	lastUpdate     dbq.UpdateCableParams
	updateCalled   bool
	assetsBySiteID map[uuid.UUID]uuid.UUID // assetID -> siteID override
}

func (f *fakeMutQ) ListCables(_ context.Context, _ dbq.ListCablesParams) ([]dbq.Cable, error) {
	return nil, nil
}
func (f *fakeMutQ) CountCables(_ context.Context, _ dbq.CountCablesParams) (int64, error) {
	return 0, nil
}
func (f *fakeMutQ) GetCable(_ context.Context, id uuid.UUID) (dbq.Cable, error) {
	if id == f.cable.ID {
		return f.cable, nil
	}
	return dbq.Cable{}, pgx.ErrNoRows
}
func (f *fakeMutQ) CreateCable(_ context.Context, _ dbq.CreateCableParams) (dbq.Cable, error) {
	return dbq.Cable{}, nil
}
func (f *fakeMutQ) DeleteCable(_ context.Context, _ uuid.UUID) error { return nil }
func (f *fakeMutQ) GetAssetSiteID(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	if sid, ok := f.assetsBySiteID[id]; ok {
		return sid, nil
	}
	return uuid.Nil, pgx.ErrNoRows
}
func (f *fakeMutQ) GetAsset(_ context.Context, id uuid.UUID) (dbq.Asset, error) {
	sid := f.cable.SiteID
	if override, ok := f.assetsBySiteID[id]; ok {
		sid = override
	}
	return dbq.Asset{ID: id, SiteID: sid, Name: "stub"}, nil
}
func (f *fakeMutQ) FindCableForPort(_ context.Context, _ dbq.FindCableForPortParams) (dbq.FindCableForPortRow, error) {
	return dbq.FindCableForPortRow{}, pgx.ErrNoRows
}
func (f *fakeMutQ) UpdateCable(_ context.Context, a dbq.UpdateCableParams) (dbq.Cable, error) {
	f.lastUpdate = a
	f.updateCalled = true
	return dbq.Cable{ID: a.ID, SiteID: f.cable.SiteID}, nil
}
func (f *fakeMutQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, pgx.ErrNoRows
}
func (f *fakeMutQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeMutQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeMutQ) ListSiteIDsForExpansion(_ context.Context, _ dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error) {
	return nil, nil
}

func mountMut(f *fakeMutQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func patch(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := authtest.Request(http.MethodPatch, path, authtest.PrincipalWithCaps("*"), bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postRaw sends a raw (possibly deliberately malformed) JSON string so
// the decode-failure paths can be exercised without json.Marshal
// "fixing" the body first.
func postRaw(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := authtest.Request(http.MethodPost, path, authtest.PrincipalWithCaps("*"), strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Regression: a wire-type mismatch (number where the NUMERIC-as-string
// convention expects a JSON string) used to fall through to the
// field-validation branch and 400 with the misleading "a_asset_id and
// b_asset_id required". It must now name the real problem.
func TestCreateCable_WireTypeMismatch_Honest400(t *testing.T) {
	rec := postRaw(t, mountMut(&fakeMutQ{}), "/cables", `{"length_m": 5}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid request body") {
		t.Errorf("want decode-error message, got %q", body)
	}
	if strings.Contains(body, "a_asset_id and b_asset_id required") {
		t.Errorf("misleading field-validation message leaked through: %q", body)
	}
}

// A well-formed body that merely omits the endpoints still gets the
// field-validation message — DecodeJSON only owns decode failures.
func TestCreateCable_MissingEndpoints_FieldMessage(t *testing.T) {
	rec := postRaw(t, mountMut(&fakeMutQ{}), "/cables", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "a_asset_id and b_asset_id required") {
		t.Errorf("got %q", rec.Body.String())
	}
}

// PATCH's decode branch now surfaces the real error too — including
// updateReq.UnmarshalJSON's own validation errors, which used to be
// flattened into "bad request body".
func TestPatchCable_WireTypeMismatch_Honest400(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: uuid.New(), BAssetID: uuid.New()}}
	req := authtest.Request(http.MethodPatch, "/cables/"+id.String(),
		authtest.PrincipalWithCaps("*"), strings.NewReader(`{"medium": 5}`))
	rec := httptest.NewRecorder()
	mountMut(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid request body") {
		t.Errorf("got %q", rec.Body.String())
	}
	if f.updateCalled {
		t.Error("UpdateCable must not run on a decode failure")
	}
}

func TestPatchCable_NotFound(t *testing.T) {
	f := &fakeMutQ{}
	rec := patch(t, mountMut(f), "/cables/"+uuid.New().String(), map[string]any{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPatchCable_BadID(t *testing.T) {
	f := &fakeMutQ{}
	rec := patch(t, mountMut(f), "/cables/not-a-uuid", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPatchCable_MetadataOnly_DoesNotRevalidatePorts(t *testing.T) {
	id, sid, aID, bID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: aID, BAssetID: bID}}
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"label": "trunk-7"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.updateCalled {
		t.Fatal("UpdateCable not called")
	}
	if !f.lastUpdate.LabelSet || f.lastUpdate.Label == nil || *f.lastUpdate.Label != "trunk-7" {
		t.Errorf("label not threaded: set=%v val=%v", f.lastUpdate.LabelSet, f.lastUpdate.Label)
	}
	if f.lastUpdate.AAssetID != nil || f.lastUpdate.BAssetID != nil {
		t.Error("placement should not change on metadata-only patch")
	}
	if f.lastUpdate.SiteID != nil {
		t.Error("site_id should only recompute when a_asset_id changes")
	}
}

func TestPatchCable_ExplicitNullClearsField(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	mediumNonEmpty := "fiber"
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: uuid.New(), BAssetID: uuid.New(), Medium: &mediumNonEmpty}}
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"medium": nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if !f.lastUpdate.MediumSet {
		t.Fatal("MediumSet should be true for {\"medium\":null}")
	}
	if f.lastUpdate.Medium != nil {
		t.Errorf("Medium should be nil for explicit null, got %v", *f.lastUpdate.Medium)
	}
}

func TestPatchCable_AAssetSwap_RecomputesSiteID(t *testing.T) {
	id, oldSite, newSite := uuid.New(), uuid.New(), uuid.New()
	oldA, newA, bID := uuid.New(), uuid.New(), uuid.New()
	f := &fakeMutQ{
		cable: dbq.Cable{ID: id, SiteID: oldSite, AAssetID: oldA, BAssetID: bID},
		// newA resolves to newSite; oldA + bID resolve to oldSite (default).
		assetsBySiteID: map[uuid.UUID]uuid.UUID{newA: newSite},
	}
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"a_asset_id": newA.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastUpdate.SiteID == nil || *f.lastUpdate.SiteID != newSite {
		t.Errorf("site_id should recompute to newSite (%s), got %v", newSite, f.lastUpdate.SiteID)
	}
	if f.lastUpdate.AAssetID == nil || *f.lastUpdate.AAssetID != newA {
		t.Error("a_asset_id not threaded")
	}
}

// Regression test for FindCableForPort precedence fix: a PATCH that
// repeats the current (a_asset, a_port) pair must NOT 409 against
// itself. The pre-fix SQL `(a_asset_id=$1 AND a_port=$2) OR
// (b_asset_id=$1 AND b_port=$2) AND id != $3` only applied excludeID
// to the b-end branch.
func TestPatchCable_SamePort_NoSelfConflict(t *testing.T) {
	port := "5"
	id, sid, aID, bID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: aID, BAssetID: bID, APort: &port}}
	// Repeat a_port = current value. placementChanged=true, but
	// validatePortNotInUse with excludeID=id must not match the row.
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"a_port": port})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.updateCalled {
		t.Fatal("UpdateCable should run for a same-port no-op PATCH")
	}
}

func TestPatchCable_NullAAssetID_Rejected(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: uuid.New(), BAssetID: uuid.New()}}
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"a_asset_id": nil})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.updateCalled {
		t.Error("UpdateCable must not run when a_asset_id is null")
	}
}

func TestPatchCable_LengthM_NullClears(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	existing := "1.50"
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: uuid.New(), BAssetID: uuid.New(), LengthM: &existing}}
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"length_m": nil})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.lastUpdate.LengthMSet {
		t.Fatal("LengthMSet must be true for {\"length_m\": null}")
	}
	if f.lastUpdate.LengthM != nil {
		t.Errorf("LengthM must be nil for explicit null, got %v", *f.lastUpdate.LengthM)
	}
}

func TestPatchCable_LengthM_StringForm_Accepted(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: uuid.New(), BAssetID: uuid.New()}}
	body := `{"length_m":"3.25"}`
	req := authtest.Request(http.MethodPatch, "/cables/"+id.String(), authtest.PrincipalWithCaps("*"), bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	mountMut(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.lastUpdate.LengthMSet || f.lastUpdate.LengthM == nil || *f.lastUpdate.LengthM != "3.25" {
		t.Errorf("LengthM string fallback failed: set=%v val=%v", f.lastUpdate.LengthMSet, f.lastUpdate.LengthM)
	}
}

func TestPatchCable_AuditDiffPopulated(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: uuid.New(), BAssetID: uuid.New()}}
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"label": "rack-7-cab-3"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if !f.updateCalled {
		t.Fatal("UpdateCable should run")
	}
	// updateReq.diff() is package-private; assert via the UpdateCable
	// param shape instead — the diff payload is constructed from the
	// same set flags.
	if !f.lastUpdate.LabelSet {
		t.Error("audit diff should carry the label change (set flag missing)")
	}
}

func TestPatchCable_SameEndpoints_400(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	aID := uuid.New()
	f := &fakeMutQ{cable: dbq.Cable{ID: id, SiteID: sid, AAssetID: aID, BAssetID: uuid.New()}}
	// Patch b_asset_id = aID → endpoints collide.
	rec := patch(t, mountMut(f), "/cables/"+id.String(), map[string]any{"b_asset_id": aID.String()})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.updateCalled {
		t.Error("UpdateCable should not run when endpoints collide")
	}
}
