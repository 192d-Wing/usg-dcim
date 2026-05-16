// Package auth holds the bearer-token middleware. The Python otter's
// security stack (OIDC discovery, JWT verification, capability
// matching, scope ABAC) is deliberately *not* re-implemented yet —
// that's its own phase. For the vertical slice we accept any bearer
// that starts with `dcim_` (API token) or `eyJ` (JWT) and attach a
// stub Principal so handler code is shaped correctly for the eventual
// real check.
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type ctxKey int

const principalKey ctxKey = 0

type Principal struct {
	Subject      uuid.UUID
	Capabilities []string
}

// Require enforces presence of a bearer token. Capability/scope
// enforcement is a TODO; this middleware exists so handlers can
// already pull a Principal out of context.
func Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			httpx.Error(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		// STUB: build a Principal with no capability constraints.
		// Replace with real JWT/api_token verification + capability
		// load before promoting otter-go past phase 1.
		p := Principal{Subject: uuid.Nil, Capabilities: []string{"*"}}
		ctx := context.WithValue(r.Context(), principalKey, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}
