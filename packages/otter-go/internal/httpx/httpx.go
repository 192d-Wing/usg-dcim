// Package httpx is small JSON + error-mapping glue. Keeping it thin
// so handlers stay readable.
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// JSON writes the value as JSON with the given status. Sets the
// Content-Type header even on empty bodies so curl doesn't sniff.
func JSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

// Error writes a {"detail": "..."} body matching the FastAPI shape so
// finch can keep using the same error parser during the migration.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"detail": msg})
}

// MapPGError converts pgx sentinel errors to HTTP status codes.
// Returns (status, message). Use Mapped() to short-circuit a handler:
//
//	if err != nil { status, msg := httpx.Mapped(err); httpx.Error(w, status, msg); return }
// ErrOutsideScope is the ABAC denial sentinel. Lives in httpx so
// Mapped can errors.Is-match it without importing internal/auth
// (auth → httpx is the existing direction; reversing it would cycle).
// internal/auth re-exports this as auth.ErrOutsideScope so existing
// callers keep working.
var ErrOutsideScope = errors.New("resource is outside your scope")

// ErrFKViolation is the FK-violation sentinel for DELETE handlers
// rejecting rows that still have children (e.g. a building with rooms).
// Maps to 409 Conflict so finch can distinguish "you can't delete this
// because it's referenced" from generic 5xx.
var ErrFKViolation = errors.New("resource has dependent rows")

// ErrUniqueViolation is the unique-constraint sentinel for INSERT
// handlers rejecting duplicates (e.g. a second notification channel
// with the same name). Python's mutation handlers pre-fetched by
// name and returned 409 with a hand-rolled message; Go relies on
// the DB unique constraint and surfaces this sentinel via
// httpx.Mapped's SQLSTATE 23505 path so the response is 409 instead
// of the default 500.
var ErrUniqueViolation = errors.New("resource already exists")

func Mapped(err error) (int, string) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return http.StatusNotFound, "not found"
	case errors.Is(err, ErrOutsideScope):
		return http.StatusForbidden, err.Error()
	case errors.Is(err, ErrFKViolation) || isPgFKViolation(err):
		return http.StatusConflict, err.Error()
	case errors.Is(err, ErrUniqueViolation) || isPgUniqueViolation(err):
		return http.StatusConflict, err.Error()
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

// isPgFKViolation matches Postgres SQLSTATE 23503 (foreign_key_violation)
// so DELETE handlers don't need to wrap pgx errors by hand.
func isPgFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isPgUniqueViolation matches Postgres SQLSTATE 23505 (unique_violation)
// so INSERT handlers don't need to wrap pgx errors by hand.
func isPgUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
