// Go port of Python's DHCP scope mutation surface
// (api/ipam.py:1986 / 2084 / 2165 / 2212). Four endpoints:
//
//   POST   /api/v1/ipam/dhcp/servers/{server_id}/scopes  CREATE
//   PATCH  /api/v1/ipam/dhcp/scopes/{scope_id}           UPDATE (partial)
//   DELETE /api/v1/ipam/dhcp/scopes/{scope_id}           soft-delete
//   POST   /api/v1/ipam/dhcp/scopes/{scope_id}/restore   undo soft-delete
//
// ABAC: every endpoint enforces fabric scope on the scope's server.
// CREATE uses GetDhcpServerFabricID (URL carries server_id directly);
// UPDATE/DELETE/RESTORE pre-fetch the scope row and enforce on its
// dhcp_server_id via the existing 1-hop server-fabric lookup.
//
// Python parity:
//   - ip_family + prefix + dhcp_server_id are immutable post-create
//     (PATCH payload model omits them); the SQL UPDATE doesn't accept
//     them either.
//   - v4 scopes cannot have pd_pools or preferred_lifetime; rejected
//     at the API layer.
//   - Template must exist + family must match the scope's family on
//     both CREATE and the PATCH-reassign branch.
//   - DELETE runs delete_scope_from_kea best-effort BEFORE tombstoning
//     the row. Kea failures don't abort the DB write — operators
//     would rather see the tombstone and re-clean later than have a
//     row they can't manage because Kea is unreachable.
//   - RESTORE is :delete-capability (the operator who could remove
//     should restore).
//
// Deferred (called out in /code-review on PR 8; same gap here): the
// per-scope auto-push fanout via auto_push_scope_in_background and
// the bundle rerender background-task enqueue. The Go scheduler's
// dhcp_bundle_rerender cron runs every 2 min, so cache freshness is
// bounded; auto-push fanout needs the dhcp_servers.auto_push column
// in the Go model + a per-scope background-task primitive. Until
// then, operators using auto_push must manually run /push after a
// scope mutation.
package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/push"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// dhcpScopeCreateReq mirrors Python's DhcpScopeCreate
// (schemas/ipam.py:410). Inherits from DhcpScopeBase: name, ip_family,
// prefix, optional FKs, default-list JSON columns, optional timers,
// enabled (default true).
type dhcpScopeCreateReq struct {
	DhcpServerID             uuid.UUID       `json:"dhcp_server_id"`
	SubnetID                 *uuid.UUID      `json:"subnet_id"`
	TemplateID               *uuid.UUID      `json:"template_id"`
	Name                     string          `json:"name"`
	IPFamily                 int32           `json:"ip_family"`
	Prefix                   string          `json:"prefix"`
	Pools                    json.RawMessage `json:"pools"`
	PdPools                  json.RawMessage `json:"pd_pools"`
	Options                  json.RawMessage `json:"options"`
	Reservations             json.RawMessage `json:"reservations"`
	ValidLifetimeSeconds     *int32          `json:"valid_lifetime_seconds"`
	RenewTimerSeconds        *int32          `json:"renew_timer_seconds"`
	RebindTimerSeconds       *int32          `json:"rebind_timer_seconds"`
	PreferredLifetimeSeconds *int32          `json:"preferred_lifetime_seconds"`
	Enabled                  *bool           `json:"enabled"`
	Description              *string         `json:"description"`
	AutoPushOverride         *bool           `json:"auto_push_override"`
}

// createDhcpScope mirrors api/ipam.py:1986. The URL server_id MUST
// match payload.dhcp_server_id (Python at line 1993); otherwise 400.
func (h *Handler) createDhcpScope(w http.ResponseWriter, r *http.Request) {
	serverID, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpServerFabric(w, r, serverID, "ipam:dhcp-scopes:create") {
		return
	}
	var req dhcpScopeCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if msg, ok := validateCreatePayload(&req, serverID); !ok {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	if !h.preflightCreateFKs(w, r, &req) {
		return
	}
	params := dbq.CreateDhcpScopeParams{
		DhcpServerID:             req.DhcpServerID,
		SubnetID:                 req.SubnetID,
		TemplateID:               req.TemplateID,
		Name:                     req.Name,
		IPFamily:                 req.IPFamily,
		Prefix:                   req.Prefix,
		PoolsJSON:                defaultEmptyArray(req.Pools),
		PdPoolsJSON:              req.PdPools, // nil → SQL NULL (v6 optional)
		OptionsJSON:              defaultEmptyArray(req.Options),
		ReservationsJSON:         defaultEmptyArray(req.Reservations),
		ValidLifetimeSeconds:     req.ValidLifetimeSeconds,
		RenewTimerSeconds:        req.RenewTimerSeconds,
		RebindTimerSeconds:       req.RebindTimerSeconds,
		PreferredLifetimeSeconds: req.PreferredLifetimeSeconds,
		Enabled:                  defaultTrue(req.Enabled),
		Description:              req.Description,
		AutoPushOverride:         req.AutoPushOverride,
	}
	out, err := h.Q.CreateDhcpScope(r.Context(), params)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope.create",
		TargetType: "dhcp_scope", TargetID: out.ID.String(),
		Metadata: map[string]any{
			"dhcp_server_id": serverID.String(),
			"ip_family":      req.IPFamily,
			"prefix":         req.Prefix,
		},
	})
	httpx.JSON(w, http.StatusCreated, toDhcpScopeOut(out))
}

// dhcpScopeUpdateReq is the PATCH payload. Every nullable column
// carries a `<field>Set bool` so the JSON "key omitted vs key
// present with null" distinction reaches the SQL CASE expressions.
// ip_family + prefix + dhcp_server_id are NOT in the struct — they're
// immutable post-create.
type dhcpScopeUpdateReq struct {
	Name                     *string
	SubnetID                 *uuid.UUID
	subnetIDSet              bool
	TemplateID               *uuid.UUID
	templateIDSet            bool
	Pools                    json.RawMessage
	poolsSet                 bool
	PdPools                  json.RawMessage
	pdPoolsSet               bool
	Options                  json.RawMessage
	optionsSet               bool
	Reservations             json.RawMessage
	reservationsSet          bool
	ValidLifetimeSeconds     *int32
	validLifetimeSet         bool
	RenewTimerSeconds        *int32
	renewTimerSet            bool
	RebindTimerSeconds       *int32
	rebindTimerSet           bool
	PreferredLifetimeSeconds *int32
	preferredLifetimeSet     bool
	Enabled                  *bool
	Description              *string
	descriptionSet           bool
	AutoPushOverride         *bool
	autoPushOverrideSet      bool
}

func (u *dhcpScopeUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		if string(v) == "null" {
			return errNullName
		}
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["subnet_id"]; ok {
		u.subnetIDSet = true
		_ = json.Unmarshal(v, &u.SubnetID)
	}
	if v, ok := raw["template_id"]; ok {
		u.templateIDSet = true
		_ = json.Unmarshal(v, &u.TemplateID)
	}
	u.poolsSet = unmarshalRawJSON(raw, "pools", &u.Pools)
	u.pdPoolsSet = unmarshalRawJSON(raw, "pd_pools", &u.PdPools)
	u.optionsSet = unmarshalRawJSON(raw, "options", &u.Options)
	u.reservationsSet = unmarshalRawJSON(raw, "reservations", &u.Reservations)
	if v, ok := raw["valid_lifetime_seconds"]; ok {
		u.validLifetimeSet = true
		_ = json.Unmarshal(v, &u.ValidLifetimeSeconds)
	}
	if v, ok := raw["renew_timer_seconds"]; ok {
		u.renewTimerSet = true
		_ = json.Unmarshal(v, &u.RenewTimerSeconds)
	}
	if v, ok := raw["rebind_timer_seconds"]; ok {
		u.rebindTimerSet = true
		_ = json.Unmarshal(v, &u.RebindTimerSeconds)
	}
	if v, ok := raw["preferred_lifetime_seconds"]; ok {
		u.preferredLifetimeSet = true
		_ = json.Unmarshal(v, &u.PreferredLifetimeSeconds)
	}
	if v, ok := raw["enabled"]; ok {
		_ = json.Unmarshal(v, &u.Enabled)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	if v, ok := raw["auto_push_override"]; ok {
		u.autoPushOverrideSet = true
		_ = json.Unmarshal(v, &u.AutoPushOverride)
	}
	return nil
}

// unmarshalRawJSON populates a json.RawMessage destination from the
// raw[key], returning true when the key was present. Used for the
// four JSONB columns where presence (not value) drives the SQL
// CASE branch. A null value yields an empty RawMessage; the handler
// later substitutes [] (or NULL for pd_pools) at the SQL boundary.
func unmarshalRawJSON(raw map[string]json.RawMessage, key string, dst *json.RawMessage) bool {
	v, ok := raw[key]
	if !ok {
		return false
	}
	if string(v) != "null" {
		*dst = v
	}
	return true
}

// updateDhcpScope mirrors api/ipam.py:2084.
func (h *Handler) updateDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	existing, ok := h.loadScopeForMutation(w, r, id, "ipam:dhcp-scopes:update")
	if !ok {
		return
	}
	var req dhcpScopeUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, errNullName) {
			httpx.Error(w, http.StatusBadRequest, errNullName.Error())
			return
		}
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if msg, ok := validateUpdatePayload(&req, existing.IPFamily); !ok {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}
	if !h.preflightUpdateFKs(w, r, &req, existing.IPFamily) {
		return
	}
	params := buildDhcpScopeUpdateParams(id, &req)
	out, err := h.Q.UpdateDhcpScope(r.Context(), params)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope.update",
		TargetType: "dhcp_scope", TargetID: id.String(),
		Diff: buildDhcpScopeAuditDiff(&req),
	})
	httpx.JSON(w, http.StatusOK, toDhcpScopeOut(out))
}

// buildDhcpScopeUpdateParams resolves the JSONB null-vs-omitted
// semantics into SQL params. pools/options/reservations clear to []
// when the patch passes null; pd_pools clears to NULL (the v6-only
// column is genuinely nullable). This split mirrors Python at
// api/ipam.py:2140-2141.
func buildDhcpScopeUpdateParams(id uuid.UUID, req *dhcpScopeUpdateReq) dbq.UpdateDhcpScopeParams {
	return dbq.UpdateDhcpScopeParams{
		ID:                       id,
		Name:                     req.Name,
		SubnetIDSet:              req.subnetIDSet,
		SubnetID:                 req.SubnetID,
		TemplateIDSet:            req.templateIDSet,
		TemplateID:               req.TemplateID,
		PoolsSet:                 req.poolsSet,
		PoolsJSON:                resolveJSONArrayClear(req.poolsSet, req.Pools),
		PdPoolsSet:               req.pdPoolsSet,
		PdPoolsJSON:              req.PdPools, // genuinely nullable; nil OK
		OptionsSet:               req.optionsSet,
		OptionsJSON:              resolveJSONArrayClear(req.optionsSet, req.Options),
		ReservationsSet:          req.reservationsSet,
		ReservationsJSON:         resolveJSONArrayClear(req.reservationsSet, req.Reservations),
		ValidLifetimeSet:         req.validLifetimeSet,
		ValidLifetimeSeconds:     req.ValidLifetimeSeconds,
		RenewTimerSet:            req.renewTimerSet,
		RenewTimerSeconds:        req.RenewTimerSeconds,
		RebindTimerSet:           req.rebindTimerSet,
		RebindTimerSeconds:       req.RebindTimerSeconds,
		PreferredLifetimeSet:     req.preferredLifetimeSet,
		PreferredLifetimeSeconds: req.PreferredLifetimeSeconds,
		Enabled:                  req.Enabled,
		DescriptionSet:           req.descriptionSet,
		Description:              req.Description,
		AutoPushOverrideSet:      req.autoPushOverrideSet,
		AutoPushOverride:         req.AutoPushOverride,
	}
}

// resolveJSONArrayClear: when the caller said `"<col>": null` (set=true,
// value empty), Python writes [] to the column; when omitted (set=false),
// the SQL CASE keeps the current value and this byte slice is ignored.
// When set with a non-null value, pass through.
func resolveJSONArrayClear(set bool, raw json.RawMessage) json.RawMessage {
	if set && len(raw) == 0 {
		return json.RawMessage(`[]`)
	}
	return raw
}

// deleteDhcpScope mirrors api/ipam.py:2165. Best-effort Kea cleanup
// via push.DeleteScopeFromKea, then UPDATE deleted_at = NOW(). Kea
// failures stay in the audit but DO NOT abort the DB tombstone —
// refusing to soft-delete because Kea is unreachable strands
// orphaned Kea config that DCIM can't subsequently manage.
func (h *Handler) deleteDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	existing, ok := h.loadScopeForMutation(w, r, id, "ipam:dhcp-scopes:delete")
	if !ok {
		return
	}
	if existing.DeletedAt != nil {
		// Python at line 2173 collapses "missing" and "already-deleted"
		// into the same 404. Operators use ?include_deleted=true on
		// LIST + POST /restore for the tombstoned workflow.
		httpx.Error(w, http.StatusNotFound, errDhcpScopeNotFoundCRUD)
		return
	}
	keaResult, keaErr := push.DeleteScopeFromKea(r.Context(), h.Q, h.pushKeaBuilder(), id)
	if keaErr != nil {
		// DeleteScopeFromKea's non-nil err is reserved for fatal
		// infrastructure failures (DB unreachable, history insert
		// errored) — distinct from a Kea-side failure which lands
		// as Result.Status="error" with no err. Refusing to
		// tombstone here matches Python's posture (the exception
		// raises and FastAPI returns 500); otherwise the audit row
		// would record an empty kea_delete_status and operators
		// would lose the DB-error trail.
		status, msg := httpx.Mapped(keaErr)
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.SoftDeleteDhcpScope(r.Context(), id); err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope.delete",
		TargetType: "dhcp_scope", TargetID: id.String(),
		Metadata: map[string]any{
			"dhcp_server_id":    existing.DhcpServerID.String(),
			"ip_family":         existing.IPFamily,
			"prefix":            existing.Prefix,
			"kea_subnet_id":     existing.KeaSubnetID,
			"kea_delete_status": string(keaResult.Status),
			"kea_delete_error":  nilIfEmpty(keaResult.Error),
			"soft_delete":       true,
		},
	})
	w.WriteHeader(http.StatusNoContent)
}

// restoreDhcpScope mirrors api/ipam.py:2212. Clears deleted_at; does
// NOT re-push to Kea (operator runs POST /push explicitly to put
// the subnet back). Capability is :delete (same operator class).
func (h *Handler) restoreDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	existing, ok := h.loadScopeForMutation(w, r, id, "ipam:dhcp-scopes:delete")
	if !ok {
		return
	}
	if existing.DeletedAt == nil {
		httpx.Error(w, http.StatusBadRequest, "scope is not soft-deleted; nothing to restore")
		return
	}
	out, err := h.Q.RestoreDhcpScope(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope.restore",
		TargetType: "dhcp_scope", TargetID: id.String(),
		Metadata: map[string]any{
			"dhcp_server_id": existing.DhcpServerID.String(),
			"ip_family":      existing.IPFamily,
			"prefix":         existing.Prefix,
		},
	})
	httpx.JSON(w, http.StatusOK, toDhcpScopeOut(out))
}

// loadScopeForMutation pre-fetches the scope row + enforces fabric
// scope on its dhcp_server_id. Shared by PATCH/DELETE/RESTORE.
// Returns (existing, false) and writes the response on missing /
// unauthorized.
func (h *Handler) loadScopeForMutation(w http.ResponseWriter, r *http.Request, id uuid.UUID, capCode string) (dbq.DhcpScope, bool) {
	existing, err := h.Q.GetDhcpScope(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeNotFoundCRUD)
		return dbq.DhcpScope{}, false
	}
	if !h.enforceDhcpServerFabric(w, r, existing.DhcpServerID, capCode) {
		return dbq.DhcpScope{}, false
	}
	return existing, true
}

// validateCreatePayload checks the shape-level invariants Python's
// DhcpScopeCreate Pydantic model enforces. Returns (errorMessage,
// false) on the first violation. Pulled out of createDhcpScope to
// keep the orchestrator under SonarCloud's cognitive-complexity
// ceiling.
func validateCreatePayload(req *dhcpScopeCreateReq, urlServerID uuid.UUID) (string, bool) {
	if req.DhcpServerID != urlServerID {
		return "payload.dhcp_server_id must match URL server_id", false
	}
	if req.Name == "" {
		return "name required", false
	}
	if req.IPFamily != 4 && req.IPFamily != 6 {
		return "ip_family must be 4 or 6", false
	}
	if req.Prefix == "" {
		return "prefix required", false
	}
	if req.IPFamily == 4 {
		if len(req.PdPools) > 0 && string(req.PdPools) != "null" {
			return "pd_pools is v6-only", false
		}
		if req.PreferredLifetimeSeconds != nil {
			return errPreferredV6Only, false
		}
	}
	if msg, ok := validateReservationsAgainstFamily(req.Reservations, req.IPFamily); !ok {
		return msg, false
	}
	return "", true
}

// validateReservationsAgainstFamily ports Python's
// _validate_reservations_against_family (api/ipam.py:1884). Each
// reservation must declare exactly one identifier matching the
// scope's family: v4 → `mac`, v6 → `duid`. Mixing them yields
// invalid Kea config and the PR 88/94 reconcile silently drops
// the wrong identifier — reject at the API instead.
func validateReservationsAgainstFamily(raw json.RawMessage, ipFamily int32) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return "", true
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		// Malformed reservations array — caller-facing 400 with the
		// decoder error message rather than the generic "invalid
		// json" since the OUTER payload decoded fine.
		return "reservations must be a JSON array of objects", false
	}
	for _, e := range entries {
		if msg, ok := validateReservationEntry(e, ipFamily); !ok {
			return msg, false
		}
	}
	return "", true
}

// validateReservationEntry checks one reservation against the
// scope's family. Split out so validateReservationsAgainstFamily
// stays under SonarCloud's cognitive-complexity ceiling.
func validateReservationEntry(e map[string]any, ipFamily int32) (string, bool) {
	_, hasMac := e["mac"]
	_, hasDuid := e["duid"]
	if ipFamily == 4 {
		if hasDuid {
			return "v4 reservations use `mac`, not `duid`", false
		}
		if !hasMac {
			return "v4 reservation requires `mac`", false
		}
		return "", true
	}
	// v6
	if hasMac {
		return "v6 reservations use `duid`, not `mac`", false
	}
	if !hasDuid {
		return "v6 reservation requires `duid`", false
	}
	return "", true
}

// validateUpdatePayload mirrors the v4/v6 mismatch checks Python
// runs at api/ipam.py:2102-2112 PLUS the PR 101 reservations
// family-match guard. Returns (msg, false) on first violation.
func validateUpdatePayload(req *dhcpScopeUpdateReq, existingFamily int32) (string, bool) {
	if existingFamily == 4 {
		if req.pdPoolsSet && len(req.PdPools) > 0 {
			return "pd_pools is v6-only", false
		}
		if req.preferredLifetimeSet && req.PreferredLifetimeSeconds != nil {
			return errPreferredV6Only, false
		}
	}
	if req.reservationsSet {
		if msg, ok := validateReservationsAgainstFamily(req.Reservations, existingFamily); !ok {
			return msg, false
		}
	}
	return "", true
}

// preflightUpdateFKs runs the subnet + template existence + template-
// family checks Python does at api/ipam.py:2113-2124 when the
// reassignment is non-null. Setting either to null unbinds and is
// always fine — Python's check explicitly skips that case
// (`if "X" in diff and diff["X"] is not None`).
func (h *Handler) preflightUpdateFKs(w http.ResponseWriter, r *http.Request, req *dhcpScopeUpdateReq, existingFamily int32) bool {
	if req.subnetIDSet && req.SubnetID != nil {
		if _, err := h.Q.GetSubnet(r.Context(), *req.SubnetID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusBadRequest, "subnet not found")
				return false
			}
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return false
		}
	}
	if req.templateIDSet && req.TemplateID != nil {
		tpl, err := h.Q.GetDhcpScopeTemplate(r.Context(), *req.TemplateID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusBadRequest, "dhcp scope template not found")
				return false
			}
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return false
		}
		if tpl.IPFamily != existingFamily {
			httpx.Error(w, http.StatusBadRequest,
				"template ip_family does not match scope ip_family")
			return false
		}
	}
	return true
}

// preflightCreateFKs runs the subnet + template existence + template-
// family checks Python does at api/ipam.py:1999-2015. Writes the
// 400 response on first failure and returns false. The subnet pre-
// check converts pgx.ErrNoRows into a 400 (FK constraint would
// surface as 23503 → 409 via httpx.Mapped without the pre-check;
// Python's 400 is the actionable shape).
func (h *Handler) preflightCreateFKs(w http.ResponseWriter, r *http.Request, req *dhcpScopeCreateReq) bool {
	if req.SubnetID != nil {
		if _, err := h.Q.GetSubnet(r.Context(), *req.SubnetID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusBadRequest, "subnet not found")
				return false
			}
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return false
		}
	}
	if req.TemplateID != nil {
		tpl, err := h.Q.GetDhcpScopeTemplate(r.Context(), *req.TemplateID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.Error(w, http.StatusBadRequest, "dhcp scope template not found")
				return false
			}
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return false
		}
		if tpl.IPFamily != req.IPFamily {
			httpx.Error(w, http.StatusBadRequest,
				"template ip_family does not match scope ip_family")
			return false
		}
	}
	return true
}

// defaultEmptyArray normalizes CREATE-time JSON payloads. Missing
// pools/options/reservations + explicit `null` payloads both
// resolve to `[]` (Python's Pydantic `Field(default_factory=list)`
// rejects `null` on Create but the cutover wire spec accepts it as
// "use the default"). Go's json.NewDecoder leaves the RawMessage
// empty when the key is omitted and as the 4-byte literal `null`
// when the value is explicit-null — handle both.
func defaultEmptyArray(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`[]`)
	}
	return raw
}

// defaultTrue mirrors Python's DhcpScopeBase.enabled default. Missing
// payload field → true; explicit false → false. Distinct from
// `*bool` direct-set because the CREATE column is NOT NULL.
func defaultTrue(b *bool) bool {
	if b == nil {
		return true
	}
	return *b
}

// buildDhcpScopeAuditDiff produces the audit `diff` payload (only
// set keys appear). Mirrors Python's exclude_unset dump at
// ipam.py:2147 — operators reading the audit stream rely on absence
// ⇒ unchanged. The four JSONB columns ride through as the raw
// decoded value; nil-when-set lands as JSON null so the auditor
// distinguishes "cleared" from "absent".
func buildDhcpScopeAuditDiff(req *dhcpScopeUpdateReq) map[string]any {
	diff := map[string]any{}
	if req.Name != nil {
		diff["name"] = *req.Name
	}
	if req.subnetIDSet {
		diff["subnet_id"] = req.SubnetID
	}
	if req.templateIDSet {
		diff["template_id"] = req.TemplateID
	}
	addJSONDiff(diff, "pools", req.poolsSet, req.Pools)
	addJSONDiff(diff, "pd_pools", req.pdPoolsSet, req.PdPools)
	addJSONDiff(diff, "options", req.optionsSet, req.Options)
	addJSONDiff(diff, "reservations", req.reservationsSet, req.Reservations)
	if req.validLifetimeSet {
		diff["valid_lifetime_seconds"] = req.ValidLifetimeSeconds
	}
	if req.renewTimerSet {
		diff["renew_timer_seconds"] = req.RenewTimerSeconds
	}
	if req.rebindTimerSet {
		diff["rebind_timer_seconds"] = req.RebindTimerSeconds
	}
	if req.preferredLifetimeSet {
		diff["preferred_lifetime_seconds"] = req.PreferredLifetimeSeconds
	}
	if req.Enabled != nil {
		diff["enabled"] = *req.Enabled
	}
	if req.descriptionSet {
		diff["description"] = req.Description
	}
	if req.autoPushOverrideSet {
		diff["auto_push_override"] = req.AutoPushOverride
	}
	return diff
}

func addJSONDiff(diff map[string]any, key string, set bool, raw json.RawMessage) {
	if !set {
		return
	}
	if len(raw) == 0 {
		diff[key] = nil
		return
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	diff[key] = v
}

// Compile-time assert: ctx is used to keep the import live for the
// loadScopeForMutation signature.
var _ = context.Background
