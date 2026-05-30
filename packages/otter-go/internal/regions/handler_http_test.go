package regions

import (
	"context"
	"encoding/json"
	"errors"
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

type fakeQuerier struct {
	list  func(ctx context.Context, arg dbq.ListRegionsParams) ([]dbq.Region, error)
	count func(ctx context.Context, arg dbq.CountRegionsParams) (int64, error)
	get   func(ctx context.Context, id uuid.UUID) (dbq.Region, error)
}

func (f *fakeQuerier) ListRegions(ctx context.Context, arg dbq.ListRegionsParams) ([]dbq.Region, error) {
	if f.list != nil {
		return f.list(ctx, arg)
	}
	return nil, nil
}

func (f *fakeQuerier) CountRegions(ctx context.Context, arg dbq.CountRegionsParams) (int64, error) {
	if f.count != nil {
		return f.count(ctx, arg)
	}
	return 0, nil
}

func (f *fakeQuerier) GetRegion(ctx context.Context, id uuid.UUID) (dbq.Region, error) {
	if f.get != nil {
		return f.get(ctx, id)
	}
	return dbq.Region{}, pgx.ErrNoRows
}

func (f *fakeQuerier) CreateRegion(_ context.Context, arg dbq.CreateRegionParams) (dbq.Region, error) {
	return dbq.Region{ID: uuid.New(), Name: arg.Name, Code: arg.Code, Description: arg.Description}, nil
}

func (f *fakeQuerier) UpdateRegion(_ context.Context, arg dbq.UpdateRegionParams) (dbq.Region, error) {
	return dbq.Region{ID: arg.ID, Name: derefStr(arg.Name, "x"), Code: "x", Description: arg.Description}, nil
}

func derefStr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func mount(f *fakeQuerier) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func do(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	// Wildcard principal — inventory:regions:read gate added in the
	// inventory cutover blocks otherwise-anonymous test traffic.
	req := authtest.Request(method, path, authtest.PrincipalWithCaps("*"), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListRegions_OK(t *testing.T) {
	rid := uuid.New()
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListRegionsParams) ([]dbq.Region, error) {
			if arg.Limit != 50 || arg.Offset != 0 {
				t.Errorf("default pagination wrong: %+v", arg)
			}
			return []dbq.Region{{ID: rid, Name: "Region A", Code: "RA"}}, nil
		},
		count: func(context.Context, dbq.CountRegionsParams) (int64, error) { return 1, nil },
	}
	rec := do(t, mount(f), "GET", "/regions")
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Total != 1 || len(body.Items) != 1 || body.Items[0].Code != "RA" {
		t.Errorf("unexpected body: %+v", body)
	}
}

func TestListRegions_LimitClamped(t *testing.T) {
	var captured dbq.ListRegionsParams
	f := &fakeQuerier{
		list: func(_ context.Context, arg dbq.ListRegionsParams) ([]dbq.Region, error) {
			captured = arg
			return nil, nil
		},
	}
	do(t, mount(f), "GET", "/regions?limit=99999&offset=-5")
	if captured.Limit != 500 {
		t.Errorf("limit not clamped: %d", captured.Limit)
	}
	if captured.Offset != 0 {
		t.Errorf("offset not clamped: %d", captured.Offset)
	}
}

func TestListRegions_DBError_Returns500(t *testing.T) {
	boom := errors.New("connection refused")
	f := &fakeQuerier{
		list: func(context.Context, dbq.ListRegionsParams) ([]dbq.Region, error) { return nil, boom },
	}
	rec := do(t, mount(f), "GET", "/regions")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
}

func TestGetRegion_OK(t *testing.T) {
	id := uuid.New()
	f := &fakeQuerier{
		get: func(_ context.Context, got uuid.UUID) (dbq.Region, error) {
			if got != id {
				t.Errorf("wrong id: got %s want %s", got, id)
			}
			return dbq.Region{ID: id, Name: "Solo", Code: "SOLO"}, nil
		},
	}
	rec := do(t, mount(f), "GET", "/regions/"+id.String())
	if rec.Code != 200 {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var got dbq.Region
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Code != "SOLO" {
		t.Errorf("body wrong: %+v", got)
	}
}

func TestGetRegion_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQuerier{}), "GET", "/regions/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "region not found") {
		t.Errorf("body should say region not found: %s", rec.Body.String())
	}
}

func TestGetRegion_BadID(t *testing.T) {
	rec := do(t, mount(&fakeQuerier{}), "GET", "/regions/not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

