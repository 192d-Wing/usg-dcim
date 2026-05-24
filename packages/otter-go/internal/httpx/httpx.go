// Package httpx is small JSON + error-mapping glue. Keeping it thin
// so handlers stay readable.
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
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
func Mapped(err error) (int, string) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return http.StatusNotFound, "not found"
	default:
		return http.StatusInternalServerError, "internal error"
	}
}
