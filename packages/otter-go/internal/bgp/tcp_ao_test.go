package bgp

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

// tcpAoFake is a richer fakeQ for the TCP-AO tests so each test
// can swap in its own GetTcpAoKeyChain / CountKeysInTcpAoKeyChain /
// UpdateTcpAoKeyChain behaviors without touching the package-shared
// fakeQ.
type tcpAoFake struct {
	fakeQ
	getChain       func(ctx context.Context, id uuid.UUID) (dbq.TcpAoKeyChain, error)
	updateChain    func(ctx context.Context, a dbq.UpdateTcpAoKeyChainParams) (dbq.TcpAoKeyChain, error)
	deleteChain    func(ctx context.Context, id uuid.UUID) error
	countChainKeys func(ctx context.Context, id uuid.UUID) (int64, error)

	lastUpdate dbq.UpdateTcpAoKeyChainParams
	deletedIDs []uuid.UUID
}

func (f *tcpAoFake) GetTcpAoKeyChain(ctx context.Context, id uuid.UUID) (dbq.TcpAoKeyChain, error) {
	if f.getChain != nil {
		return f.getChain(ctx, id)
	}
	return dbq.TcpAoKeyChain{}, pgx.ErrNoRows
}
func (f *tcpAoFake) UpdateTcpAoKeyChain(ctx context.Context, a dbq.UpdateTcpAoKeyChainParams) (dbq.TcpAoKeyChain, error) {
	f.lastUpdate = a
	if f.updateChain != nil {
		return f.updateChain(ctx, a)
	}
	return dbq.TcpAoKeyChain{ID: a.ID}, nil
}
func (f *tcpAoFake) DeleteTcpAoKeyChain(ctx context.Context, id uuid.UUID) error {
	f.deletedIDs = append(f.deletedIDs, id)
	if f.deleteChain != nil {
		return f.deleteChain(ctx, id)
	}
	return nil
}
func (f *tcpAoFake) CountKeysInTcpAoKeyChain(ctx context.Context, id uuid.UUID) (int64, error) {
	if f.countChainKeys != nil {
		return f.countChainKeys(ctx, id)
	}
	return 0, nil
}

func mountTcpAo(f *tcpAoFake) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func authedReq(method, path string, body []byte) *http.Request {
	return authtest.Request(method, path, authtest.PrincipalWithCaps("*"), bytes.NewReader(body))
}

func TestListTcpAoKeyChains_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	mountTcpAo(&tcpAoFake{}).ServeHTTP(rec, authedReq(http.MethodGet, "/bgp/tcp-ao-key-chains", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	var body tcpAoChainsPage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Items == nil {
		t.Error("Items must be [] not null on empty list")
	}
}

func TestListTcpAoKeyChains_NoCap_403(t *testing.T) {
	f := &tcpAoFake{}
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	req := authtest.Request(http.MethodGet, "/bgp/tcp-ao-key-chains",
		authtest.PrincipalWithCaps("routing:asns:read"), nil) // wrong cap
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetTcpAoKeyChain_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	mountTcpAo(&tcpAoFake{}).ServeHTTP(rec,
		authedReq(http.MethodGet, "/bgp/tcp-ao-key-chains/"+uuid.New().String(), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestGetTcpAoKeyChain_OK(t *testing.T) {
	id := uuid.New()
	f := &tcpAoFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: id, Name: "lab-chain"}, nil
		},
	}
	rec := httptest.NewRecorder()
	mountTcpAo(f).ServeHTTP(rec,
		authedReq(http.MethodGet, "/bgp/tcp-ao-key-chains/"+id.String(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateTcpAoKeyChain_MissingName_400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"description": "no name"})
	rec := httptest.NewRecorder()
	mountTcpAo(&tcpAoFake{}).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestCreateTcpAoKeyChain_OK(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"name": "lab-chain"})
	rec := httptest.NewRecorder()
	mountTcpAo(&tcpAoFake{}).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Explicit `{"description": null}` clears the column (PATCH parity).
func TestUpdateTcpAoKeyChain_ExplicitNullDescription(t *testing.T) {
	id := uuid.New()
	desc := "old desc"
	f := &tcpAoFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: id, Name: "x", Description: &desc}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"description": nil})
	rec := httptest.NewRecorder()
	mountTcpAo(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-key-chains/"+id.String(), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.lastUpdate.DescriptionSet {
		t.Fatal("DescriptionSet must be true for explicit-null patch")
	}
	if f.lastUpdate.Description != nil {
		t.Errorf("Description must be nil for explicit null, got %v", *f.lastUpdate.Description)
	}
}

// Omitted description key keeps current — UpdateCableParams.DescriptionSet
// must stay false so the COALESCE branch keeps the existing value.
func TestUpdateTcpAoKeyChain_OmittedDescriptionKeepsCurrent(t *testing.T) {
	id := uuid.New()
	f := &tcpAoFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: id, Name: "x"}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"name": "renamed"})
	rec := httptest.NewRecorder()
	mountTcpAo(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-key-chains/"+id.String(), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastUpdate.DescriptionSet {
		t.Error("DescriptionSet must be false when the key was omitted from the body")
	}
	if f.lastUpdate.Name == nil || *f.lastUpdate.Name != "renamed" {
		t.Errorf("name not threaded: %+v", f.lastUpdate)
	}
}

func TestDeleteTcpAoKeyChain_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	mountTcpAo(&tcpAoFake{}).ServeHTTP(rec,
		authedReq(http.MethodDelete, "/bgp/tcp-ao-key-chains/"+uuid.New().String(), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

// Mirrors Python's ConflictError: a chain with keys can't be deleted.
func TestDeleteTcpAoKeyChain_WithKeys_Conflict(t *testing.T) {
	id := uuid.New()
	f := &tcpAoFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: id, Name: "x"}, nil
		},
		countChainKeys: func(_ context.Context, _ uuid.UUID) (int64, error) { return 3, nil },
	}
	rec := httptest.NewRecorder()
	mountTcpAo(f).ServeHTTP(rec,
		authedReq(http.MethodDelete, "/bgp/tcp-ao-key-chains/"+id.String(), nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.deletedIDs) != 0 {
		t.Error("DeleteTcpAoKeyChain must not run when keys still exist")
	}
}

func TestDeleteTcpAoKeyChain_OK(t *testing.T) {
	id := uuid.New()
	f := &tcpAoFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: id, Name: "x"}, nil
		},
	}
	rec := httptest.NewRecorder()
	mountTcpAo(f).ServeHTTP(rec,
		authedReq(http.MethodDelete, "/bgp/tcp-ao-key-chains/"+id.String(), nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.deletedIDs) != 1 || f.deletedIDs[0] != id {
		t.Errorf("delete not called: %v", f.deletedIDs)
	}
}
