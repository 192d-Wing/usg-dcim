// Go port of Python's DHCP scope read surface (api/ipam.py:1945,
// 2069). LIST + GET only — the four mutation endpoints (POST/PATCH/
// DELETE/RESTORE) ship in a follow-up PR.
//
//   GET /api/v1/ipam/dhcp/servers/{id}/scopes  paginated list per server
//   GET /api/v1/ipam/dhcp/scopes/{id}          single scope
//
// LIST filters mirror Python's list_dhcp_scopes (ip_family, enabled,
// diff_status, include_deleted). ABAC: the LIST endpoint enforces
// fabric scope on the server's fabric_id (matches Python's
// _enforce_scope_via_server gate); GET enforces on the scope's
// transitive fabric_id via the existing 2-hop GetDhcpScopeFabricID.
package ipam

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type dhcpScopesPage = httpx.Page[dhcpScopeOut]

// dhcpScopeOut mirrors Python's DhcpScopeOut (schemas/ipam.py:434).
// Same wire-shape pattern as scope-templates (PR 8): the dbq row's
// `*_json` JSON columns are renamed to their Python-canonical
// counterparts (pools / pd_pools / options / reservations /
// last_diff_delta) so finch/operator clients round-trip without a
// field-name remap. Timestamps use the project's standard Python-
// isoformat shape.
type dhcpScopeOut struct {
	ID                       uuid.UUID    `json:"id"`
	DhcpServerID             uuid.UUID    `json:"dhcp_server_id"`
	SubnetID                 *uuid.UUID   `json:"subnet_id"`
	TemplateID               *uuid.UUID   `json:"template_id"`
	Name                     string       `json:"name"`
	IPFamily                 int32        `json:"ip_family"`
	Prefix                   string       `json:"prefix"`
	Pools                    rawJSONArray `json:"pools"`
	PdPools                  rawJSON      `json:"pd_pools"`
	Options                  rawJSONArray `json:"options"`
	Reservations             rawJSONArray `json:"reservations"`
	ValidLifetimeSeconds     *int32       `json:"valid_lifetime_seconds"`
	RenewTimerSeconds        *int32       `json:"renew_timer_seconds"`
	RebindTimerSeconds       *int32       `json:"rebind_timer_seconds"`
	PreferredLifetimeSeconds *int32       `json:"preferred_lifetime_seconds"`
	Enabled                  bool         `json:"enabled"`
	Description              *string      `json:"description"`
	KeaSubnetID              *int32       `json:"kea_subnet_id"`
	LastDiffAt               *string      `json:"last_diff_at"`
	LastDiffStatus           *string      `json:"last_diff_status"`
	LastDiffDelta            rawJSON      `json:"last_diff_delta"`
	AutoPushOverride         *bool        `json:"auto_push_override"`
	DeletedAt                *string      `json:"deleted_at"`
	CreatedAt                string       `json:"created_at"`
	UpdatedAt                string       `json:"updated_at"`
}

// rawJSON is a json.RawMessage that JSON-encodes as null when empty.
// Used for genuinely nullable JSONB columns (pd_pools_json, last_
// diff_delta_json) where Python emits None too.
type rawJSON []byte

func (r rawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

// rawJSONArray is used for JSONB columns that Python coerces to []
// via `column_json or []` (pools_json, options_json, reservations_
// json — see Python's models/ipam.py:625, :633, :637). The schema
// declares them NOT NULL with default=list, but legacy/manual rows
// can still carry NULL; this preserves Python's "[]" wire shape so
// downstream code never has to switch on null vs empty array.
type rawJSONArray []byte

func (r rawJSONArray) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("[]"), nil
	}
	return r, nil
}

// pythonISOTZ is shared with the drift-summary aggregator + scope-
// template handlers (PR 8/9 established the convention). UTC →
// "+00:00" to match Python's datetime.isoformat() for tz-aware
// datetimes.
const scopeISOTZ = "2006-01-02T15:04:05.000000-07:00"

func toDhcpScopeOut(s dbq.GetDhcpScopeRow) dhcpScopeOut {
	out := dhcpScopeOut{
		ID:                       s.ID,
		DhcpServerID:             s.DhcpServerID,
		SubnetID:                 s.SubnetID,
		TemplateID:               s.TemplateID,
		Name:                     s.Name,
		IPFamily:                 s.IPFamily,
		Prefix:                   s.Prefix,
		Pools:                    rawJSONArray(s.PoolsJSON),
		PdPools:                  rawJSON(s.PdPoolsJSON),
		Options:                  rawJSONArray(s.OptionsJSON),
		Reservations:             rawJSONArray(s.ReservationsJSON),
		ValidLifetimeSeconds:     s.ValidLifetimeSeconds,
		RenewTimerSeconds:        s.RenewTimerSeconds,
		RebindTimerSeconds:       s.RebindTimerSeconds,
		PreferredLifetimeSeconds: s.PreferredLifetimeSeconds,
		Enabled:                  s.Enabled,
		Description:              s.Description,
		KeaSubnetID:              s.KeaSubnetID,
		LastDiffStatus:           s.LastDiffStatus,
		LastDiffDelta:            rawJSON(s.LastDiffDeltaJSON),
		AutoPushOverride:         s.AutoPushOverride,
		CreatedAt:                s.CreatedAt.UTC().Format(scopeISOTZ),
		UpdatedAt:                s.UpdatedAt.UTC().Format(scopeISOTZ),
	}
	if s.LastDiffAt != nil {
		t := s.LastDiffAt.UTC().Format(scopeISOTZ)
		out.LastDiffAt = &t
	}
	if s.DeletedAt != nil {
		t := s.DeletedAt.UTC().Format(scopeISOTZ)
		out.DeletedAt = &t
	}
	return out
}

const errDhcpScopeNotFoundCRUD = "dhcp scope not found"

// listDhcpScopes mirrors Python's list_dhcp_scopes at
// api/ipam.py:1945. The {server_id} URL param scopes the list to one
// DhcpServer; the optional query knobs match Python's signature
// exactly so client query strings travel unchanged across the cutover.
func (h *Handler) listDhcpScopes(w http.ResponseWriter, r *http.Request) {
	serverID, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpServerFabric(w, r, serverID, "ipam:dhcp-scopes:read") {
		return
	}
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	params := dbq.ListDhcpScopesByServerParams{
		Limit:          limit,
		Offset:         offset,
		DhcpServerID:   serverID,
		IncludeDeleted: parseBoolQuery(q.Get("include_deleted")),
	}
	if v := q.Get("ip_family"); v != "" {
		fam, err := parseIPFamilyParam(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		params.IPFamily = &fam
	}
	if v := q.Get("enabled"); v != "" {
		b, ok := parseBoolStrict(v)
		if !ok {
			httpx.Error(w, http.StatusBadRequest, "enabled must be true or false")
			return
		}
		params.Enabled = &b
	}
	if v := q.Get("diff_status"); v != "" {
		if !isValidDiffStatus(v) {
			httpx.Error(w, http.StatusBadRequest,
				"diff_status must be one of: drifted, error, in_sync, missing_from_kea, never_pushed")
			return
		}
		params.DiffStatus = &v
	}
	items, err := h.Q.ListDhcpScopesByServer(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDhcpScopesByServer(r.Context(), dbq.CountDhcpScopesByServerParams{
		DhcpServerID:   serverID,
		IncludeDeleted: params.IncludeDeleted,
		IPFamily:       params.IPFamily,
		Enabled:        params.Enabled,
		DiffStatus:     params.DiffStatus,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	out := make([]dhcpScopeOut, len(items))
	for i, s := range items {
		out[i] = toDhcpScopeOut(dbq.GetDhcpScopeRow(s))
	}
	httpx.JSON(w, http.StatusOK, dhcpScopesPage{Items: out, Total: total, Limit: limit, Offset: offset})
}

// getDhcpScope mirrors Python's get_dhcp_scope at api/ipam.py:2069.
// Returns soft-deleted scopes too (the response carries deleted_at);
// client decides whether to call restore. ABAC via the 2-hop
// GetDhcpScopeFabricID lookup the per-scope push/diff endpoints
// already use.
func (h *Handler) getDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpScopeFabric(w, r, id, "ipam:dhcp-scopes:read") {
		return
	}
	s, err := h.Q.GetDhcpScope(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
	httpx.JSON(w, http.StatusOK, toDhcpScopeOut(s))
}

// parseBoolStrict rejects everything except "true"/"false"/"1"/"0" so
// `?enabled=yes` lands as 400 instead of silently defaulting. Note:
// Python's Pydantic v2 bool parser accepts more forms (`yes`/`on`/
// case-insensitive `True`); the divergence is intentional — the
// LIST endpoint is documented (Pydantic Query[bool]) to use only
// the canonical four values, so cutover clients sending anything
// else were relying on undocumented coercion.
func parseBoolStrict(s string) (bool, bool) {
	switch s {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// parseBoolQuery interprets ?include_deleted=... the same way Python's
// FastAPI Query[bool] does: missing or empty → false; "true"/"1" →
// true; anything else → false (forgiving — Python's pydantic v2
// bool parser also accepts "yes"/"on", so this is slightly stricter
// in spirit but not in the cutover wire contract).
func parseBoolQuery(s string) bool {
	b, _ := strconv.ParseBool(s)
	return b
}

// isValidDiffStatus mirrors Python's enum check at api/ipam.py:1971.
// The five values match diff.Status constants.
func isValidDiffStatus(s string) bool {
	switch s {
	case "in_sync", "drifted", "missing_from_kea", "never_pushed", "error":
		return true
	}
	return false
}

