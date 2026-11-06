// Console auth mode: otter-go embedded behind the usg-ops-console
// gateway. The gateway authenticates the user (CAC/PIV via Keycloak) and
// sends a fresh ES256 assertion (X-Console-Assertion) on every proxied
// request. This file verifies that assertion, resolves the principal, and
// dispatches between console and standalone (bearer) auth per
// OTTER_AUTH_MODE.
//
// Never log the assertion or its claims: rejection logs carry only a
// fixed reason string.
package auth

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// AuthMode selects how otter-go authenticates browser requests.
type AuthMode string

const (
	ModeStandalone AuthMode = "standalone" // bearer JWT / API token only (default)
	ModeConsole    AuthMode = "console"    // console assertion; API tokens for automation
	ModeBoth       AuthMode = "both"       // assertion if present, else bearer
)

// ParseAuthMode parses OTTER_AUTH_MODE; empty means standalone.
func ParseAuthMode(s string) (AuthMode, error) {
	switch m := AuthMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "":
		return ModeStandalone, nil
	case ModeStandalone, ModeConsole, ModeBoth:
		return m, nil
	}
	return "", fmt.Errorf("OTTER_AUTH_MODE must be standalone, console or both")
}

const (
	// ConsoleAssertionHeader carries the gateway-signed assertion.
	ConsoleAssertionHeader = "X-Console-Assertion"
	// CSRFHeader must be "1" on unsafe methods for console-authenticated
	// requests. Browsers cannot set it cross-origin without a preflight
	// that the gateway refuses.
	CSRFHeader = "X-DCIM-CSRF"

	consoleAssertionTyp  = "console-assertion+jwt"
	consoleMaxLeeway     = 30 * time.Second
	consoleAssertionTTL  = 60 * time.Second
	consoleMaxTokenBytes = 8 << 10
	consoleRolePrefix    = "dcim:"
	consoleMaxRoles      = 64
	consoleMaxSubLen     = 200
	consoleMaxJTILen     = 128
)

// ConsoleConfig is the verifier configuration (env OTTER_CONSOLE_*).
type ConsoleConfig struct {
	Issuer       string        // exact console origin (iss)
	Audience     string        // tool id (aud), "dcim"
	RequiredACRs []string      // acr must equal one of these
	Leeway       time.Duration // clamped to [0, 30s]
	JWKS         *JWKSCache
	Now          func() time.Time // nil = time.Now
	// AllowDevACR permits acr values containing ":dev:" (dev password
	// login at the console). Env OTTER_CONSOLE_DEV_ACR_ALLOWED=true;
	// DEV ONLY. Dev acr never sets Principal.MFA.
	AllowDevACR bool
}

// isDevACR reports whether acr names a dev-only (non-MFA) login.
func isDevACR(acr string) bool { return strings.Contains(acr, ":dev:") }

// CheckUpstreamIssuer binds console accounts to one IdP namespace.
// Console accounts are linked by users.sso_subject = assertion sub. A
// sub is only unique within its issuer, so when standalone OIDC is
// also configured (DCIM_OIDC_ISSUER), the Keycloak issuer behind the
// console (OTTER_CONSOLE_UPSTREAM_ISSUER) must equal it; otherwise a
// colliding sub from another realm would inherit that account. With
// no standalone OIDC every sso_subject is console-created, so linking
// is free.
//
// COMPLIANCE MAPPING:
// - NIST 800-53: AC-2 (Account Management) — accounts bound to (issuer, sub)
func CheckUpstreamIssuer(upstream, standalone string) error {
	norm := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	if norm(standalone) == "" {
		return nil
	}
	if norm(upstream) == "" {
		return errors.New("OTTER_CONSOLE_UPSTREAM_ISSUER is required when DCIM_OIDC_ISSUER is set")
	}
	if norm(upstream) != norm(standalone) {
		return errors.New("OTTER_CONSOLE_UPSTREAM_ISSUER must equal DCIM_OIDC_ISSUER (accounts are linked by sub)")
	}
	return nil
}

// ConsoleClaims is the subset of console_protocol::AssertionClaims that
// otter-go reads. Unknown claims are ignored.
type ConsoleClaims struct {
	PreferredUsername string   `json:"preferred_username"`
	Name              string   `json:"name"`
	Roles             []string `json:"roles"`
	Sid               string   `json:"sid"`
	ACR               string   `json:"acr"`
	Ctx               struct {
		Site        *string `json:"site"`
		Tenant      *string `json:"tenant"`
		Environment *string `json:"environment"`
	} `json:"ctx"`
	jwt.RegisteredClaims
}

// assertion rejection reasons (safe to log; never contain token data).
var (
	errAssertionMissing  = errors.New("missing")
	errAssertionBearer   = errors.New("bearer_with_assertion")
	errAssertionTooLarge = errors.New("too_large")
	errAssertionHeader   = errors.New("bad_header")
	errAssertionKey      = errors.New("unknown_kid")
	errAssertionClaims   = errors.New("bad_claims")
	errAssertionTTL      = errors.New("ttl_too_long")
	errAssertionACR      = errors.New("acr_mismatch")
	errAssertionReplay   = errors.New("replay")
	errReplayCacheFull   = errors.New("replay_cache_full")
)

// ConsoleVerifier verifies assertions and holds the jti replay cache.
type ConsoleVerifier struct {
	cfg    ConsoleConfig
	parser *jwt.Parser
	replay *replayCache
}

// NewConsoleVerifier validates cfg.
func NewConsoleVerifier(cfg ConsoleConfig) (*ConsoleVerifier, error) {
	if cfg.Issuer == "" || cfg.Audience == "" || cfg.JWKS == nil {
		return nil, errors.New("console auth requires OTTER_CONSOLE_ISSUER, OTTER_CONSOLE_TOOL_ID and OTTER_CONSOLE_JWKS_URL")
	}
	if len(cfg.RequiredACRs) == 0 {
		return nil, errors.New("console auth requires OTTER_CONSOLE_REQUIRED_ACR")
	}
	for _, acr := range cfg.RequiredACRs {
		if isDevACR(acr) && !cfg.AllowDevACR {
			return nil, errors.New("OTTER_CONSOLE_REQUIRED_ACR contains a :dev: acr; set OTTER_CONSOLE_DEV_ACR_ALLOWED=true (DEV ONLY)")
		}
	}
	if cfg.Leeway < 0 {
		cfg.Leeway = 0
	}
	if cfg.Leeway > consoleMaxLeeway {
		cfg.Leeway = consoleMaxLeeway
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	p := jwt.NewParser(
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer(cfg.Issuer),
		jwt.WithAudience(cfg.Audience),
		jwt.WithLeeway(cfg.Leeway),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(cfg.Now),
	)
	return &ConsoleVerifier{cfg: cfg, parser: p, replay: newReplayCache(200_000)}, nil
}

// Verify checks one raw assertion and records its jti. Errors are the
// errAssertion* reasons or ErrJWKSUnavailable / errReplayCacheFull.
func (v *ConsoleVerifier) Verify(ctx context.Context, raw string) (*ConsoleClaims, error) {
	if raw == "" {
		return nil, errAssertionMissing
	}
	if len(raw) > consoleMaxTokenBytes {
		return nil, errAssertionTooLarge
	}
	var keyErr error
	claims := &ConsoleClaims{}
	_, err := v.parser.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if typ, _ := t.Header["typ"].(string); typ != consoleAssertionTyp {
			return nil, errAssertionHeader
		}
		if alg, _ := t.Header["alg"].(string); alg != "ES256" {
			return nil, errAssertionHeader
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errAssertionHeader
		}
		k, err := v.cfg.JWKS.Key(ctx, kid)
		if err != nil {
			keyErr = err
			return nil, err
		}
		return k, nil
	})
	if err != nil {
		switch {
		case errors.Is(keyErr, ErrJWKSUnavailable):
			return nil, ErrJWKSUnavailable
		case keyErr != nil:
			return nil, errAssertionKey
		case errors.Is(err, errAssertionHeader):
			return nil, errAssertionHeader
		}
		return nil, errAssertionClaims
	}
	if len(claims.Audience) != 1 || claims.NotBefore == nil || claims.IssuedAt == nil {
		return nil, errAssertionClaims
	}
	if claims.Subject == "" || len(claims.Subject) > consoleMaxSubLen ||
		claims.ID == "" || len(claims.ID) > consoleMaxJTILen || len(claims.Roles) > consoleMaxRoles {
		return nil, errAssertionClaims
	}
	if claims.ExpiresAt.Sub(claims.IssuedAt.Time) > consoleAssertionTTL {
		return nil, errAssertionTTL
	}
	if !slices.Contains(v.cfg.RequiredACRs, claims.ACR) {
		return nil, errAssertionACR
	}
	if err := v.replay.check(claims.ID, claims.ExpiresAt.Add(v.cfg.Leeway), v.cfg.Now()); err != nil {
		return nil, err
	}
	return claims, nil
}

// replayCache remembers each jti until its assertion can no longer
// verify (exp + leeway). Bounded: when full of live entries, new
// assertions are refused (503) rather than evicting live jtis.
//
// POAM: the cache is per process; with several otter-go replicas a
// replay routed to a different replica is not detected (exp+aud still
// bound it to ~90 s at this tool).
type replayCache struct {
	mu        sync.Mutex
	m         map[string]time.Time
	max       int
	lastSweep time.Time
}

func newReplayCache(max int) *replayCache {
	return &replayCache{m: map[string]time.Time{}, max: max}
}

func (c *replayCache) check(jti string, until, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if exp, ok := c.m[jti]; ok && now.Before(exp) {
		return errAssertionReplay
	}
	if len(c.m) >= c.max || now.Sub(c.lastSweep) > 10*time.Second {
		for k, exp := range c.m {
			if !now.Before(exp) {
				delete(c.m, k)
			}
		}
		c.lastSweep = now
	}
	if len(c.m) >= c.max {
		return errReplayCacheFull
	}
	c.m[jti] = until
	return nil
}

// userCache avoids a users lookup on every request (each request has a
// fresh assertion). Only active accounts are cached (an inactive hit is
// re-read every time), entries live 15 s, and Invalidate evicts an
// account immediately.
//
// COMPLIANCE MAPPING:
// - NIST 800-53: AC-2 (Account Management) — deactivation effective within 15 s, or at once via Invalidate
//
// POAM: deactivation in another otter-go process (or via the admin API,
// which does not yet call Invalidate) is bounded by the 15 s TTL.
type userCache struct {
	mu  sync.Mutex
	m   map[string]userCacheEntry
	max int
	ttl time.Duration
}

type userCacheEntry struct {
	id     uuid.UUID
	active bool
	exp    time.Time
}

func (c *userCache) get(sub string, now time.Time) (uuid.UUID, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[sub]
	if !ok || !e.active || !now.Before(e.exp) {
		return uuid.Nil, false
	}
	return e.id, true
}

func (c *userCache) put(sub string, id uuid.UUID, active bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !active {
		delete(c.m, sub)
		return
	}
	if len(c.m) >= c.max {
		clear(c.m)
	}
	c.m[sub] = userCacheEntry{id: id, active: true, exp: now.Add(c.ttl)}
}

func (c *userCache) evictID(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if e.id == id {
			delete(c.m, k)
		}
	}
}

// ConsoleAuth builds the console-mode request authenticator.
type ConsoleAuth struct {
	Log      *slog.Logger
	Q        Querier
	Verifier *ConsoleVerifier
	Audit    AuditRecorder // optional; JIT creation and auth failures are audited
	// UpstreamIssuer is the Keycloak issuer behind the console
	// (OTTER_CONSOLE_UPSTREAM_ISSUER), recorded in audit metadata. main
	// validates it with CheckUpstreamIssuer.
	UpstreamIssuer string
	users          *userCache
	failAudit      *auditLimiter
	once           sync.Once
}

func (a *ConsoleAuth) init() {
	a.once.Do(func() {
		a.users = &userCache{m: map[string]userCacheEntry{}, max: 10_000, ttl: 15 * time.Second}
		a.failAudit = newAuditLimiter(10, 100, 4096, 10*time.Second)
	})
}

func (a *ConsoleAuth) cache() *userCache {
	a.init()
	return a.users
}

// InvalidateUser evicts a cached account (call on deactivate).
func (a *ConsoleAuth) InvalidateUser(id uuid.UUID) { a.cache().evictID(id) }

// auditLimiter bounds failure audit rows per process with a bucket per
// source IP plus a global ceiling, each counted per window. The per-IP
// map is an LRU capped at maxIPs, so spoofed or rotating sources cannot
// grow it without bound. Dropped rows are counted per reason and the
// counts are carried on the next row written ("suppressed"), so a flood
// of bad assertions from one peer cannot fill the audit table, saturate
// the DB, or starve rows for other peers.
type auditLimiter struct {
	mu          sync.Mutex
	perIP       int
	global      int
	window      time.Duration
	maxIPs      int
	globalStart time.Time
	globalN     int
	ips         map[string]*list.Element
	lru         *list.List // front = most recently used *ipBucket
	suppressed  map[string]int
}

type ipBucket struct {
	ip    string
	start time.Time
	n     int
}

func newAuditLimiter(perIP, global, maxIPs int, window time.Duration) *auditLimiter {
	return &auditLimiter{
		perIP: perIP, global: global, maxIPs: maxIPs, window: window,
		ips: map[string]*list.Element{}, lru: list.New(), suppressed: map[string]int{},
	}
}

// allow returns whether to write a row now and, when it does, the
// per-reason counts dropped since the last written row.
func (l *auditLimiter) allow(ip, reason string, now time.Time) (bool, map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.globalStart) >= l.window {
		l.globalStart, l.globalN = now, 0
	}
	var b *ipBucket
	if el, ok := l.ips[ip]; ok {
		l.lru.MoveToFront(el)
		b = el.Value.(*ipBucket)
	} else {
		for l.lru.Len() >= l.maxIPs {
			old := l.lru.Back()
			l.lru.Remove(old)
			delete(l.ips, old.Value.(*ipBucket).ip)
		}
		b = &ipBucket{ip: ip, start: now}
		l.ips[ip] = l.lru.PushFront(b)
	}
	if now.Sub(b.start) >= l.window {
		b.start, b.n = now, 0
	}
	if b.n >= l.perIP || l.globalN >= l.global {
		l.suppressed[reason]++
		return false, nil
	}
	b.n++
	l.globalN++
	return true, l.takeSuppressedLocked()
}

// takeSuppressed returns and clears the dropped counts (used by rows
// that bypass the limiter, such as account_inactive).
func (l *auditLimiter) takeSuppressed() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.takeSuppressedLocked()
}

func (l *auditLimiter) takeSuppressedLocked() map[string]int {
	if len(l.suppressed) == 0 {
		return nil
	}
	s := l.suppressed
	l.suppressed = map[string]int{}
	return s
}

// sourceIP is the TCP peer (finch-nginx or the gateway). Forwarded
// headers are not trusted here.
func sourceIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// audit writes one console auth row. metadata must never contain the
// assertion or its claims beyond sid/jti identifiers.
func (a *ConsoleAuth) audit(ctx context.Context, action string, uid *uuid.UUID, success bool, ip string, meta map[string]any) {
	if a.Audit == nil {
		return
	}
	p := dbq.InsertAuditLogParams{Action: action, Success: success}
	tt := "auth"
	p.TargetType = &tt
	label := "anonymous"
	if uid != nil && *uid != uuid.Nil {
		id := *uid
		p.ActorUserID = &id
		label = "user:" + id.String()
		tid := id.String()
		p.TargetID = &tid
	}
	p.ActorLabel = &label
	if ip != "" {
		p.ActorIP = &ip
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["method"] = MethodConsole
	if a.UpstreamIssuer != "" {
		meta["upstream_issuer"] = a.UpstreamIssuer
	}
	if b, err := json.Marshal(meta); err == nil {
		p.MetadataJson = b
	}
	if err := a.Audit.InsertAuditLog(ctx, p); err != nil {
		a.Log.Error("audit_write_failed", "action", action, "err", err.Error())
	}
}

// auditFailure records a console auth failure with a fixed reason
// string and source IP only, rate-limited by failAudit (per source IP
// plus a global ceiling). account_inactive rows are always written: they
// need a valid signed, unreplayed assertion, so they cannot be flooded
// by an unauthenticated peer, and losing them would hide disabled-account
// use.
//
// COMPLIANCE MAPPING:
// - NIST 800-53: AU-2 / AU-3 (Event Logging / Content) — failed console authentication: reason, source, outcome
// - NIST 800-53: AU-4 / SC-5 — failure rows bounded per window so floods cannot exhaust audit storage
func (a *ConsoleAuth) auditFailure(r *http.Request, reason string, uid *uuid.UUID) {
	a.init()
	ip := sourceIP(r)
	var suppressed map[string]int
	if reason == reasonAccountInactive {
		suppressed = a.failAudit.takeSuppressed()
	} else {
		var ok bool
		if ok, suppressed = a.failAudit.allow(ip, reason, a.now()); !ok {
			return
		}
	}
	meta := map[string]any{"reason": reason}
	if len(suppressed) > 0 {
		meta["suppressed"] = suppressed
	}
	a.audit(r.Context(), "auth.console_reject", uid, false, ip, meta)
}

const reasonAccountInactive = "account_inactive"

func (a *ConsoleAuth) now() time.Time {
	if a.Verifier != nil && a.Verifier.cfg.Now != nil {
		return a.Verifier.cfg.Now()
	}
	return time.Now()
}

// COMPLIANCE MAPPING:
// - NIST 800-53: IA-2(12) (Acceptance of PIV Credentials) — acr must equal the configured CAC/PIV acr
// - NIST 800-53: SC-23 (Session Authenticity) — ES256 assertion pinned to iss/aud/typ/kid, jti replay cache
// - NIST 800-53: AC-3 (Access Enforcement) — invalid assertion is 401, never falls back to bearer
// - NIST 800-53: AC-2 (Account Management) — accounts linked by IdP sub, JIT-created and audited
func (a *ConsoleAuth) authenticate(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	if _, hasBearer := bearerToken(r); hasBearer || r.Header.Get("Authorization") != "" {
		a.reject(w, r, errAssertionBearer)
		return Principal{}, false
	}
	vals := r.Header.Values(ConsoleAssertionHeader)
	if len(vals) != 1 {
		a.reject(w, r, errAssertionMissing)
		return Principal{}, false
	}
	claims, err := a.Verifier.Verify(r.Context(), vals[0])
	if err != nil {
		a.reject(w, r, err)
		return Principal{}, false
	}
	uid, err := a.resolveUser(r.Context(), claims, sourceIP(r))
	if err != nil {
		if errors.Is(err, errInactiveUser) {
			a.auditFailure(r, reasonAccountInactive, &uid)
			httpx.Error(w, http.StatusForbidden, "account disabled")
			return Principal{}, false
		}
		a.Log.Error("console_user_resolve_failed", "err", err.Error())
		httpx.Error(w, http.StatusInternalServerError, "auth backend unavailable")
		return Principal{}, false
	}
	roles := make([]string, 0, len(claims.Roles))
	for _, role := range claims.Roles {
		if strings.HasPrefix(role, consoleRolePrefix) {
			roles = append(roles, role)
		}
	}
	caps, err := resolveCaps(r.Context(), a.Q, &SessionClaims{Subject: uid, IdpRoles: roles})
	if err != nil {
		a.Log.Error("caps_resolve_failed", "err", err.Error())
		httpx.Error(w, http.StatusInternalServerError, "auth backend unavailable")
		return Principal{}, false
	}
	// Fail closed: nil Scopes means "global", so a scope lookup error
	// must not produce a principal.
	//
	// COMPLIANCE MAPPING:
	// - NIST 800-53: AC-3 (Access Enforcement) — ABAC scope resolution fails closed (503)
	scopeRows, scopeErr := a.Q.GetUserScopedCapabilities(r.Context(), uid)
	if scopeErr != nil {
		a.Log.Error("scope_resolve_failed", "err", scopeErr.Error())
		httpx.Error(w, http.StatusServiceUnavailable, "auth backend unavailable")
		return Principal{}, false
	}
	return Principal{
		Subject:      uid,
		Capabilities: caps,
		Scopes:       resolveUserScopes(scopeRows),
		// The allowed non-dev acr is the CAC/PIV login at the console;
		// a dev password acr is never MFA.
		// POAM: prod acr value tracked under POAM-028.
		MFA:         !isDevACR(claims.ACR),
		Label:       "user:" + uid.String(),
		Method:      MethodConsole,
		SessionID:   claims.Sid,
		AssertionID: claims.ID,
	}, true
}

func (a *ConsoleAuth) reject(w http.ResponseWriter, r *http.Request, err error) {
	a.auditFailure(r, err.Error(), nil)
	switch {
	case errors.Is(err, ErrJWKSUnavailable), errors.Is(err, errReplayCacheFull):
		a.Log.Warn("console_assertion_unavailable", "reason", err.Error())
		httpx.Error(w, http.StatusServiceUnavailable, "console auth unavailable")
	default:
		a.Log.Info("console_assertion_reject", "reason", err.Error())
		httpx.Error(w, http.StatusUnauthorized, "invalid console assertion")
	}
}

var errInactiveUser = errors.New("inactive user")

// resolveUser links the assertion sub to users.sso_subject (the same
// value the standalone OIDC flow stores, so one Keycloak identity is
// one DCIM account in either mode) and JIT-creates the account when
// absent. Email is never used for linking: the assertion carries no
// verified email, and email matching would let a console identity
// take over a local account.
//
// The returned id is set alongside errInactiveUser so the failure can
// be attributed in the audit row.
func (a *ConsoleAuth) resolveUser(ctx context.Context, c *ConsoleClaims, ip string) (uuid.UUID, error) {
	now := a.Verifier.cfg.Now()
	if id, ok := a.cache().get(c.Subject, now); ok {
		return id, nil
	}
	u, err := a.Q.GetUserBySsoSubject(ctx, c.Subject)
	switch {
	case err == nil:
		if !u.IsActive {
			a.cache().put(c.Subject, u.ID, false, now)
			return u.ID, errInactiveUser
		}
		_ = a.Q.UpdateUserLastLogin(ctx, u.ID)
	case errors.Is(err, pgx.ErrNoRows):
		sub := c.Subject
		var name *string
		if n := firstNonEmpty(c.Name, c.PreferredUsername); n != "" {
			if len(n) > 255 {
				n = n[:255]
			}
			name = &n
		}
		// Synthetic, non-routable, unique placeholder (users.email is
		// NOT NULL UNIQUE). Admins can edit it later.
		email := "console+" + uuid.NewSHA1(uuid.NameSpaceURL, []byte(sub)).String() + "@users.invalid"
		u, err = a.Q.CreateOidcUser(ctx, dbq.CreateOidcUserParams{Email: email, DisplayName: name, SsoSubject: &sub})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Concurrent first request for the same sub created it.
			u, err = a.Q.GetUserBySsoSubject(ctx, c.Subject)
			if err != nil {
				return uuid.Nil, err
			}
			if !u.IsActive {
				return u.ID, errInactiveUser
			}
			// Same as the normal lookup path: record the login and let
			// the shared put below cache it. No JIT audit row: the
			// winning request created the account and audited it.
			_ = a.Q.UpdateUserLastLogin(ctx, u.ID)
			break
		}
		if err != nil {
			return uuid.Nil, err
		}
		id := u.ID
		a.audit(ctx, "auth.console_jit_create", &id, true, ip,
			map[string]any{"sid": c.Sid, "jti": c.ID})
	default:
		return uuid.Nil, err
	}
	a.cache().put(c.Subject, u.ID, true, now)
	return u.ID, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// Dispatch returns the auth middleware for mode.
//   - standalone: bearer (unchanged).
//   - console: assertion required; the only bearer accepted is an API
//     token (dcim_…) and only when no assertion is present.
//   - both: assertion if the header is present (no fallback on failure),
//     otherwise bearer.
//
// Console-authenticated unsafe requests must carry X-DCIM-CSRF: 1.
//
// COMPLIANCE MAPPING:
// - NIST 800-53: AC-3 (Access Enforcement) — mode dispatch with no fallback after a failed assertion
// - NIST 800-53: SC-23 (Session Authenticity) — CSRF header on console-authenticated writes
func Dispatch(mode AuthMode, console *ConsoleAuth, bearer func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	if mode == ModeStandalone || mode == "" {
		return bearer
	}
	return func(next http.Handler) http.Handler {
		bearerNext := bearer(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(r.Header.Values(ConsoleAssertionHeader)) == 0 {
				if mode == ModeConsole {
					raw, ok := bearerToken(r)
					if !ok || !strings.HasPrefix(raw, "dcim_") {
						console.reject(w, r, errAssertionMissing)
						return
					}
				}
				bearerNext.ServeHTTP(w, r)
				return
			}
			p, ok := console.authenticate(w, r)
			if !ok {
				return
			}
			if isUnsafeMethod(r.Method) && r.Header.Get(CSRFHeader) != "1" {
				httpx.Error(w, http.StatusForbidden, "missing "+CSRFHeader+" header")
				return
			}
			ctx := WithScopeFilterCache(context.WithValue(r.Context(), principalKey, p))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// APITokenOnly is the bearer half used in console mode: API tokens for
// automation, never session JWTs (those come from the disabled
// standalone login).
func APITokenOnly(q Querier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok || !strings.HasPrefix(raw, "dcim_") {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			p, ok := apiTokenPrincipal(r.Context(), q, raw)
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "invalid api token")
				return
			}
			ctx := WithScopeFilterCache(context.WithValue(r.Context(), principalKey, p))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
