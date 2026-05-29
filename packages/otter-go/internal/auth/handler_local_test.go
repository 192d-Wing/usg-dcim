package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// withPrincipal wraps router calls so listTokens/issueToken/revokeToken
// see a Principal injected (since the test bypasses Verifying).
func withPrincipal(r http.Handler, p Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), principalKey, p)
		r.ServeHTTP(w, req.WithContext(ctx))
	})
}

// loginFakeQ extends fakeQ with a populated user the login handler
// can look up. Embedded so the new methods aren't duplicated.
type loginFakeQ struct {
	fakeQ
	loginUser dbq.User
	getErr    error
}

func (f *loginFakeQ) GetUserByEmail(_ context.Context, _ string) (dbq.User, error) {
	if f.getErr != nil {
		return dbq.User{}, f.getErr
	}
	return f.loginUser, nil
}

func TestLogin_BadCreds_401(t *testing.T) {
	q := &loginFakeQ{getErr: pgx.ErrNoRows}
	h := &Handler{Q: q, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	body, _ := json.Marshal(loginReq{Email: "x@y", Password: "wrong"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", bytes.NewReader(body)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d", rec.Code)
	}
}

func TestLogin_Success_MintsJWT(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	uid := uuid.New()
	hashStr := string(hash)
	q := &loginFakeQ{loginUser: dbq.User{ID: uid, Email: "a@b", IsActive: true, PasswordHash: &hashStr}}
	h := &Handler{Q: q, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 900}}
	r := chi.NewRouter()
	h.Mount(r)
	body, _ := json.Marshal(loginReq{Email: "a@b", Password: "hunter2"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/login", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	var out tokenOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.AccessToken == "" || out.ExpiresIn != 900 {
		t.Errorf("token shape: %+v", out)
	}
	// Verify the JWT is valid against our secret.
	if _, err := Verify(out.AccessToken, VerifierConfig{PrimarySecret: []byte("s")}); err != nil {
		t.Errorf("minted jwt didn't verify: %v", err)
	}
}

func TestLogout_204AndRevokes(t *testing.T) {
	secret := []byte("s")
	uid := uuid.New()
	// Mint a JWT we can revoke.
	c := jwt.MapClaims{
		"sub": uid.String(),
		"jti": "to-revoke",
		"exp": int64(99999999999),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	raw, _ := tok.SignedString(secret)

	revokedJtis := map[string]bool{}
	q := &recordingFakeQ{recordInsertJti: func(jti string) { revokedJtis[jti] = true }}
	h := &Handler{Q: q, Mint: MintConfig{Secret: secret, TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("got %d", rec.Code)
	}
	if !revokedJtis["to-revoke"] {
		t.Error("jti was not inserted into revoked_jtis")
	}
}

func TestLogout_NoBearer_Still204(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/auth/logout", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("got %d", rec.Code)
	}
}

// ---- /tokens cap-gate (structural enforcement of the middleware) ----
//
// These tests guard the RequireCapability wraps in Mount() so a future
// refactor that drops them is caught at CI time. The handlers also
// hold inline HasCapability checks as belt-and-suspenders, but tests
// belong at the structural layer the convention says is canonical
// (matches admin's TestRoleMutate_RejectsWithoutCap pattern).

func TestListTokens_RejectsWithoutCap(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest("GET", "/auth/tokens", nil)
	rec := httptest.NewRecorder()
	withPrincipal(r, Principal{Subject: uuid.New(), Capabilities: []string{
		"some:other:cap",
	}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (missing admin:api-tokens:read)", rec.Code)
	}
}

func TestIssueToken_RejectsWithoutCreateCap(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	body, _ := json.Marshal(tokenIssueReq{Name: "x", PermissionCodes: []string{}})
	req := httptest.NewRequest("POST", "/auth/tokens", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	withPrincipal(r, Principal{Subject: uuid.New(), Capabilities: []string{
		"admin:api-tokens:read", // read, not create
	}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (missing admin:api-tokens:create)", rec.Code)
	}
}

func TestRevokeToken_RejectsWithoutDeleteCap(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest("DELETE", "/auth/tokens/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	withPrincipal(r, Principal{Subject: uuid.New(), Capabilities: []string{
		"admin:api-tokens:read",
	}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (missing admin:api-tokens:delete)", rec.Code)
	}
}

func TestIssueToken_NoEscalation_403(t *testing.T) {
	h := &Handler{Q: &fakeQ{}, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	body, _ := json.Marshal(tokenIssueReq{
		Name:            "evil",
		PermissionCodes: []string{"inventory:sites:delete"},
	})
	req := httptest.NewRequest("POST", "/auth/tokens", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	withPrincipal(r, Principal{Subject: uuid.New(), Capabilities: []string{
		"admin:api-tokens:create", "inventory:sites:read",
	}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d", rec.Code)
	}
}

func TestIssueToken_GrantsHeldCaps(t *testing.T) {
	createdRows := 0
	q := &recordingFakeQ{onCreate: func() { createdRows++ }}
	h := &Handler{Q: q, Mint: MintConfig{Secret: []byte("s"), TTLSecond: 60}}
	r := chi.NewRouter()
	h.Mount(r)
	body, _ := json.Marshal(tokenIssueReq{
		Name: "ci", PermissionCodes: []string{"inventory:sites:read"},
	})
	req := httptest.NewRequest("POST", "/auth/tokens", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	withPrincipal(r, Principal{Subject: uuid.New(), Capabilities: []string{"*"}}).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d (%s)", rec.Code, rec.Body.String())
	}
	var out apiTokenOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Plaintext == "" || createdRows != 1 {
		t.Errorf("token not created: %+v", out)
	}
}

func TestApiTokenBearer_BuildsPrincipalFromTokenCaps(t *testing.T) {
	uid := uuid.New()
	tokenID := uuid.New()
	raw, digest, _ := generateAPIToken()
	q := &apiBearerFakeQ{
		fakeQ: fakeQ{
			user:     dbq.User{ID: uid, IsActive: true},
			userCaps: []string{"dns:zones:read"},
		},
		row: dbq.ApiToken{
			ID: tokenID, OwnerUserID: uid, TokenHash: digest,
			PermissionCodes: json.RawMessage(`["dns:zones:read"]`),
		},
	}
	mw := Verifying(nopLogger(), q, VerifierConfig{PrimarySecret: []byte("s")})
	var got Principal
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = From(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got.Subject != uid {
		t.Errorf("subject: %v", got.Subject)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "dns:zones:read" {
		t.Errorf("caps: %v", got.Capabilities)
	}
	if got.Label != "token:"+tokenID.String() {
		t.Errorf("label: %q", got.Label)
	}
}

func TestApiTokenBearer_RejectsInactiveOwner(t *testing.T) {
	uid := uuid.New()
	raw, digest, _ := generateAPIToken()
	q := &apiBearerFakeQ{
		fakeQ: fakeQ{
			user:     dbq.User{ID: uid, IsActive: false},
			userCaps: []string{"dns:zones:read"},
		},
		row: dbq.ApiToken{
			ID: uuid.New(), OwnerUserID: uid, TokenHash: digest,
			PermissionCodes: json.RawMessage(`["dns:zones:read"]`),
		},
	}
	mw := Verifying(nopLogger(), q, VerifierConfig{PrimarySecret: []byte("s")})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 (inactive owner), got %d", rec.Code)
	}
}

func TestApiTokenBearer_IntersectsCapsWithOwner(t *testing.T) {
	uid := uuid.New()
	tokenID := uuid.New()
	raw, digest, _ := generateAPIToken()
	q := &apiBearerFakeQ{
		fakeQ: fakeQ{
			user:     dbq.User{ID: uid, IsActive: true},
			userCaps: []string{"dns:zones:read"},
		},
		row: dbq.ApiToken{
			ID: tokenID, OwnerUserID: uid, TokenHash: digest,
			PermissionCodes: json.RawMessage(`["dns:zones:read","admin:users:create"]`),
		},
	}
	mw := Verifying(nopLogger(), q, VerifierConfig{PrimarySecret: []byte("s")})
	var got Principal
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = From(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "dns:zones:read" {
		t.Errorf("expected admin:users:create to be filtered out by owner-cap intersection; got %v", got.Capabilities)
	}
}

// ---- shared test fakes ----

type recordingFakeQ struct {
	fakeQ
	recordInsertJti func(string)
	onCreate        func()
}

func (f *recordingFakeQ) InsertRevokedJti(_ context.Context, arg dbq.InsertRevokedJtiParams) error {
	if f.recordInsertJti != nil {
		f.recordInsertJti(arg.Jti)
	}
	return nil
}
func (f *recordingFakeQ) CreateApiToken(_ context.Context, arg dbq.CreateApiTokenParams) (dbq.ApiToken, error) {
	if f.onCreate != nil {
		f.onCreate()
	}
	return dbq.ApiToken{
		ID: uuid.New(), Name: arg.Name, OwnerUserID: arg.OwnerUserID,
		TokenHash: arg.TokenHash, PermissionCodes: arg.PermissionCodes,
		ScopeJson: arg.ScopeJson, ExpiresAt: arg.ExpiresAt,
	}, nil
}

type apiBearerFakeQ struct {
	fakeQ
	row dbq.ApiToken
}

func (f *apiBearerFakeQ) GetApiTokenByHash(_ context.Context, h string) (dbq.ApiToken, error) {
	if h != f.row.TokenHash {
		return dbq.ApiToken{}, pgx.ErrNoRows
	}
	return f.row, nil
}
