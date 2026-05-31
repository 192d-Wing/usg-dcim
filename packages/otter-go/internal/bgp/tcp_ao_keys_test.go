package bgp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// tcpAoKeysFake extends fakeQ with closures for the new TCP-AO keys
// methods so each test can swap behavior without touching the
// package-shared fake.
type tcpAoKeysFake struct {
	fakeQ
	getChain  func(ctx context.Context, id uuid.UUID) (dbq.TcpAoKeyChain, error)
	getKey    func(ctx context.Context, id uuid.UUID) (dbq.TcpAoKey, error)
	createKey func(ctx context.Context, a dbq.CreateTcpAoKeyParams) (dbq.TcpAoKey, error)
	updateKey func(ctx context.Context, a dbq.UpdateTcpAoKeyParams) (dbq.TcpAoKey, error)
	maxKeyID  func(ctx context.Context, id uuid.UUID) (int32, error)

	lastListKeys   dbq.ListTcpAoKeysParams
	lastUpdateKey  dbq.UpdateTcpAoKeyParams
	createInputs   []dbq.CreateTcpAoKeyParams
	deletedKeyIDs  []uuid.UUID
	listKeysResult []dbq.TcpAoKey
}

func (f *tcpAoKeysFake) GetTcpAoKeyChain(ctx context.Context, id uuid.UUID) (dbq.TcpAoKeyChain, error) {
	if f.getChain != nil {
		return f.getChain(ctx, id)
	}
	return dbq.TcpAoKeyChain{}, pgx.ErrNoRows
}
func (f *tcpAoKeysFake) GetTcpAoKey(ctx context.Context, id uuid.UUID) (dbq.TcpAoKey, error) {
	if f.getKey != nil {
		return f.getKey(ctx, id)
	}
	return dbq.TcpAoKey{}, pgx.ErrNoRows
}
func (f *tcpAoKeysFake) ListTcpAoKeys(_ context.Context, a dbq.ListTcpAoKeysParams) ([]dbq.TcpAoKey, error) {
	f.lastListKeys = a
	return f.listKeysResult, nil
}
func (f *tcpAoKeysFake) CountTcpAoKeys(_ context.Context, _ dbq.CountTcpAoKeysParams) (int64, error) {
	return int64(len(f.listKeysResult)), nil
}
func (f *tcpAoKeysFake) CreateTcpAoKey(ctx context.Context, a dbq.CreateTcpAoKeyParams) (dbq.TcpAoKey, error) {
	f.createInputs = append(f.createInputs, a)
	if f.createKey != nil {
		return f.createKey(ctx, a)
	}
	return dbq.TcpAoKey{ID: uuid.New(), KeyChainID: a.KeyChainID, KeyID: a.KeyID,
		Algorithm: a.Algorithm, Secret: a.Secret}, nil
}
func (f *tcpAoKeysFake) UpdateTcpAoKey(ctx context.Context, a dbq.UpdateTcpAoKeyParams) (dbq.TcpAoKey, error) {
	f.lastUpdateKey = a
	if f.updateKey != nil {
		return f.updateKey(ctx, a)
	}
	return dbq.TcpAoKey{ID: a.ID}, nil
}
func (f *tcpAoKeysFake) DeleteTcpAoKey(_ context.Context, id uuid.UUID) error {
	f.deletedKeyIDs = append(f.deletedKeyIDs, id)
	return nil
}
func (f *tcpAoKeysFake) MaxKeyIDInTcpAoKeyChain(ctx context.Context, id uuid.UUID) (int32, error) {
	if f.maxKeyID != nil {
		return f.maxKeyID(ctx, id)
	}
	return 0, nil
}

func mountKeys(f *tcpAoKeysFake) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

// ----- list / get / create -----

func TestListTcpAoKeys_FiltersByKeyChainID(t *testing.T) {
	chainID := uuid.New()
	f := &tcpAoKeysFake{}
	req := authedReq(http.MethodGet, "/bgp/tcp-ao-keys?key_chain_id="+chainID.String(), nil)
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if f.lastListKeys.KeyChainID == nil || *f.lastListKeys.KeyChainID != chainID {
		t.Errorf("key_chain_id not threaded: %+v", f.lastListKeys)
	}
}

func TestListTcpAoKeys_BadKeyChainID(t *testing.T) {
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec,
		authedReq(http.MethodGet, "/bgp/tcp-ao-keys?key_chain_id=not-a-uuid", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestListTcpAoKeys_NoCap_403(t *testing.T) {
	req := authtest.Request(http.MethodGet, "/bgp/tcp-ao-keys",
		authtest.PrincipalWithCaps("routing:asns:read"), nil)
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestGetTcpAoKey_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec,
		authedReq(http.MethodGet, "/bgp/tcp-ao-keys/"+uuid.New().String(), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestCreateTcpAoKey_BadAlgorithm_400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"key_chain_id": uuid.New().String(), "key_id": 1, "send_id": 1, "recv_id": 1,
		"algorithm": "rot13", "secret": "abc",
	})
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-keys", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateTcpAoKey_ChainNotFound_404(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"key_chain_id": uuid.New().String(), "key_id": 1, "send_id": 1, "recv_id": 1,
		"algorithm": "hmac-sha1-96", "secret": "abc",
	})
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-keys", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Python's TcpAoKeyBase requires key_id/send_id/recv_id (no defaults).
// Mirror by rejecting omission in Go.
func TestCreateTcpAoKey_MissingRequiredIDs_400(t *testing.T) {
	chainID := uuid.New()
	body, _ := json.Marshal(map[string]any{
		"key_chain_id": chainID.String(),
		"algorithm":    "hmac-sha1-96",
		"secret":       "abc",
	})
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-keys", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Python's _validate_window raises when valid_to <= valid_from.
func TestCreateTcpAoKey_InvertedWindow_400(t *testing.T) {
	chainID := uuid.New()
	f := &tcpAoKeysFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: chainID}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{
		"key_chain_id": chainID.String(),
		"key_id":       1, "send_id": 1, "recv_id": 1,
		"algorithm": "hmac-sha1-96", "secret": "abc",
		"valid_from": "2027-01-01T00:00:00Z",
		"valid_to":   "2026-01-01T00:00:00Z",
	})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec, authedReq(http.MethodPost, "/bgp/tcp-ao-keys", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (inverted window), got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateTcpAoKey_OK(t *testing.T) {
	chainID := uuid.New()
	f := &tcpAoKeysFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: chainID}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{
		"key_chain_id": chainID.String(), "key_id": 1, "send_id": 1, "recv_id": 1,
		"algorithm": "aes-128-cmac", "secret": "deadbeef",
	})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec, authedReq(http.MethodPost, "/bgp/tcp-ao-keys", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ----- update set/unset semantics -----

func TestUpdateTcpAoKey_ExplicitNullDescriptionClears(t *testing.T) {
	id := uuid.New()
	desc := "old"
	f := &tcpAoKeysFake{
		getKey: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKey, error) {
			return dbq.TcpAoKey{ID: id, Description: &desc}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"description": nil})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-keys/"+id.String(), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.lastUpdateKey.DescriptionSet {
		t.Fatal("DescriptionSet must be true for explicit null")
	}
	if f.lastUpdateKey.Description != nil {
		t.Errorf("Description must be nil, got %v", *f.lastUpdateKey.Description)
	}
}

func TestUpdateTcpAoKey_ExplicitNullValidFromClears(t *testing.T) {
	id := uuid.New()
	f := &tcpAoKeysFake{
		getKey: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKey, error) {
			return dbq.TcpAoKey{ID: id}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"valid_from": nil})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-keys/"+id.String(), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !f.lastUpdateKey.ValidFromSet {
		t.Fatal("ValidFromSet must be true for explicit null")
	}
	if f.lastUpdateKey.ValidFrom != nil {
		t.Errorf("ValidFrom must be nil, got %v", *f.lastUpdateKey.ValidFrom)
	}
}

func TestUpdateTcpAoKey_OmittedKeepsCurrent(t *testing.T) {
	id := uuid.New()
	f := &tcpAoKeysFake{
		getKey: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKey, error) {
			return dbq.TcpAoKey{ID: id}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"send_id": 42})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-keys/"+id.String(), body))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if f.lastUpdateKey.DescriptionSet || f.lastUpdateKey.ValidFromSet || f.lastUpdateKey.ValidToSet {
		t.Error("set-flags must stay false when their keys were omitted")
	}
	if f.lastUpdateKey.SendID == nil || *f.lastUpdateKey.SendID != 42 {
		t.Errorf("SendID not threaded: %+v", f.lastUpdateKey.SendID)
	}
}

// Mirrors Python's TcpAoKeyUpdate which omits key_id intentionally —
// part of (chain_id, key_id) natural key + rotation timeline.
func TestUpdateTcpAoKey_KeyIDPatchRefused(t *testing.T) {
	id := uuid.New()
	f := &tcpAoKeysFake{
		getKey: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKey, error) {
			return dbq.TcpAoKey{ID: id}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"key_id": 42})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-keys/"+id.String(), body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (key_id not patchable); got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateTcpAoKey_BadAlgorithm_400(t *testing.T) {
	id := uuid.New()
	f := &tcpAoKeysFake{
		getKey: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKey, error) {
			return dbq.TcpAoKey{ID: id}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"algorithm": "rot13"})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPatch, "/bgp/tcp-ao-keys/"+id.String(), body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

// ----- rotate-batch -----

func TestRotateBatch_ChainNotFound_404(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"count": 1})
	rec := httptest.NewRecorder()
	mountKeys(&tcpAoKeysFake{}).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains/"+uuid.New().String()+"/rotate-batch", body))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestRotateBatch_BadCount(t *testing.T) {
	chainID := uuid.New()
	f := &tcpAoKeysFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: chainID}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"count": 999})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains/"+chainID.String()+"/rotate-batch", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRotateBatch_DefaultsAndResumeFromMaxKeyID(t *testing.T) {
	// Pin the rng so the test is deterministic. The handler reads
	// randomSecretHex via a package-level var; swap and restore.
	origRand := randomSecretHex
	randomSecretHex = func() (string, error) { return "deadbeef", nil }
	defer func() { randomSecretHex = origRand }()
	// Pin the clock too — defaults emit `start = timeNow()`.
	pinned := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	origNow := timeNow
	timeNow = func() time.Time { return pinned }
	defer func() { timeNow = origNow }()

	chainID := uuid.New()
	f := &tcpAoKeysFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: chainID}, nil
		},
		maxKeyID: func(_ context.Context, _ uuid.UUID) (int32, error) { return 7, nil },
	}
	// Empty body — exercise defaults (count=12, days_per_key=30, algo=hmac).
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains/"+chainID.String()+"/rotate-batch", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.createInputs) != 12 {
		t.Fatalf("expected 12 keys created, got %d", len(f.createInputs))
	}
	if f.createInputs[0].KeyID != 8 {
		t.Errorf("first key_id should resume at max+1 (=8), got %d", f.createInputs[0].KeyID)
	}
	if f.createInputs[11].KeyID != 19 {
		t.Errorf("last key_id should be 19, got %d", f.createInputs[11].KeyID)
	}
	if f.createInputs[0].Algorithm != "hmac-sha1-96" {
		t.Errorf("default algorithm wrong: %s", f.createInputs[0].Algorithm)
	}
	if f.createInputs[0].Secret != "deadbeef" {
		t.Errorf("secret not threaded from rng: %s", f.createInputs[0].Secret)
	}
	// First key window starts at pinned baseline.
	if f.createInputs[0].ValidFrom == nil || !f.createInputs[0].ValidFrom.Equal(pinned) {
		t.Errorf("first ValidFrom must equal pinned start; got %v", f.createInputs[0].ValidFrom)
	}
}

func TestRotateBatch_CustomStart_AcceptedAndOffsetCorrectly(t *testing.T) {
	chainID := uuid.New()
	customStart := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &tcpAoKeysFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: chainID}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{
		"start": customStart.Format(time.RFC3339),
		"count": 3, "days_per_key": 10, "algorithm": "aes-128-cmac",
	})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains/"+chainID.String()+"/rotate-batch", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(f.createInputs) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(f.createInputs))
	}
	if f.createInputs[0].ValidFrom == nil || !f.createInputs[0].ValidFrom.Equal(customStart) {
		t.Errorf("first ValidFrom must equal customStart; got %v", f.createInputs[0].ValidFrom)
	}
	expectedSecond := customStart.Add(10 * 24 * time.Hour)
	if f.createInputs[1].ValidFrom == nil || !f.createInputs[1].ValidFrom.Equal(expectedSecond) {
		t.Errorf("second ValidFrom must be start + 10d; got %v", f.createInputs[1].ValidFrom)
	}
	if f.createInputs[0].Algorithm != "aes-128-cmac" {
		t.Errorf("algorithm not threaded: %s", f.createInputs[0].Algorithm)
	}
}

func TestRotateBatch_CreateError_BailsAndReportsError(t *testing.T) {
	chainID := uuid.New()
	boom := errors.New("conflict")
	calls := 0
	f := &tcpAoKeysFake{
		getChain: func(_ context.Context, _ uuid.UUID) (dbq.TcpAoKeyChain, error) {
			return dbq.TcpAoKeyChain{ID: chainID}, nil
		},
		createKey: func(_ context.Context, _ dbq.CreateTcpAoKeyParams) (dbq.TcpAoKey, error) {
			calls++
			if calls == 2 {
				return dbq.TcpAoKey{}, boom
			}
			return dbq.TcpAoKey{ID: uuid.New()}, nil
		},
	}
	body, _ := json.Marshal(map[string]any{"count": 5})
	rec := httptest.NewRecorder()
	mountKeys(f).ServeHTTP(rec,
		authedReq(http.MethodPost, "/bgp/tcp-ao-key-chains/"+chainID.String()+"/rotate-batch", body))
	if rec.Code == http.StatusCreated {
		t.Fatalf("expected non-2xx after createKey error; got %d", rec.Code)
	}
	if calls != 2 {
		t.Errorf("createKey should bail after 2nd call; got %d", calls)
	}
}

// ----- helper: ensure bytes import is used (otherwise gofmt nukes it) -----

var _ = bytes.NewReader
