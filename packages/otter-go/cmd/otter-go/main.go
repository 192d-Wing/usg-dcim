// Command otter-go is the Go port of the Python otter API. See
// packages/otter-go/README.md for status and the phased migration plan
// in docs/dev/otter-go-migration.md.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/alerts"
	"github.com/usg-dcim/packages/otter-go/internal/admin"
	"github.com/usg-dcim/packages/otter-go/internal/assets"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/bgp"
	"github.com/usg-dcim/packages/otter-go/internal/cables"
	"github.com/usg-dcim/packages/otter-go/internal/collectors"
	"github.com/usg-dcim/packages/otter-go/internal/dns"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
	"github.com/usg-dcim/packages/otter-go/internal/ipam"
	"github.com/usg-dcim/packages/otter-go/internal/lir"
	"github.com/usg-dcim/packages/otter-go/internal/locations"
	"github.com/usg-dcim/packages/otter-go/internal/notifications"
	"github.com/usg-dcim/packages/otter-go/internal/organization"
	"github.com/usg-dcim/packages/otter-go/internal/power"
	"github.com/usg-dcim/packages/otter-go/internal/racks"
	"github.com/usg-dcim/packages/otter-go/internal/regions"
	"github.com/usg-dcim/packages/otter-go/internal/search"
	"github.com/usg-dcim/packages/otter-go/internal/sites"
	"github.com/usg-dcim/packages/otter-go/internal/stencils"
	"github.com/usg-dcim/packages/otter-go/internal/telemetry"
	"github.com/usg-dcim/packages/shared-go/env"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgDSN := env.String("DCIM_POSTGRES_DSN_RAW", "postgres://dcim:dcim@postgres:5432/dcim")
	addr := env.String("OTTER_GO_ADDR", ":8000")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	pgCtx, pgCancel := context.WithTimeout(ctx, 30*time.Second)
	pool, err := pgxpool.New(pgCtx, pgDSN)
	pgCancel()
	if err != nil {
		log.Error("pg_connect_failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	q := dbq.New(pool)
	sh := &sites.Handler{Q: q, Audit: q}
	rh := &regions.Handler{Q: q, Audit: q}
	lh := &locations.Handler{Q: q, Audit: q}
	rkh := &racks.Handler{Q: q, Audit: q}
	ah := &assets.Handler{Q: q, Audit: q}
	ch := &cables.Handler{Q: q, Audit: q}
	ih := &ipam.Handler{Q: q, Audit: q}
	lih := &lir.Handler{Q: q, Audit: q}
	ph := &power.Handler{Q: q, Audit: q}
	bh := &bgp.Handler{Q: q, Audit: q}
	dh := &dns.Handler{Q: q, Audit: q}
	auh := &audit.Handler{Q: q}
	alh := &alerts.Handler{Q: q, Audit: q}
	nh := &notifications.Handler{Q: q, Audit: q}
	coh := &collectors.Handler{Q: q, Audit: q}
	oh := &organization.Handler{Q: q, Audit: q}
	sth := &stencils.Handler{}
	th := &telemetry.Handler{Q: q}
	// Default DNS recursive_upstreams — same shape Python's
	// settings.dns_recursive_upstreams handles (comma-separated env
	// override, hard-coded {1.1.1.1, 8.8.8.8} fallback). Surfaced
	// by GET /admin/system/dns-settings as `default_recursive_upstreams`
	// so the UI can render the reset-to-default affordance.
	dnsDefault := []string{"1.1.1.1", "8.8.8.8"}
	if csv := env.String("DCIM_DNS_RECURSIVE_UPSTREAMS", ""); csv != "" {
		dnsDefault = dnsDefault[:0]
		for _, v := range strings.Split(csv, ",") {
			if v = strings.TrimSpace(v); v != "" {
				dnsDefault = append(dnsDefault, v)
			}
		}
	}
	adh := &admin.Handler{Q: q, Audit: q, DefaultDnsRecursiveUpstreams: dnsDefault}
	srh := &search.Handler{Q: q}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		pingCtx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db unavailable")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Auth: prefer the real JWT-verifying middleware when DCIM_JWT_SECRET
	// is set. Operators who haven't wired Keycloak yet can fall back to
	// the loud-panicking stub by setting OTTER_GO_INSECURE_AUTH_STUB=true
	// (sealed dev environments only).
	jwtSecret := env.String("DCIM_JWT_SECRET", "")
	jwtTTL := env.Int("DCIM_JWT_TTL_SECONDS", 900)
	oidcCfg := auth.OIDCConfig{
		Issuer:       env.String("DCIM_OIDC_ISSUER", ""),
		ClientID:     env.String("DCIM_OIDC_CLIENT_ID", ""),
		ClientSecret: env.String("DCIM_OIDC_CLIENT_SECRET", ""),
		RedirectURI:  env.String("DCIM_OIDC_REDIRECT_URI", ""),
		PublicURL:    env.String("DCIM_OIDC_PUBLIC_URL", ""),
	}
	if csv := env.String("DCIM_MFA_AMR_VALUES", "mfa,otp,hwk"); csv != "" {
		for _, v := range strings.Split(csv, ",") {
			if v = strings.TrimSpace(v); v != "" {
				oidcCfg.MFAAMRValues = append(oidcCfg.MFAAMRValues, v)
			}
		}
	}
	oidcCtx, oidcCancel := context.WithTimeout(ctx, 10*time.Second)
	oidcProvider, err := auth.NewOIDC(oidcCtx, oidcCfg)
	oidcCancel()
	if err != nil {
		log.Error("oidc_init_failed", "err", err)
		os.Exit(1)
	}
	// Fernet for the at-rest IdP refresh_token. Same env var the
	// Python side reads; empty key set disables encryption (plaintext
	// fallback, logged loudly).
	fernetCfg, err := auth.ParseFernetKey(env.String("DCIM_DNS_DNSSEC_SECRET", ""))
	if err != nil {
		log.Error("fernet_init_failed", "err", err)
		os.Exit(1)
	}
	if len(fernetCfg.Keys) == 0 {
		log.Warn("refresh_token_plaintext", "msg", "DCIM_DNS_DNSSEC_SECRET unset; IdP refresh_tokens stored in plaintext")
	}
	authHandler := &auth.Handler{
		Q:      q,
		OIDC:   oidcProvider,
		Mint:   auth.MintConfig{Secret: []byte(jwtSecret), TTLSecond: jwtTTL},
		Fernet: fernetCfg,
		Audit:  q,
	}
	var authMW func(http.Handler) http.Handler
	if jwtSecret != "" {
		// jwt_old_secrets is a CSV of base64-or-plain old keys, matching
		// the Python settings.jwt_old_secrets dict (values only — kid
		// matching ships with PR 36 alongside OIDC).
		var oldSecrets [][]byte
		if csv := env.String("DCIM_JWT_OLD_SECRETS", ""); csv != "" {
			for _, s := range strings.Split(csv, ",") {
				if s = strings.TrimSpace(s); s != "" {
					oldSecrets = append(oldSecrets, []byte(s))
				}
			}
		}
		authMW = auth.Verifying(log, q, auth.VerifierConfig{
			PrimarySecret: []byte(jwtSecret),
			OldSecrets:    oldSecrets,
		})
		log.Info("auth_jwt_enabled", "old_secrets", len(oldSecrets))
	} else {
		authMW = auth.MustStub(log)
	}
	r.Route("/api/v1", func(r chi.Router) {
		// /api/v1/auth/{login,logout,refresh,oidc/*} must remain
		// reachable to unauthenticated browsers — the SPA hits these
		// BEFORE it holds a session. authHandler.Mount(r, authMW)
		// keeps the login flow public and wraps only the protected
		// half (/me, /tokens CRUD) in Verifying. Done first so the
		// public routes register without the middleware that the
		// other subhandlers register below inside r.Group.
		authHandler.Mount(r, authMW)
		// Everything else under /api/v1 requires a verified session.
		r.Group(func(r chi.Router) {
			r.Use(authMW)
			sh.Mount(r)
			rh.Mount(r)
			lh.Mount(r)
			rkh.Mount(r)
			ah.Mount(r)
			ch.Mount(r)
			ih.Mount(r)
			lih.Mount(r)
			ph.Mount(r)
			bh.Mount(r)
			dh.Mount(r)
			auh.Mount(r)
			alh.Mount(r)
			nh.Mount(r)
			coh.Mount(r)
			oh.Mount(r)
			sth.Mount(r)
			th.Mount(r)
			adh.Mount(r)
			srh.Mount(r)
		})
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("otter_go_listen", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen_failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutdown_initiated")
	shCtx, shCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shCancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Error("shutdown_error", "err", err)
	}
}
