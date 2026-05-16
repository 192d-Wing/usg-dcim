package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeQ struct {
	userCaps      []string
	idpCaps       []string
	revoked       bool
	user          dbq.User
	userErr       error
	gotIdpRoles   []string
	getCalls      int
	jtiCheckCalls int
}

func (f *fakeQ) GetUserCapabilities(_ context.Context, _ uuid.UUID) ([]string, error) {
	return f.userCaps, nil
}
func (f *fakeQ) GetCapabilitiesForIdpRoles(_ context.Context, roles []string) ([]string, error) {
	f.gotIdpRoles = roles
	return f.idpCaps, nil
}
func (f *fakeQ) IsJtiRevoked(_ context.Context, _ string) (bool, error) {
	f.jtiCheckCalls++
	return f.revoked, nil
}
func (f *fakeQ) GetUser(_ context.Context, _ uuid.UUID) (dbq.User, error) {
	f.getCalls++
	return f.user, f.userErr
}
func (f *fakeQ) GetUserByEmail(_ context.Context, _ string) (dbq.User, error) {
	return dbq.User{}, pgx.ErrNoRows
}
func (f *fakeQ) GetUserBySsoSubject(_ context.Context, _ string) (dbq.User, error) {
	return dbq.User{}, pgx.ErrNoRows
}
func (f *fakeQ) CreateOidcUser(_ context.Context, _ dbq.CreateOidcUserParams) (dbq.User, error) {
	return dbq.User{ID: uuid.New()}, nil
}
func (f *fakeQ) UpdateOidcUserOnLogin(_ context.Context, _ dbq.UpdateOidcUserOnLoginParams) (dbq.User, error) {
	return dbq.User{ID: uuid.New()}, nil
}

func mintJWT(t *testing.T, secret []byte, sub uuid.UUID, jti string, exp time.Time, idpRoles []string) string {
	t.Helper()
	c := jwt.MapClaims{
		"sub": sub.String(),
		"jti": jti,
		"exp": exp.Unix(),
		"iat": time.Now().Unix(),
	}
	if len(idpRoles) > 0 {
		c["idp_roles"] = idpRoles
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	s, err := tok.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerify_AcceptsPrimarySecret(t *testing.T) {
	secret := []byte("primary-key")
	sub := uuid.New()
	tok := mintJWT(t, secret, sub, "j1", time.Now().Add(5*time.Minute), nil)
	c, err := Verify(tok, VerifierConfig{PrimarySecret: secret})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Subject != sub || c.JTI != "j1" {
		t.Errorf("claims: %+v", c)
	}
}

func TestVerify_AcceptsRotatedOldSecret(t *testing.T) {
	old := []byte("old-key")
	primary := []byte("new-key")
	tok := mintJWT(t, old, uuid.New(), "j2", time.Now().Add(5*time.Minute), nil)
	if _, err := Verify(tok, VerifierConfig{PrimarySecret: primary, OldSecrets: [][]byte{old}}); err != nil {
		t.Fatalf("verify w/ old: %v", err)
	}
}

func TestVerify_RejectsWrongKey(t *testing.T) {
	tok := mintJWT(t, []byte("attacker"), uuid.New(), "j3", time.Now().Add(5*time.Minute), nil)
	if _, err := Verify(tok, VerifierConfig{PrimarySecret: []byte("real")}); err == nil {
		t.Fatal("expected verify to reject")
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	secret := []byte("s")
	tok := mintJWT(t, secret, uuid.New(), "j4", time.Now().Add(-1*time.Hour), nil)
	if _, err := Verify(tok, VerifierConfig{PrimarySecret: secret}); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestVerify_RejectsMissingJTI(t *testing.T) {
	secret := []byte("s")
	c := jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	s, _ := tok.SignedString(secret)
	if _, err := Verify(s, VerifierConfig{PrimarySecret: secret}); err == nil {
		t.Fatal("expected missing jti rejection")
	}
}

func TestVerifying_AttachesPrincipalWithUnionedCaps(t *testing.T) {
	secret := []byte("s")
	sub := uuid.New()
	q := &fakeQ{
		userCaps: []string{"inventory:sites:read"},
		idpCaps:  []string{"dns:zones:read", "inventory:sites:read"}, // dup
	}
	tok := mintJWT(t, secret, sub, "j", time.Now().Add(5*time.Minute), []string{"network-admin"})
	mw := Verifying(nopLogger(), q, VerifierConfig{PrimarySecret: secret})
	var got Principal
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = From(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if got.Subject != sub {
		t.Errorf("sub: %v", got.Subject)
	}
	if len(got.Capabilities) != 2 {
		t.Errorf("union+dedupe: got %v", got.Capabilities)
	}
	if q.jtiCheckCalls != 1 {
		t.Errorf("jti check should run exactly once, got %d", q.jtiCheckCalls)
	}
}

func TestVerifying_RejectsRevokedJTI(t *testing.T) {
	secret := []byte("s")
	q := &fakeQ{revoked: true}
	tok := mintJWT(t, secret, uuid.New(), "j", time.Now().Add(5*time.Minute), nil)
	mw := Verifying(nopLogger(), q, VerifierConfig{PrimarySecret: secret})
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("downstream should not run")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d", rec.Code)
	}
}

func TestVerifying_RejectsMissingBearer(t *testing.T) {
	mw := Verifying(nopLogger(), &fakeQ{}, VerifierConfig{PrimarySecret: []byte("x")})
	h := mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("downstream") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d", rec.Code)
	}
}

func TestHasCapability(t *testing.T) {
	cases := []struct {
		held []string
		code string
		want bool
	}{
		{[]string{"*"}, "anything:goes:read", true},
		// inventory:* is 2 segments — only matches 2-segment codes
		// (e.g. specialty caps like power:control). Python's
		// find_matching_capability enforces equal segment counts.
		{[]string{"inventory:*"}, "inventory:sites:read", false},
		{[]string{"inventory:sites:*"}, "inventory:sites:read", true},
		{[]string{"inventory:*:read"}, "inventory:sites:read", true},
		{[]string{"dns:*:read"}, "dns:zones:create", false},
		{[]string{"power:control"}, "power:control", true},
		{[]string{"power:*"}, "power:control", true},
		{[]string{"inventory:sites:read"}, "inventory:sites:create", false},
	}
	for _, c := range cases {
		if got := HasCapability(c.held, c.code); got != c.want {
			t.Errorf("held=%v code=%q: got %v want %v", c.held, c.code, got, c.want)
		}
	}
}

func TestRequireCapability_Forbidden(t *testing.T) {
	r := chi.NewRouter()
	r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), principalKey, Principal{Capabilities: []string{"dns:zones:read"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	}).With(RequireCapability("inventory:sites:create")).Get("/x", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not reach")
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d", rec.Code)
	}
}

func TestRequireCapability_Allows(t *testing.T) {
	r := chi.NewRouter()
	r.With(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), principalKey, Principal{Capabilities: []string{"*"}})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	}).With(RequireCapability("inventory:sites:create")).Get("/x", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 200 {
		t.Errorf("got %d", rec.Code)
	}
}
