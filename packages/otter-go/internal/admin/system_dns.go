package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
	"github.com/jackc/pgx/v5"
)

// systemKeyDnsRecursiveUpstreams matches Python's
// services.dns._SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS — the system_settings.key
// the override is stored under. Both stacks read and write this same row.
const systemKeyDnsRecursiveUpstreams = "dns_recursive_upstreams"

// systemDnsSettingsOut is the wire shape returned by GET + PUT. Mirrors
// Python's SystemDnsSettingsOut (api/admin.py:59) byte-for-byte:
//
//   - recursive_upstreams           — the effective list (DB override
//                                     if set, else env default)
//   - override_active               — true iff a non-empty DB row exists
//   - default_recursive_upstreams   — the env-backed default, surfaced
//                                     separately so the UI can render
//                                     "current vs default" + a reset CTA
//   - updated_at                    — set when an override row exists,
//                                     omitted otherwise
type systemDnsSettingsOut struct {
	RecursiveUpstreams        []string   `json:"recursive_upstreams"`
	OverrideActive            bool       `json:"override_active"`
	DefaultRecursiveUpstreams []string   `json:"default_recursive_upstreams"`
	UpdatedAt                 *time.Time `json:"updated_at"`
}

// systemDnsSettingsUpdate is the PUT request body. Both null and empty
// list clear the override (Python parity) — handled by normalizeUpstreams
// returning nil in either case so the reset path runs once.
type systemDnsSettingsUpdate struct {
	RecursiveUpstreams []string `json:"recursive_upstreams"`
}

func (h *Handler) getSystemDnsSettings(w http.ResponseWriter, r *http.Request) {
	row, found, err := h.loadDnsOverride(r.Context())
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	overrideActive, effective := h.effectiveUpstreams(row, found)
	out := systemDnsSettingsOut{
		RecursiveUpstreams:        effective,
		OverrideActive:            overrideActive,
		DefaultRecursiveUpstreams: h.defaultUpstreamsCopy(),
	}
	if found {
		t := row.UpdatedAt
		out.UpdatedAt = &t
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) putSystemDnsSettings(w http.ResponseWriter, r *http.Request) {
	var payload systemDnsSettingsUpdate
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	normalized := normalizeUpstreams(payload.RecursiveUpstreams)

	var (
		action   string
		metadata map[string]any
		updated  *time.Time
	)
	if normalized == nil {
		// Reset path. Idempotent: DELETE is a no-op when the row is
		// already absent, so a "reset → reset" sequence doesn't fail.
		if err := h.Q.DeleteSystemSetting(r.Context(), systemKeyDnsRecursiveUpstreams); err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
		action = "system_dns_upstreams.reset"
		metadata = map[string]any{}
	} else {
		blob, err := json.Marshal(normalized)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, "encode upstreams")
			return
		}
		if err := h.Q.UpsertSystemSetting(r.Context(), dbq.UpsertSystemSettingParams{
			Key:   systemKeyDnsRecursiveUpstreams,
			Value: blob,
		}); err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
		action = "system_dns_upstreams.update"
		metadata = map[string]any{"upstreams": normalized}
		// Re-read so the response carries the server-stamped updated_at,
		// which matches Python's `row.updated_at if normalized is not None
		// and row is not None else None` semantics.
		row, found, err := h.loadDnsOverride(r.Context())
		if err == nil && found {
			t := row.UpdatedAt
			updated = &t
		}
	}

	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     action,
		TargetType: "system_setting",
		TargetID:   systemKeyDnsRecursiveUpstreams,
		Metadata:   metadata,
	})

	out := systemDnsSettingsOut{
		OverrideActive:            normalized != nil,
		DefaultRecursiveUpstreams: h.defaultUpstreamsCopy(),
		UpdatedAt:                 updated,
	}
	if normalized != nil {
		out.RecursiveUpstreams = normalized
	} else {
		out.RecursiveUpstreams = h.defaultUpstreamsCopy()
	}
	httpx.JSON(w, http.StatusOK, out)
}

// loadDnsOverride reads the system_settings row for the DNS recursive
// upstreams key. found=false when the row is absent (pgx.ErrNoRows) —
// callers treat that as "no override, fall back to default."
func (h *Handler) loadDnsOverride(ctx context.Context) (dbq.SystemSetting, bool, error) {
	row, err := h.Q.GetSystemSetting(ctx, systemKeyDnsRecursiveUpstreams)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dbq.SystemSetting{}, false, nil
		}
		return dbq.SystemSetting{}, false, err
	}
	return row, true, nil
}

// effectiveUpstreams resolves the DB-or-env layered value the same way
// Python's services.dns.get_system_dns_upstreams does: a non-empty list
// in the DB row wins; an absent row OR a row whose value isn't a
// non-empty list falls back to the env-backed default.
func (h *Handler) effectiveUpstreams(row dbq.SystemSetting, found bool) (overrideActive bool, effective []string) {
	if found {
		var parsed []string
		if err := json.Unmarshal(row.Value, &parsed); err == nil && len(parsed) > 0 {
			return true, parsed
		}
	}
	return false, h.defaultUpstreamsCopy()
}

// defaultUpstreamsCopy returns a fresh copy of the env-backed default
// so the slice baked into the Handler isn't aliased into a response (or
// later mutated by a caller). Cheap — single-digit elements typically.
func (h *Handler) defaultUpstreamsCopy() []string {
	out := make([]string, len(h.DefaultDnsRecursiveUpstreams))
	copy(out, h.DefaultDnsRecursiveUpstreams)
	return out
}

// normalizeUpstreams mirrors Python's _normalize_upstreams: strip
// whitespace, drop empty entries, dedupe while preserving operator
// order (so the rendered Corefile honors the order the admin picked).
// A nil input or an all-empty input collapses to nil so the caller can
// treat "clear override" as a single case.
func normalizeUpstreams(incoming []string) []string {
	if incoming == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(incoming))
	out := make([]string, 0, len(incoming))
	for _, item := range incoming {
		stripped := strings.TrimSpace(item)
		if stripped == "" {
			continue
		}
		if _, ok := seen[stripped]; ok {
			continue
		}
		seen[stripped] = struct{}{}
		out = append(out, stripped)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
