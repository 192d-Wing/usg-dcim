package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

const (
	tIssuer = "https://console.test"
	tACR    = "urn:test:cac"
)

var tNow = time.Unix(1_800_000_000, 0)

type testKey struct {
	priv *ecdsa.PrivateKey
	kid  string
}

func newTestKey(t *testing.T, kid string) testKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testKey{priv: k, kid: kid}
}

func (k testKey) jwk() map[string]string {
	b, _ := k.priv.PublicKey.Bytes() // 04||x||y
	return map[string]string{
		"kty": "EC", "crv": "P-256", "kid": k.kid, "use": "sig", "alg": "ES256",
		"x": base64.RawURLEncoding.EncodeToString(b[1:33]),
		"y": base64.RawURLEncoding.EncodeToString(b[33:65]),
	}
}

// jwksServer serves the current key set; hits counts fetches.
type jwksServer struct {
	srv  *httptest.Server
	keys atomic.Value // []testKey
	hits atomic.Int32
	fail atomic.Bool
}

func newJWKSServer(t *testing.T, keys ...testKey) *jwksServer {
	s := &jwksServer{}
	s.keys.Store(keys)
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		if s.fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		var out []map[string]string
		for _, k := range s.keys.Load().([]testKey) {
			out = append(out, k.jwk())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": out})
	}))
	t.Cleanup(s.srv.Close)
	return s
}

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time      { return time.Unix(c.t.Load(), 0) }
func (c *clock) add(d time.Duration) { c.t.Add(int64(d / time.Second)) }

func newClock() *clock { c := &clock{}; c.t.Store(tNow.Unix()); return c }

func newVerifier(t *testing.T, js *jwksServer, c *clock) *ConsoleVerifier {
	t.Helper()
	cache, err := NewJWKSCache(js.srv.URL, false)
	if err != nil {
		t.Fatal(err)
	}
	cache.Now = c.now
	v, err := NewConsoleVerifier(ConsoleConfig{
		Issuer: tIssuer, Audience: "dcim", RequiredACRs: []string{tACR},
		Leeway: 5 * time.Second, JWKS: cache, Now: c.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func baseClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss": tIssuer, "aud": "dcim", "sub": "kc-sub-1",
		"preferred_username": "alice", "name": "Alice",
		"roles": []string{"dcim:viewer"}, "ctx": map[string]any{"site": "s1", "tenant": nil, "environment": nil},
		"sid": "sid-abc", "jti": uuid.NewString(),
		"iat": tNow.Unix(), "nbf": tNow.Unix(), "exp": tNow.Add(60 * time.Second).Unix(),
		"acr": tACR,
	}
}

func sign(t *testing.T, k testKey, c jwt.MapClaims, hdr map[string]any) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, c)
	tok.Header["typ"] = consoleAssertionTyp
	tok.Header["kid"] = k.kid
	for h, v := range hdr {
		if v == nil {
			delete(tok.Header, h)
		} else {
			tok.Header[h] = v
		}
	}
	s, err := tok.SignedString(k.priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConsoleVerify_Valid(t *testing.T) {
	k := newTestKey(t, "k1")
	v := newVerifier(t, newJWKSServer(t, k), newClock())
	c, err := v.Verify(context.Background(), sign(t, k, baseClaims(), nil))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Subject != "kc-sub-1" || c.ACR != tACR || len(c.Roles) != 1 || c.Ctx.Site == nil || *c.Ctx.Site != "s1" {
		t.Errorf("claims: %+v", c)
	}
}

func TestConsoleVerify_Rejections(t *testing.T) {
	k := newTestKey(t, "k1")
	other := newTestKey(t, "k1") // same kid, different key
	unknown := newTestKey(t, "nope")
	mod := func(f func(jwt.MapClaims)) jwt.MapClaims { c := baseClaims(); f(c); return c }
	hs := func(t *testing.T) string {
		// HS256 with the public key bytes as the HMAC secret.
		pub, _ := k.priv.PublicKey.Bytes()
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, baseClaims())
		tok.Header["typ"] = consoleAssertionTyp
		tok.Header["kid"] = "k1"
		s, _ := tok.SignedString(pub)
		return s
	}
	none := func(t *testing.T) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, baseClaims())
		tok.Header["typ"] = consoleAssertionTyp
		tok.Header["kid"] = "k1"
		s, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		return s
	}
	cases := []struct {
		name string
		tok  func(t *testing.T) string
		want error
	}{
		{"empty", func(*testing.T) string { return "" }, errAssertionMissing},
		{"garbage", func(*testing.T) string { return "a.b.c" }, errAssertionClaims},
		{"too large", func(*testing.T) string { return strings.Repeat("a", consoleMaxTokenBytes+1) }, errAssertionTooLarge},
		{"alg none", none, errAssertionClaims},
		{"hs256 pubkey secret", hs, errAssertionClaims},
		{"wrong typ", func(t *testing.T) string { return sign(t, k, baseClaims(), map[string]any{"typ": "JWT"}) }, errAssertionHeader},
		{"missing typ", func(t *testing.T) string { return sign(t, k, baseClaims(), map[string]any{"typ": nil}) }, errAssertionHeader},
		{"missing kid", func(t *testing.T) string { return sign(t, k, baseClaims(), map[string]any{"kid": nil}) }, errAssertionHeader},
		{"unknown kid", func(t *testing.T) string { return sign(t, unknown, baseClaims(), nil) }, errAssertionKey},
		{"bad signature", func(t *testing.T) string { return sign(t, other, baseClaims(), nil) }, errAssertionClaims},
		{"wrong iss", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) { c["iss"] = "https://evil" }), nil)
		}, errAssertionClaims},
		{"wrong aud", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { c["aud"] = "hello" }), nil) }, errAssertionClaims},
		{"aud array", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) { c["aud"] = []string{"dcim", "hello"} }), nil)
		}, errAssertionClaims},
		{"expired", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) {
				c["iat"] = tNow.Add(-120 * time.Second).Unix()
				c["nbf"] = c["iat"]
				c["exp"] = tNow.Add(-60 * time.Second).Unix()
			}), nil)
		}, errAssertionClaims},
		{"nbf future", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) { c["nbf"] = tNow.Add(30 * time.Second).Unix() }), nil)
		}, errAssertionClaims},
		{"iat future", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) { c["iat"] = tNow.Add(30 * time.Second).Unix() }), nil)
		}, errAssertionClaims},
		{"missing exp", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { delete(c, "exp") }), nil) }, errAssertionClaims},
		{"missing nbf", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { delete(c, "nbf") }), nil) }, errAssertionClaims},
		{"missing iat", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { delete(c, "iat") }), nil) }, errAssertionClaims},
		{"ttl too long", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) { c["exp"] = tNow.Add(61 * time.Second).Unix() }), nil)
		}, errAssertionTTL},
		{"missing sub", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { c["sub"] = "" }), nil) }, errAssertionClaims},
		{"missing jti", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { delete(c, "jti") }), nil) }, errAssertionClaims},
		{"missing acr", func(t *testing.T) string { return sign(t, k, mod(func(c jwt.MapClaims) { delete(c, "acr") }), nil) }, errAssertionACR},
		{"wrong acr", func(t *testing.T) string {
			return sign(t, k, mod(func(c jwt.MapClaims) { c["acr"] = "urn:test:password" }), nil)
		}, errAssertionACR},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifier(t, newJWKSServer(t, k), newClock())
			_, err := v.Verify(context.Background(), tc.tok(t))
			if err != tc.want {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConsoleVerify_Replay(t *testing.T) {
	k := newTestKey(t, "k1")
	c := newClock()
	v := newVerifier(t, newJWKSServer(t, k), c)
	tok := sign(t, k, baseClaims(), nil)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(context.Background(), tok); err != errAssertionReplay {
		t.Fatalf("replay: got %v", err)
	}
	// Entry expires with the assertion (exp + leeway); by then the token
	// itself fails exp, so the cache never needs to outlive it.
	c.add(66 * time.Second)
	if _, err := v.Verify(context.Background(), tok); err != errAssertionClaims {
		t.Fatalf("after expiry: got %v", err)
	}
}

func TestReplayCache_BoundedAndSweeps(t *testing.T) {
	rc := newReplayCache(2)
	if rc.check("a", tNow.Add(time.Minute), tNow) != nil || rc.check("b", tNow.Add(time.Minute), tNow) != nil {
		t.Fatal("insert")
	}
	if err := rc.check("c", tNow.Add(time.Minute), tNow); err != errReplayCacheFull {
		t.Fatalf("full: %v", err)
	}
	if err := rc.check("c", tNow.Add(2*time.Minute), tNow.Add(time.Minute)); err != nil {
		t.Fatalf("after sweep: %v", err)
	}
}

func TestJWKS_KidRotationAndRateLimit(t *testing.T) {
	k1, k2 := newTestKey(t, "k1"), newTestKey(t, "k2")
	js := newJWKSServer(t, k1)
	c := newClock()
	v := newVerifier(t, js, c)
	if _, err := v.Verify(context.Background(), sign(t, k1, baseClaims(), nil)); err != nil {
		t.Fatal(err)
	}
	// Forged kids within MinRefetch: at most one extra fetch.
	for i := 0; i < 20; i++ {
		_, _ = v.Verify(context.Background(), sign(t, newTestKey(t, "forged"), baseClaims(), nil))
	}
	if got := js.hits.Load(); got > 2 {
		t.Fatalf("refetch storm: %d fetches", got)
	}
	// Rotate: k2 published; after the rate-limit window an unknown kid refetches.
	js.keys.Store([]testKey{k1, k2})
	c.add(61 * time.Second)
	cl := baseClaims()
	cl["iat"], cl["nbf"], cl["exp"] = c.now().Unix(), c.now().Unix(), c.now().Add(60*time.Second).Unix()
	if _, err := v.Verify(context.Background(), sign(t, k2, cl, nil)); err != nil {
		t.Fatalf("rotated kid: %v", err)
	}
}

func TestJWKS_ServesLastGoodThenFailsClosed(t *testing.T) {
	k := newTestKey(t, "k1")
	js := newJWKSServer(t, k)
	c := newClock()
	cache, _ := NewJWKSCache(js.srv.URL, false)
	cache.Now = c.now
	if _, err := cache.Key(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	js.fail.Store(true)
	c.add(6 * time.Minute) // stale, refetch fails, still within MaxAge
	if _, err := cache.Key(context.Background(), "k1"); err != nil {
		t.Fatalf("last good: %v", err)
	}
	c.add(5 * time.Minute) // past MaxAge
	if _, err := cache.Key(context.Background(), "k1"); err != ErrJWKSUnavailable {
		t.Fatalf("want fail closed, got %v", err)
	}
}

func TestJWKS_ColdFetchFailureIsUnavailable(t *testing.T) {
	js := newJWKSServer(t)
	js.fail.Store(true)
	cache, _ := NewJWKSCache(js.srv.URL, false)
	if _, err := cache.Key(context.Background(), "k1"); err != ErrJWKSUnavailable {
		t.Fatalf("got %v", err)
	}
}

func TestJWKS_Parse(t *testing.T) {
	k := newTestKey(t, "k1")
	good := k.jwk()
	bad := func(f func(m map[string]string)) map[string]string {
		m := map[string]string{}
		for a, b := range good {
			m[a] = b
		}
		f(m)
		return m
	}
	cases := []struct {
		name string
		keys []map[string]string
		ok   bool
	}{
		{"good", []map[string]string{good}, true},
		{"rsa", []map[string]string{bad(func(m map[string]string) { m["kty"] = "RSA" })}, false},
		{"p384", []map[string]string{bad(func(m map[string]string) { m["crv"] = "P-384" })}, false},
		{"no kid", []map[string]string{bad(func(m map[string]string) { delete(m, "kid") })}, false},
		{"hs alg", []map[string]string{bad(func(m map[string]string) { m["alg"] = "HS256" })}, false},
		{"off curve", []map[string]string{bad(func(m map[string]string) { m["y"] = m["x"] })}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{"keys": tc.keys})
			_, err := parseJWKS(b)
			if (err == nil) != tc.ok {
				t.Errorf("err=%v ok=%v", err, tc.ok)
			}
		})
	}
	if _, err := NewJWKSCache("file:///etc/passwd", false); err == nil {
		t.Error("file url accepted")
	}
}

func TestParseAuthMode(t *testing.T) {
	for in, want := range map[string]AuthMode{"": ModeStandalone, "Console": ModeConsole, "both": ModeBoth, "standalone": ModeStandalone} {
		if got, err := ParseAuthMode(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseAuthMode("oidc"); err == nil {
		t.Error("accepted bogus mode")
	}
}

func TestNewConsoleVerifier_RequiresConfig(t *testing.T) {
	cache, _ := NewJWKSCache("http://127.0.0.1:1/jwks", false)
	if _, err := NewConsoleVerifier(ConsoleConfig{Issuer: tIssuer, Audience: "dcim", JWKS: cache}); err == nil {
		t.Error("missing acr accepted")
	}
	if _, err := NewConsoleVerifier(ConsoleConfig{Audience: "dcim", RequiredACRs: []string{tACR}, JWKS: cache}); err == nil {
		t.Error("missing issuer accepted")
	}
}

// consoleFakeQ records JIT behaviour.
type consoleFakeQ struct {
	fakeQ
	bySub    map[string]dbq.User
	creates  int
	idpRoles []string
}

func (f *consoleFakeQ) GetUserBySsoSubject(_ context.Context, s string) (dbq.User, error) {
	if u, ok := f.bySub[s]; ok {
		return u, nil
	}
	return dbq.User{}, pgx.ErrNoRows
}
func (f *consoleFakeQ) CreateOidcUser(_ context.Context, a dbq.CreateOidcUserParams) (dbq.User, error) {
	f.creates++
	u := dbq.User{ID: uuid.New(), Email: a.Email, SsoSubject: a.SsoSubject, IsActive: true}
	f.bySub[*a.SsoSubject] = u
	return u, nil
}
func (f *consoleFakeQ) GetCapabilitiesForIdpRoles(_ context.Context, roles []string) ([]string, error) {
	f.idpRoles = roles
	var out []string
	for _, r := range roles {
		switch r {
		case "dcim:viewer":
			out = append(out, "inventory:*:read")
		case "dcim:operator":
			out = append(out, "inventory:*:update")
		}
	}
	return out, nil
}
func (f *consoleFakeQ) GetUser(_ context.Context, id uuid.UUID) (dbq.User, error) {
	for _, u := range f.bySub {
		if u.ID == id {
			return u, nil
		}
	}
	return dbq.User{}, pgx.ErrNoRows
}

type dispatchEnv struct {
	k   testKey
	q   *consoleFakeQ
	mw  func(http.Handler) http.Handler
	got *Principal
	h   http.Handler
	ca  *ConsoleAuth
}

func newDispatchEnv(t *testing.T, mode AuthMode) *dispatchEnv {
	t.Helper()
	k := newTestKey(t, "k1")
	q := &consoleFakeQ{bySub: map[string]dbq.User{}}
	q.userCaps = []string{"dns:zones:read"} // per-user DB grant
	ca := &ConsoleAuth{Log: nopLogger(), Q: q, Verifier: newVerifier(t, newJWKSServer(t, k), newClock())}
	bearer := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := bearerToken(r); !ok {
				http.Error(w, "no bearer", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), Principal{Label: "bearer"})))
		})
	}
	if mode == ModeConsole {
		bearer = APITokenOnly(q)
	}
	e := &dispatchEnv{k: k, q: q, got: &Principal{}, ca: ca}
	e.mw = Dispatch(mode, ca, bearer)
	e.h = e.mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*e.got, _ = From(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	return e
}

func (e *dispatchEnv) do(t *testing.T, method string, hdr map[string]string) int {
	req := httptest.NewRequest(method, "/x", nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Code
}

func TestDispatch(t *testing.T) {
	cases := []struct {
		name   string
		mode   AuthMode
		method string
		hdr    func(e *dispatchEnv, t *testing.T) map[string]string
		want   int
		label  string
	}{
		{"console valid GET", ModeConsole, "GET", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}
		}, 200, "user:"},
		{"console missing", ModeConsole, "GET", func(*dispatchEnv, *testing.T) map[string]string { return nil }, 401, ""},
		{"console session bearer refused", ModeConsole, "GET", func(*dispatchEnv, *testing.T) map[string]string {
			return map[string]string{"Authorization": "Bearer eyJhbGciOi.x.y"}
		}, 401, ""},
		{"console invalid api token", ModeConsole, "GET", func(*dispatchEnv, *testing.T) map[string]string {
			return map[string]string{"Authorization": "Bearer dcim_unknown"}
		}, 401, ""},
		{"console bearer plus assertion", ModeConsole, "GET", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil), "Authorization": "Bearer dcim_x"}
		}, 401, ""},
		{"console POST without csrf", ModeConsole, "POST", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}
		}, 403, ""},
		{"console POST wrong csrf", ModeConsole, "DELETE", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil), CSRFHeader: "yes"}
		}, 403, ""},
		{"console POST with csrf", ModeConsole, "POST", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil), CSRFHeader: "1"}
		}, 200, "user:"},
		{"both no assertion uses bearer", ModeBoth, "POST", func(*dispatchEnv, *testing.T) map[string]string {
			return map[string]string{"Authorization": "Bearer abc"}
		}, 200, "bearer"},
		{"both invalid assertion never falls back", ModeBoth, "GET", func(*dispatchEnv, *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: "a.b.c"}
		}, 401, ""},
		{"both valid assertion", ModeBoth, "GET", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}
		}, 200, "user:"},
		{"standalone ignores assertion", ModeStandalone, "GET", func(e *dispatchEnv, t *testing.T) map[string]string {
			return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}
		}, 401, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDispatchEnv(t, tc.mode)
			if got := e.do(t, tc.method, tc.hdr(e, t)); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
			if tc.label != "" && !strings.HasPrefix(e.got.Label, tc.label) {
				t.Errorf("label %q", e.got.Label)
			}
		})
	}
}

func TestDispatch_JITAndRoleMapping(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	cl := baseClaims()
	cl["roles"] = []string{"dcim:operator", "dcim:viewer", "other:admin"}
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, cl, nil)}) != 200 {
		t.Fatal("first request")
	}
	first := e.got.Subject
	if e.q.creates != 1 || e.got.Method != MethodConsole || !e.got.MFA {
		t.Fatalf("creates=%d principal=%+v", e.q.creates, e.got)
	}
	if len(e.q.idpRoles) != 2 {
		t.Errorf("non-dcim role forwarded: %v", e.q.idpRoles)
	}
	for _, want := range []string{"inventory:racks:read", "inventory:racks:update", "dns:zones:read"} {
		if !HasCapability(e.got.Capabilities, want) {
			t.Errorf("missing %s in %v", want, e.got.Capabilities)
		}
	}
	cl["jti"] = uuid.NewString()
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, cl, nil)}) != 200 {
		t.Fatal("second request")
	}
	if e.q.creates != 1 || e.got.Subject != first {
		t.Errorf("JIT not idempotent: creates=%d", e.q.creates)
	}
	// Replay of the same assertion is refused.
	cl2 := baseClaims()
	raw := sign(t, e.k, cl2, nil)
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: raw}) != 200 ||
		e.do(t, "GET", map[string]string{ConsoleAssertionHeader: raw}) != 401 {
		t.Error("replay not refused at middleware")
	}
}

func TestDispatch_LinksExistingAccountAndRejectsInactive(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	sub := "kc-sub-1"
	existing := dbq.User{ID: uuid.New(), Email: "a@x", SsoSubject: &sub, IsActive: true}
	e.q.bySub[sub] = existing
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}) != 200 ||
		e.got.Subject != existing.ID || e.q.creates != 0 {
		t.Fatalf("not linked: %+v creates=%d", e.got, e.q.creates)
	}
	e2 := newDispatchEnv(t, ModeConsole)
	existing.IsActive = false
	e2.q.bySub[sub] = existing
	if got := e2.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e2.k, baseClaims(), nil)}); got != 403 {
		t.Fatalf("inactive: %d", got)
	}
}

func TestDispatch_NoDcimRoleGets403OnHandler(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	e.q.userCaps = nil
	cl := baseClaims()
	cl["roles"] = []string{}
	h := e.mw(RequireCapability("inventory:racks:read")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(ConsoleAssertionHeader, sign(t, e.k, cl, nil))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestHandler_ConsoleModeRefusesStandaloneRoutesAndReportsMode(t *testing.T) {
	for _, tc := range []struct {
		mode      AuthMode
		assertion bool
		want      int
	}{
		{ModeConsole, false, 404},
		{ModeBoth, true, 404},
		{ModeStandalone, true, 400}, // reaches the handler (OIDC unconfigured)
		{ModeBoth, false, 400},
	} {
		h := &Handler{Q: &fakeQ{}, Mode: tc.mode}
		r := chi.NewRouter()
		h.Mount(r)
		for _, p := range []struct{ m, path string }{{"GET", "/auth/oidc/login"}, {"POST", "/auth/login"}, {"POST", "/auth/refresh"}, {"POST", "/auth/logout"}} {
			req := httptest.NewRequest(p.m, p.path, strings.NewReader("{}"))
			if tc.assertion {
				req.Header.Set(ConsoleAssertionHeader, "x")
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if tc.want == 404 && rec.Code != 404 {
				t.Errorf("%s %s %s: got %d", tc.mode, p.m, p.path, rec.Code)
			}
			if tc.want != 404 && rec.Code == 404 {
				t.Errorf("%s %s %s: unexpectedly refused", tc.mode, p.m, p.path)
			}
		}
		req := httptest.NewRequest("GET", "/auth/methods", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("/methods refused in %s", tc.mode)
		}
	}
}

func TestMe_ReportsAuthMode(t *testing.T) {
	sub := "s"
	uid := uuid.New()
	for _, tc := range []struct {
		method string
		user   dbq.User
		want   string
	}{
		{MethodConsole, dbq.User{ID: uid, SsoSubject: &sub}, "console"},
		{MethodAPIToken, dbq.User{ID: uid}, "api_token"},
		{"", dbq.User{ID: uid, SsoSubject: &sub}, "oidc"},
		{"", dbq.User{ID: uid}, "local"},
	} {
		h := &Handler{Q: &fakeQ{user: tc.user}}
		r := chi.NewRouter()
		h.Mount(r)
		req := httptest.NewRequest("GET", "/auth/me", nil)
		req = req.WithContext(WithPrincipal(req.Context(), Principal{Subject: uid, Method: tc.method}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var body struct {
			AuthMode string `json:"auth_mode"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.AuthMode != tc.want {
			t.Errorf("method %q: auth_mode %q want %q", tc.method, body.AuthMode, tc.want)
		}
	}
}
