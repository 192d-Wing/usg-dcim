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
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
	"github.com/usg-dcim/packages/otter-go/internal/regions"
	"github.com/usg-dcim/packages/otter-go/internal/sites"
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
	sh := &sites.Handler{Q: q}
	rh := &regions.Handler{Q: q}

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

	// Auth is the stub middleware — refuses to start unless
	// OTTER_GO_INSECURE_AUTH_STUB is truthy. Replace with the real
	// OIDC+capability middleware before promoting otter-go past Phase 1.
	authMW := auth.MustStub(log)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)
		sh.Mount(r)
		rh.Mount(r)
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
