// Package auth holds the bearer-token middleware. The Python otter's
// security stack (OIDC discovery, JWT verification, capability
// matching, scope ABAC) is deliberately *not* re-implemented yet —
// that's its own phase. For the vertical slice we accept any bearer
// that starts with `dcim_` (API token) or `eyJ` (JWT) and attach a
// stub Principal so handler code is shaped correctly.
//
// THE STUB IS A SECURITY FOOT-GUN. Loading this middleware in any
// shared environment without the real OIDC/capability check would
// grant `*` to anyone who can send an Authorization header. The
// MustStub constructor refuses to build the middleware unless the
// operator opts in explicitly via OTTER_GO_INSECURE_AUTH_STUB=true,
// and every request logs a loud `auth_stub_in_use` warning so the
// situation cannot drift to prod silently.
package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type ctxKey int

const principalKey ctxKey = 0

// EnvInsecureStub is the env var that gates the stub middleware. Must
// be set to a truthy value (1/true/yes/on) for MustStub to succeed.
const EnvInsecureStub = "OTTER_GO_INSECURE_AUTH_STUB"

type Principal struct {
	Subject      uuid.UUID
	Capabilities []string
}

// MustStub returns the stub middleware after asserting that the
// operator has consciously opted in. It panics (i.e. the process
// refuses to start) when the env var is unset or falsy. Replace the
// caller with the real OIDC+capabilities middleware before unsetting
// the env var.
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

// stubMiddleware is the actual handler chain; split out so tests can
// exercise it without the env-gate panic.
func stubMiddleware(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			// Loud per-request log so this can't go unnoticed in prod.
			log.Warn("auth_stub_in_use",
				"path", r.URL.Path,
				"method", r.Method,
				"remote", r.RemoteAddr,
			)
			p := Principal{Subject: uuid.Nil, Capabilities: []string{"*"}}
			ctx := context.WithValue(r.Context(), principalKey, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

func envTruthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
