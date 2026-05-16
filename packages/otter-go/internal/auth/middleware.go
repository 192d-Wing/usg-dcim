// Package auth holds the JWT bearer-token middleware and the stub
// fallback used in sealed dev environments.
//
// Real path (Verifying): parses Authorization: Bearer <JWT>, verifies
// the HS256 signature against the active or any rotated old secret,
// rejects revoked JTIs, and resolves the principal's capabilities from
// {user_roles assignments} ∪ {oidc_role_mappings matching idp_roles
// claim}. ABAC scope (per-site/fabric/region) is intentionally NOT
// enforced in PR 35 — every matched capability is treated as global.
// PR 36 layers scope on top.
//
// Stub path (MustStub): unchanged. Still env-gated by
// OTTER_GO_INSECURE_AUTH_STUB and still grants `*`. Available only so
// frontend devs can run otter-go without standing up Keycloak.
package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type ctxKey int

const principalKey ctxKey = 0

// EnvInsecureStub is the env var that gates the stub middleware. Must
// be truthy (1/true/yes/on) for MustStub to succeed.
const EnvInsecureStub = "OTTER_GO_INSECURE_AUTH_STUB"

// Principal is what every authenticated handler reads out of the
// request context. Capabilities is the deduped union of cap codes
// resolved from the user's role assignments and IdP-asserted roles.
// MFA mirrors the JWT's `mfa` claim and is read by capability gates
// that the deployment lists in mfa_required_caps.
type Principal struct {
	Subject      uuid.UUID
	Capabilities []string
	MFA          bool
	// Label is the audit-trail-friendly identifier. Today it's
	// "user:<uuid>"; once API tokens land it'll be "token:<id>".
	Label string
}

// Querier is the slice of sqlc methods the verify path needs. Lets
// tests swap in an in-memory fake without a real Postgres pool.
type Querier interface {
	GetUserCapabilities(ctx context.Context, userID uuid.UUID) ([]string, error)
	GetCapabilitiesForIdpRoles(ctx context.Context, idpRoles []string) ([]string, error)
	IsJtiRevoked(ctx context.Context, jti string) (bool, error)
	GetUser(ctx context.Context, id uuid.UUID) (dbq.User, error)
	GetUserByEmail(ctx context.Context, email string) (dbq.User, error)
	GetUserBySsoSubject(ctx context.Context, ssoSubject string) (dbq.User, error)
	CreateOidcUser(ctx context.Context, arg dbq.CreateOidcUserParams) (dbq.User, error)
	UpdateOidcUserOnLogin(ctx context.Context, arg dbq.UpdateOidcUserOnLoginParams) (dbq.User, error)

	UpdateUserLastLogin(ctx context.Context, id uuid.UUID) error
	UpdateUserRefreshToken(ctx context.Context, arg dbq.UpdateUserRefreshTokenParams) error
	InsertRevokedJti(ctx context.Context, arg dbq.InsertRevokedJtiParams) error
	GetApiTokenByHash(ctx context.Context, tokenHash string) (dbq.ApiToken, error)
	ListApiTokensByOwner(ctx context.Context, ownerUserID uuid.UUID) ([]dbq.ApiToken, error)
	GetApiToken(ctx context.Context, id uuid.UUID) (dbq.ApiToken, error)
	CreateApiToken(ctx context.Context, arg dbq.CreateApiTokenParams) (dbq.ApiToken, error)
	RevokeApiToken(ctx context.Context, id uuid.UUID) error
	TouchApiTokenLastUsed(ctx context.Context, id uuid.UUID) error
}

// Verifying returns the production JWT middleware. Every request must
// carry an Authorization: Bearer <JWT> header. The handler chain:
//   1. Reject if header missing/malformed.
//   2. Verify HS256 signature against primary or any rotated secret.
//   3. Reject if exp passed (jwt lib enforces this for us).
//   4. Reject if jti is in revoked_jtis and not yet pruned.
//   5. Build capabilities = user_role caps ∪ oidc-mapped caps.
//   6. Inject Principal into r.Context().
func Verifying(log *slog.Logger, q Querier, cfg VerifierConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			// API tokens start with "dcim_" (matches Python's
			// generate_api_token). Look up by sha256 hash; on hit,
			// build the principal directly from token.permission_codes
			// and skip the JWT path entirely.
			if strings.HasPrefix(raw, "dcim_") {
				p, ok := apiTokenPrincipal(r.Context(), q, raw)
				if !ok {
					httpx.Error(w, http.StatusUnauthorized, "invalid api token")
					return
				}
				ctx := context.WithValue(r.Context(), principalKey, p)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			claims, err := Verify(raw, cfg)
			if err != nil {
				log.Debug("jwt_reject", "err", err.Error())
				httpx.Error(w, http.StatusUnauthorized, "invalid bearer token")
				return
			}
			revoked, err := q.IsJtiRevoked(r.Context(), claims.JTI)
			if err != nil {
				log.Error("jti_check_failed", "err", err.Error())
				httpx.Error(w, http.StatusInternalServerError, "auth backend unavailable")
				return
			}
			if revoked {
				httpx.Error(w, http.StatusUnauthorized, "session revoked")
				return
			}
			caps, err := resolveCaps(r.Context(), q, claims)
			if err != nil {
				log.Error("caps_resolve_failed", "err", err.Error())
				httpx.Error(w, http.StatusInternalServerError, "auth backend unavailable")
				return
			}
			p := Principal{
				Subject:      claims.Subject,
				Capabilities: caps,
				MFA:          claims.MFA,
				Label:        "user:" + claims.Subject.String(),
			}
			ctx := context.WithValue(r.Context(), principalKey, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// resolveCaps unions user-assigned and IdP-mapped capability codes.
// Empty IdP roles claim means we only resolve via user_roles; missing
// user (race with delete) means we only resolve via IdP roles.
func resolveCaps(ctx context.Context, q Querier, c *SessionClaims) ([]string, error) {
	seen := map[string]struct{}{}
	add := func(codes []string) {
		for _, code := range codes {
			seen[code] = struct{}{}
		}
	}
	userCaps, err := q.GetUserCapabilities(ctx, c.Subject)
	if err != nil {
		return nil, err
	}
	add(userCaps)
	if len(c.IdpRoles) > 0 {
		idpCaps, err := q.GetCapabilitiesForIdpRoles(ctx, c.IdpRoles)
		if err != nil {
			return nil, err
		}
		add(idpCaps)
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	return out, nil
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	raw := strings.TrimPrefix(h, "Bearer ")
	if raw == "" {
		return "", false
	}
	return raw, true
}

// MustStub returns the stub middleware after asserting that the
// operator has consciously opted in. Panics on startup unless
// OTTER_GO_INSECURE_AUTH_STUB is truthy. Use for sealed dev envs only.
func MustStub(log *slog.Logger) func(http.Handler) http.Handler {
	if !envTruthy(os.Getenv(EnvInsecureStub)) {
		panic(fmt.Sprintf(
			"refusing to start: auth stub requested but %s is not truthy. "+
				"Set %s=true ONLY in a sealed dev environment, or wire the "+
				"real OIDC middleware before bringing otter-go up.",
			EnvInsecureStub, EnvInsecureStub,
		))
	}
	log.Warn("auth_stub_enabled",
		"env", EnvInsecureStub,
		"warning", "every authenticated request will be granted * capabilities",
	)
	return stubMiddleware(log)
}

func stubMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := bearerToken(r); !ok {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			log.Warn("auth_stub_in_use",
				"path", r.URL.Path,
				"method", r.Method,
				"remote", r.RemoteAddr,
			)
			p := Principal{Subject: uuid.Nil, Capabilities: []string{"*"}, Label: "stub"}
			ctx := context.WithValue(r.Context(), principalKey, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// From returns the Principal injected by the middleware, if any.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// WithPrincipal returns a context with the principal attached. Useful
// for tests that bypass the verify middleware but need RequireCapability
// to see a valid principal.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey, p)
}

// RequireCapability returns a middleware that rejects requests whose
// principal doesn't hold `code` (with wildcard matching). It panics if
// no Principal was injected upstream — that's a wiring bug, not a
// runtime error.
func RequireCapability(code string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := From(r.Context())
			if !ok {
				httpx.Error(w, http.StatusInternalServerError, "missing principal")
				return
			}
			if !HasCapability(p.Capabilities, code) {
				httpx.JSON(w, http.StatusForbidden, map[string]any{
					"detail": map[string]any{
						"error": map[string]string{
							"code":    "missing_capability",
							"message": code,
						},
					},
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func envTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
