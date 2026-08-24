// Package httpx is small JSON + error-mapping glue. Keeping it thin
// so handlers stay readable.
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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

// DecodeJSON decodes the request body into dst. On failure it writes
// 400 {"detail": "invalid request body: <decode error>"} and returns
// false so handlers can short-circuit:
//
//	var req createReq
//	if !httpx.DecodeJSON(w, r, &req) { return }
//
// The decode error's own text is surfaced because it names the actual
// problem (e.g. `json: cannot unmarshal number into Go struct field
// createReq.length_m of type string`). The old pattern — folding the
// decode error into the handler's field-validation branch — produced
// misleading 400s: a wire-type mismatch on length_m surfaced as
// "a_asset_id and b_asset_id required". encoding/json error strings
// are short and carry no request payload, so echoing them is bounded
// and safe. Field-presence validation stays in the handler, after
// this returns true.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		Error(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
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

// WriteMapped translates err through Mapped and writes it — the
// shorthand several handler packages had grown locally (assets,
// racks, locations).
func WriteMapped(w http.ResponseWriter, err error) {
	status, msg := Mapped(err)
	Error(w, status, msg)
}

// IDParam pulls the {id} chi route param, writing a 400 when it
// isn't a uuid. ok=false means the response has already been
// written.
func IDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}
