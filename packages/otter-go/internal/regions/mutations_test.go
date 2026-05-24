package regions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// mountWithCaps wires the handler and seeds a Principal with the given
// capabilities so the auth.RequireCapability middleware lets the
// request through. Mirrors what main.go does for /api/v1/*.
func mountWithCaps(f *fakeQuerier, caps []string) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: caps})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestCreateRegion_Forbidden(t *testing.T) {
	h := mountWithCaps(&fakeQuerier{}, []string{"inventory:regions:read"})
	body, _ := json.Marshal(map[string]string{"name": "EU", "code": "eu"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/regions", bytes.NewReader(body)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d", rec.Code)
	}
}

func TestCreateRegion_OK(t *testing.T) {
	h := mountWithCaps(&fakeQuerier{}, []string{"*"})
	body, _ := json.Marshal(map[string]string{"name": "EU", "code": "eu"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/regions", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Errorf("got %d", rec.Code)
	}
}

func TestCreateRegion_400OnMissingFields(t *testing.T) {
	h := mountWithCaps(&fakeQuerier{}, []string{"*"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/regions", bytes.NewReader([]byte(`{}`))))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestUpdateRegion_TrackPresentFields(t *testing.T) {
	var captured dbq.UpdateRegionParams
	c := &capturingQuerier{onUpdate: func(p dbq.UpdateRegionParams) { captured = p }}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"*"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	(&Handler{Q: c}).Mount(r)

	body := []byte(`{"description": null}`) // explicit null
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("PATCH", "/regions/"+uuid.New().String(), bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if !captured.DescriptionSet || captured.Description != nil {
		t.Errorf("expected DescriptionSet=true Description=nil; got %+v", captured)
	}
}

// capturingQuerier intercepts UpdateRegion to capture the args; other
// methods are inherited from the embedded fakeQuerier zero value.
type capturingQuerier struct {
	fakeQuerier
	onUpdate func(dbq.UpdateRegionParams)
}

func (c *capturingQuerier) UpdateRegion(_ context.Context, arg dbq.UpdateRegionParams) (dbq.Region, error) {
	if c.onUpdate != nil {
		c.onUpdate(arg)
	}
	return dbq.Region{ID: arg.ID}, nil
}
