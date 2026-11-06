package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// auditSink records audit rows.
type auditSink struct {
	mu   sync.Mutex
	rows []dbq.InsertAuditLogParams
}

func (s *auditSink) InsertAuditLog(_ context.Context, p dbq.InsertAuditLogParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, p)
	return nil
}

func (s *auditSink) byAction(a string) []dbq.InsertAuditLogParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []dbq.InsertAuditLogParams
	for _, r := range s.rows {
		if r.Action == a {
			out = append(out, r)
		}
	}
	return out
}

func meta(t *testing.T, p dbq.InsertAuditLogParams) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(p.MetadataJson, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// scopeFailQ fails the ABAC scope query.
type scopeFailQ struct{ *consoleFakeQ }

func (scopeFailQ) GetUserScopedCapabilities(context.Context, uuid.UUID) ([]dbq.GetUserScopedCapabilitiesRow, error) {
	return nil, errors.New("db down")
}

// raceQ simulates a concurrent JIT: the first lookup misses, the
// create hits a unique violation, the re-read finds the row.
type raceQ struct {
	*consoleFakeQ
	lookups   int
	winner    dbq.User
	lastLogin []uuid.UUID
}

func (q *raceQ) GetUserBySsoSubject(_ context.Context, _ string) (dbq.User, error) {
	q.lookups++
	if q.lookups == 1 {
		return dbq.User{}, pgx.ErrNoRows
	}
	return q.winner, nil
}

func (q *raceQ) UpdateUserLastLogin(_ context.Context, id uuid.UUID) error {
	q.lastLogin = append(q.lastLogin, id)
	return nil
}

func (q *raceQ) CreateOidcUser(context.Context, dbq.CreateOidcUserParams) (dbq.User, error) {
	return dbq.User{}, &pgconn.PgError{Code: "23505"}
}

func TestCheckUpstreamIssuer(t *testing.T) {
	for _, tc := range []struct {
		up, sa string
		ok     bool
	}{
		{"", "", true},
		{"https://kc/realms/a", "", true},
		{"https://kc/realms/a", "https://kc/realms/a/", true},
		{"", "https://kc/realms/a", false},
		{"https://kc/realms/b", "https://kc/realms/a", false},
	} {
		if err := CheckUpstreamIssuer(tc.up, tc.sa); (err == nil) != tc.ok {
			t.Errorf("%q vs %q: err=%v", tc.up, tc.sa, err)
		}
	}
}

func TestJWKS_RequiresHTTPSOffLoopback(t *testing.T) {
	for _, tc := range []struct {
		url   string
		allow bool
		ok    bool
	}{
		{"https://gateway.internal/jwks", false, true},
		{"http://127.0.0.1:9/jwks", false, true},
		{"http://[::1]:9/jwks", false, true},
		{"http://localhost:9/jwks", false, true},
		{"http://gateway.internal/jwks", false, false},
		{"http://gateway.internal/jwks", true, true},
	} {
		if _, err := NewJWKSCache(tc.url, tc.allow); (err == nil) != tc.ok {
			t.Errorf("%s allow=%v: err=%v", tc.url, tc.allow, err)
		}
	}
}

func TestConsoleVerifier_DevACRRequiresFlagAndIsNotMFA(t *testing.T) {
	cache, _ := NewJWKSCache("http://127.0.0.1:1/jwks", false)
	dev := "urn:usg-ops-console:dev:password"
	cfg := ConsoleConfig{Issuer: tIssuer, Audience: "dcim", RequiredACRs: []string{dev}, JWKS: cache}
	if _, err := NewConsoleVerifier(cfg); err == nil {
		t.Fatal("dev acr accepted without OTTER_CONSOLE_DEV_ACR_ALLOWED")
	}
	cfg.AllowDevACR = true
	if _, err := NewConsoleVerifier(cfg); err != nil {
		t.Fatal(err)
	}

	e := newDispatchEnv(t, ModeConsole)
	e.ca.Verifier.cfg.RequiredACRs = []string{tACR, dev}
	cl := baseClaims()
	cl["acr"] = dev
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, cl, nil)}) != 200 || e.got.MFA {
		t.Fatalf("dev acr: MFA=%v", e.got.MFA)
	}
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}) != 200 || !e.got.MFA {
		t.Fatal("prod acr not MFA")
	}
}

func TestDispatch_ScopeErrorFailsClosed(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	e.ca.Q = scopeFailQ{e.q}
	if got := e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}); got != 503 {
		t.Fatalf("scope error: got %d, want 503", got)
	}
}

func TestDispatch_CarriesSidAndJti(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	cl := baseClaims()
	if e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, cl, nil)}) != 200 {
		t.Fatal("not 200")
	}
	if e.got.SessionID != "sid-abc" || e.got.AssertionID != cl["jti"] {
		t.Fatalf("sid/jti not carried: %q %q", e.got.SessionID, e.got.AssertionID)
	}
}

func TestDispatch_AuditsFailuresBoundedWithoutTokenData(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	sink := &auditSink{}
	e.ca.Audit = sink
	bad := sign(t, e.k, baseClaims(), nil) + "x" // bad signature
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = "10.1.2.3:4444"
		req.Header.Set(ConsoleAssertionHeader, bad)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("got %d", rec.Code)
		}
	}
	rows := sink.byAction("auth.console_reject")
	if len(rows) != 10 {
		t.Fatalf("rows=%d, want bounded at 10 per source IP", len(rows))
	}
	r := rows[0]
	if r.Success || r.ActorIP == nil || *r.ActorIP != "10.1.2.3" {
		t.Fatalf("bad row: %+v", r)
	}
	if m := meta(t, r); m["reason"] == "" || m["method"] != "console" {
		t.Fatalf("meta: %v", m)
	}
	for _, row := range sink.rows {
		if strings.Contains(string(row.MetadataJson), bad[:20]) || strings.Contains(string(row.MetadataJson), "kc-sub-1") {
			t.Fatal("token or claim data in audit row")
		}
	}
	// Another peer is not starved by the first one's flood, and its row
	// carries the per-reason suppressed count.
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.7.7.7:1"
	req.Header.Set(ConsoleAssertionHeader, bad)
	e.h.ServeHTTP(httptest.NewRecorder(), req)
	rows = sink.byAction("auth.console_reject")
	last := rows[len(rows)-1]
	if *last.ActorIP != "10.7.7.7" {
		t.Fatalf("other peer starved: %+v", last)
	}
	sup, _ := meta(t, last)["suppressed"].(map[string]any)
	if len(sup) != 1 {
		t.Fatalf("suppressed=%v", meta(t, last)["suppressed"])
	}
	for _, v := range sup {
		if v != float64(40) {
			t.Fatalf("suppressed=%v", sup)
		}
	}
}

func TestAuditLimiter_PerIPGlobalAndLRU(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newAuditLimiter(2, 5, 3, 10*time.Second)
	// Per IP.
	for i, want := range []bool{true, true, false} {
		if ok, _ := l.allow("a", "r1", now); ok != want {
			t.Fatalf("a#%d=%v", i, ok)
		}
	}
	// Global ceiling of 5 across IPs (a used 2).
	got := 0
	for _, ip := range []string{"b", "b", "c", "c"} {
		if ok, _ := l.allow(ip, "r2", now); ok {
			got++
		}
	}
	if got != 3 {
		t.Fatalf("allowed %d under global ceiling, want 3", got)
	}
	// LRU cap: map never exceeds maxIPs.
	for i := 0; i < 100; i++ {
		l.allow(fmt.Sprintf("ip%d", i), "r3", now)
	}
	if len(l.ips) != 3 || l.lru.Len() != 3 {
		t.Fatalf("ips=%d lru=%d, want 3", len(l.ips), l.lru.Len())
	}
	// Next window: allowed, and suppressed counts are per reason.
	ok, sup := l.allow("a", "r1", now.Add(11*time.Second))
	// r1 was already carried on b's first row; r2/r3 drops follow.
	if !ok || sup["r1"] != 0 || sup["r2"] != 1 || sup["r3"] != 100 {
		t.Fatalf("ok=%v sup=%v", ok, sup)
	}
	if _, sup = l.allow("a", "r1", now.Add(11*time.Second)); sup != nil {
		t.Fatalf("suppressed not cleared: %v", sup)
	}
}

func TestDispatch_InactiveAlwaysAudited(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	sink := &auditSink{}
	e.ca.Audit = sink
	sub := "kc-sub-1"
	e.q.bySub[sub] = dbq.User{ID: uuid.New(), Email: "a@x", SsoSubject: &sub, IsActive: false}
	for i := 0; i < 30; i++ {
		if got := e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}); got != 403 {
			t.Fatalf("got %d", got)
		}
	}
	if n := len(sink.byAction("auth.console_reject")); n != 30 {
		t.Fatalf("inactive rows=%d, want 30 (never rate-limited)", n)
	}
}

func TestDispatch_InactiveAuditedAndNotCached(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	sink := &auditSink{}
	e.ca.Audit = sink
	sub := "kc-sub-1"
	u := dbq.User{ID: uuid.New(), Email: "a@x", SsoSubject: &sub, IsActive: true}
	e.q.bySub[sub] = u
	hdr := func() map[string]string {
		return map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}
	}
	if e.do(t, "GET", hdr()) != 200 {
		t.Fatal("active not 200")
	}
	// Deactivate: the cached entry still serves until evicted.
	u.IsActive = false
	e.q.bySub[sub] = u
	e.ca.InvalidateUser(u.ID)
	if got := e.do(t, "GET", hdr()); got != 403 {
		t.Fatalf("after invalidate: %d", got)
	}
	rows := sink.byAction("auth.console_reject")
	if len(rows) != 1 || rows[0].ActorUserID == nil || *rows[0].ActorUserID != u.ID || meta(t, rows[0])["reason"] != "account_inactive" {
		t.Fatalf("inactive not audited: %+v", rows)
	}
	// Inactive is never cached: reactivation applies at once.
	u.IsActive = true
	e.q.bySub[sub] = u
	if got := e.do(t, "GET", hdr()); got != 200 {
		t.Fatalf("reactivated: %d", got)
	}
}

func TestResolveUser_JITUniqueViolationRereads(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	sub := "kc-sub-1"
	winner := dbq.User{ID: uuid.New(), Email: "w@x", SsoSubject: &sub, IsActive: true}
	rq := &raceQ{consoleFakeQ: e.q, winner: winner}
	e.ca.Q = rq
	sink := &auditSink{}
	e.ca.Audit = sink
	if got := e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}); got != 200 {
		t.Fatalf("got %d", got)
	}
	if e.got.Subject != winner.ID {
		t.Fatalf("subject %v, want %v", e.got.Subject, winner.ID)
	}
	if len(rq.lastLogin) != 1 || rq.lastLogin[0] != winner.ID {
		t.Fatalf("last login not updated: %v", rq.lastLogin)
	}
	if n := len(sink.byAction("auth.console_jit_create")); n != 0 {
		t.Fatalf("loser wrote %d JIT rows", n)
	}
	// Cached: a second request does not hit the DB again.
	if got := e.do(t, "GET", map[string]string{ConsoleAssertionHeader: sign(t, e.k, baseClaims(), nil)}); got != 200 {
		t.Fatalf("second: %d", got)
	}
	if rq.lookups != 2 {
		t.Fatalf("lookups=%d, want 2 (cached after re-read)", rq.lookups)
	}
}

func TestResolveUser_JITAuditHasSourceAndSid(t *testing.T) {
	e := newDispatchEnv(t, ModeConsole)
	sink := &auditSink{}
	e.ca.Audit = sink
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "10.9.9.9:1"
	req.Header.Set(ConsoleAssertionHeader, sign(t, e.k, baseClaims(), nil))
	e.h.ServeHTTP(httptest.NewRecorder(), req)
	rows := sink.byAction("auth.console_jit_create")
	if len(rows) != 1 || rows[0].ActorIP == nil || *rows[0].ActorIP != "10.9.9.9" || meta(t, rows[0])["sid"] != "sid-abc" {
		t.Fatalf("jit audit: %+v", rows)
	}
}
