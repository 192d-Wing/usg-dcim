// Go port of Python's DHCP scope-template CRUD surface
// (api/ipam.py:2735-2920). Reusable option-bundle + timer defaults
// that DhcpScope rows inherit via template_id. Five routes:
//
//   GET    /api/v1/ipam/dhcp/scope-templates           list
//   POST   /api/v1/ipam/dhcp/scope-templates           create
//   GET    /api/v1/ipam/dhcp/scope-templates/{id}      get
//   PATCH  /api/v1/ipam/dhcp/scope-templates/{id}      partial update
//   DELETE /api/v1/ipam/dhcp/scope-templates/{id}      delete (FK SET NULL on scopes)
//
// ABAC: every route enforces fabric scope via GetDhcpScopeTemplate-
// FabricID (1-hop); LIST also filters with ScopedFabricFilter so a
// fabric-scoped caller only sees their templates.
//
// Two background side-effects Python does on PATCH (auto-push fanout
// + bundle cache rerender) are NOT in this PR — the Go cron polling
// (dhcp_bundle_rerender every 2 min) covers cache freshness, and an
// auto-push fanout port depends on shared infra (`auto_push_scope_in_
// background`) that hasn't been ported yet. Documented in the update
// handler so a future PR knows the wiring point.
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
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const (
	errDhcpScopeTemplateNotFound = "dhcp scope template not found"
	errPreferredV6Only           = "preferred_lifetime_seconds is v6-only"
)

// dhcpScopeTemplateOut is the API wire shape (Python parity:
// schemas/ipam.py DhcpScopeTemplateOut). The underlying dbq row's
// `options_json` column is renamed to `options` so a GET response
// round-trips cleanly through a POST/PATCH that accepts the same
// shape. Without this remap, finch/operator tooling reading
// body.options would see undefined (the dbq tag is options_json).
type dhcpScopeTemplateOut struct {
	ID                       uuid.UUID       `json:"id"`
	FabricID                 uuid.UUID       `json:"fabric_id"`
	Name                     string          `json:"name"`
	IPFamily                 int32           `json:"ip_family"`
	Options                  json.RawMessage `json:"options"`
	ValidLifetimeSeconds     *int32          `json:"valid_lifetime_seconds"`
	RenewTimerSeconds        *int32          `json:"renew_timer_seconds"`
	RebindTimerSeconds       *int32          `json:"rebind_timer_seconds"`
	PreferredLifetimeSeconds *int32          `json:"preferred_lifetime_seconds"`
	Description              *string         `json:"description"`
	CreatedAt                string          `json:"created_at"`
	UpdatedAt                string          `json:"updated_at"`
}

// toScopeTemplateOut adapts the dbq row to the wire shape. Timestamps
// use the same isoformat-with-tz layout as the push history endpoint
// (PR 5) so consumers parse both surfaces with one ISO-8601 mode.
func toScopeTemplateOut(t dbq.DhcpScopeTemplate) dhcpScopeTemplateOut {
	const isoTZ = "2006-01-02T15:04:05.000000-07:00"
	opts := t.OptionsJSON
	if len(opts) == 0 {
		opts = json.RawMessage(`[]`)
	}
	return dhcpScopeTemplateOut{
		ID: t.ID, FabricID: t.FabricID,
		Name: t.Name, IPFamily: t.IPFamily,
		Options:                  opts,
		ValidLifetimeSeconds:     t.ValidLifetimeSeconds,
		RenewTimerSeconds:        t.RenewTimerSeconds,
		RebindTimerSeconds:       t.RebindTimerSeconds,
		PreferredLifetimeSeconds: t.PreferredLifetimeSeconds,
		Description:              t.Description,
		CreatedAt:                t.CreatedAt.UTC().Format(isoTZ),
		UpdatedAt:                t.UpdatedAt.UTC().Format(isoTZ),
	}
}

type dhcpScopeTemplatesPage = httpx.Page[dhcpScopeTemplateOut]

func (h *Handler) listDhcpScopeTemplates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	scopeIds, ok := scopedListFilter(r, "ipam:dhcp-scope-templates:read")
	if !ok {
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.DhcpScopeTemplate](limit, offset))
		return
	}
	params := dbq.ListDhcpScopeTemplatesParams{Limit: limit, Offset: offset, ScopeFabricIds: scopeIds}
	if v := q.Get("fabric_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "fabric_id is not a uuid")
			return
		}
		params.FabricID = &id
	}
	if v := q.Get("ip_family"); v != "" {
		fam, err := parseIPFamilyParam(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		params.IPFamily = &fam
	}
	items, err := h.Q.ListDhcpScopeTemplates(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountDhcpScopeTemplates(r.Context(), dbq.CountDhcpScopeTemplatesParams{
		FabricID: params.FabricID, IPFamily: params.IPFamily, ScopeFabricIds: scopeIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	out := make([]dhcpScopeTemplateOut, len(items))
	for i, t := range items {
		out[i] = toScopeTemplateOut(t)
	}
	httpx.JSON(w, http.StatusOK, dhcpScopeTemplatesPage{Items: out, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getDhcpScopeTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	t, err := h.Q.GetDhcpScopeTemplate(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeTemplateNotFound)
		return
	}
	if !h.enforceFabric(w, r, t.FabricID, "ipam:dhcp-scope-templates:read") {
		return
	}
	httpx.JSON(w, http.StatusOK, toScopeTemplateOut(t))
}

// dhcpScopeTemplateCreateReq mirrors Python's DhcpScopeTemplateCreate
// (schemas/ipam.py). Options arrives as a JSON array of
// {code, data, ...} objects the renderer's MergeTemplateIntoScope
// reads — the API stores it verbatim as options_json after a
// canonical-encode pass (drops nulls so empty fields don't ship to
// Kea).
type dhcpScopeTemplateCreateReq struct {
	FabricID                 uuid.UUID       `json:"fabric_id"`
	Name                     string          `json:"name"`
	IPFamily                 int32           `json:"ip_family"`
	Options                  json.RawMessage `json:"options"`
	ValidLifetimeSeconds     *int32          `json:"valid_lifetime_seconds"`
	RenewTimerSeconds        *int32          `json:"renew_timer_seconds"`
	RebindTimerSeconds       *int32          `json:"rebind_timer_seconds"`
	PreferredLifetimeSeconds *int32          `json:"preferred_lifetime_seconds"`
	Description              *string         `json:"description"`
}

func (h *Handler) createDhcpScopeTemplate(w http.ResponseWriter, r *http.Request) {
	var req dhcpScopeTemplateCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name and fabric_id required")
		return
	}
	if req.IPFamily != 4 && req.IPFamily != 6 {
		httpx.Error(w, http.StatusBadRequest, "ip_family must be 4 or 6")
		return
	}
	if req.IPFamily == 4 && req.PreferredLifetimeSeconds != nil {
		httpx.Error(w, http.StatusBadRequest, errPreferredV6Only)
		return
	}
	if !h.enforceFabric(w, r, req.FabricID, "ipam:dhcp-scope-templates:create") {
		return
	}
	// Python checks that the fabric exists before the INSERT to
	// give a nicer 400. The DB FK would otherwise surface as a 23503
	// → 409 via httpx.Mapped. Match Python's posture with a narrow
	// pre-check that fits the existing helper.
	if _, ok := h.lookupFabricID(w, r.Context(),
		func(ctx context.Context) (uuid.UUID, error) { return req.FabricID, h.fabricExists(ctx, req.FabricID) },
		"fabric not found"); !ok {
		return
	}
	options := req.Options
	if len(options) == 0 {
		options = json.RawMessage(`[]`)
	}
	out, err := h.Q.CreateDhcpScopeTemplate(r.Context(), dbq.CreateDhcpScopeTemplateParams{
		FabricID:                 req.FabricID,
		Name:                     req.Name,
		IPFamily:                 req.IPFamily,
		OptionsJSON:              options,
		ValidLifetimeSeconds:     req.ValidLifetimeSeconds,
		RenewTimerSeconds:        req.RenewTimerSeconds,
		RebindTimerSeconds:       req.RebindTimerSeconds,
		PreferredLifetimeSeconds: req.PreferredLifetimeSeconds,
		Description:              req.Description,
	})
	if err != nil {
		// 23505 on (fabric_id, name) unique → 409 via the standard
		// path. Other errors map per httpx.Mapped.
		mapErr(w, err, errDhcpScopeTemplateNotFound)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope_template.create",
		TargetType: "dhcp_scope_template", TargetID: out.ID.String(),
		Metadata: map[string]any{
			"fabric_id": req.FabricID.String(),
			"ip_family": req.IPFamily,
		},
	})
	httpx.JSON(w, http.StatusCreated, toScopeTemplateOut(out))
}

// dhcpScopeTemplateUpdateReq carries the PATCH payload. Every
// nullable column needs a paired `<field>Set bool` so the JSON
// "key omitted vs key present with null" distinction reaches the
// SQL CASE expressions. The custom UnmarshalJSON below populates
// the Set flags from the raw object.
type dhcpScopeTemplateUpdateReq struct {
	Name                     *string
	Options                  json.RawMessage
	optionsSet               bool
	ValidLifetimeSeconds     *int32
	validLifetimeSet         bool
	RenewTimerSeconds        *int32
	renewTimerSet            bool
	RebindTimerSeconds       *int32
	rebindTimerSet           bool
	PreferredLifetimeSeconds *int32
	preferredLifetimeSet     bool
	Description              *string
	descriptionSet           bool
}

// errNullName is the sentinel buildScopeTemplateUpdate / the handler
// translate to a 400. The column is NOT NULL in Postgres; Python's
// PATCH bubbles that up as an IntegrityError → 500. Catching the
// case earlier gives a cleaner error and a smaller behavioural
// divergence from Python (still an error, just better-shaped).
var errNullName = errors.New("name must not be null")

func (u *dhcpScopeTemplateUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		// `"name": null` would COALESCE-keep the current value in
		// SQL (silent no-op), masking a likely client bug. Reject
		// it instead — Python errors loudly on the underlying NOT
		// NULL constraint; we error early here.
		if string(v) == "null" {
			return errNullName
		}
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["options"]; ok {
		u.optionsSet = true
		if string(v) != "null" {
			u.Options = v
		}
	}
	setIfPresent(raw, "valid_lifetime_seconds", &u.validLifetimeSet, &u.ValidLifetimeSeconds)
	setIfPresent(raw, "renew_timer_seconds", &u.renewTimerSet, &u.RenewTimerSeconds)
	setIfPresent(raw, "rebind_timer_seconds", &u.rebindTimerSet, &u.RebindTimerSeconds)
	setIfPresent(raw, "preferred_lifetime_seconds", &u.preferredLifetimeSet, &u.PreferredLifetimeSeconds)
	var descSet bool
	setIfPresent(raw, "description", &descSet, &u.Description)
	u.descriptionSet = descSet
	return nil
}

func (h *Handler) updateDhcpScopeTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	// Pre-fetch to enforce fabric scope on the EXISTING row's fabric.
	// Python does this via db.get(...) then enforce_fabric_scope.
	// The same row also gives us ip_family for the preferred-lifetime
	// v6-only guard.
	existing, err := h.Q.GetDhcpScopeTemplate(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeTemplateNotFound)
		return
	}
	if !h.enforceFabric(w, r, existing.FabricID, "ipam:dhcp-scope-templates:update") {
		return
	}
	var req dhcpScopeTemplateUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, errNullName) {
			httpx.Error(w, http.StatusBadRequest, errNullName.Error())
			return
		}
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if existing.IPFamily == 4 && req.preferredLifetimeSet && req.PreferredLifetimeSeconds != nil {
		httpx.Error(w, http.StatusBadRequest, errPreferredV6Only)
		return
	}
	// Normalize the cleared-to-empty case for options. Python:
	// `payload.options == None → options_json = []`. Mirror by
	// substituting [] when the patch said `"options": null`.
	options := req.Options
	if req.optionsSet && len(options) == 0 {
		options = json.RawMessage(`[]`)
	}
	out, err := h.Q.UpdateDhcpScopeTemplate(r.Context(), dbq.UpdateDhcpScopeTemplateParams{
		ID:                       id,
		Name:                     req.Name,
		OptionsSet:               req.optionsSet,
		OptionsJSON:              options,
		ValidLifetimeSet:         req.validLifetimeSet,
		ValidLifetimeSeconds:     req.ValidLifetimeSeconds,
		RenewTimerSet:            req.renewTimerSet,
		RenewTimerSeconds:        req.RenewTimerSeconds,
		RebindTimerSet:           req.rebindTimerSet,
		RebindTimerSeconds:       req.RebindTimerSeconds,
		PreferredLifetimeSet:     req.preferredLifetimeSet,
		PreferredLifetimeSeconds: req.PreferredLifetimeSeconds,
		DescriptionSet:           req.descriptionSet,
		Description:              req.Description,
	})
	if err != nil {
		mapErr(w, err, errDhcpScopeTemplateNotFound)
		return
	}
	// Audit diff payload mirrors Python's `diff=payload.model_dump(
	// exclude_unset=True)` (line 2865). Build it from the Set flags
	// so a key the client omitted doesn't appear in the audit log
	// (operators reading the audit stream rely on "only changed
	// keys appear" to triage which fields a user touched).
	diff := buildScopeTemplateAuditDiff(&req)
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope_template.update",
		TargetType: "dhcp_scope_template", TargetID: id.String(),
		Diff: diff,
	})
	// Deferred (Python parity gap, documented in package doc):
	// auto-push fanout via schedule_template_fanout_pushes +
	// per-scope auto_push_scope_in_background, and per-server
	// enqueue_bundle_rerender. The Go scheduler runs the bundle
	// rerender every 2 minutes regardless, so cache freshness is
	// bounded. Auto-push fanout depends on shared infra not yet
	// ported and is left for a follow-up.
	httpx.JSON(w, http.StatusOK, toScopeTemplateOut(out))
}

func (h *Handler) deleteDhcpScopeTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	existing, err := h.Q.GetDhcpScopeTemplate(r.Context(), id)
	if err != nil {
		mapErr(w, err, errDhcpScopeTemplateNotFound)
		return
	}
	if !h.enforceFabric(w, r, existing.FabricID, "ipam:dhcp-scope-templates:delete") {
		return
	}
	if err := h.Q.DeleteDhcpScopeTemplate(r.Context(), id); err != nil {
		mapErr(w, err, errDhcpScopeTemplateNotFound)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope_template.delete",
		TargetType: "dhcp_scope_template", TargetID: id.String(),
		Metadata: map[string]any{
			"fabric_id": existing.FabricID.String(),
			"ip_family": existing.IPFamily,
		},
	})
	w.WriteHeader(http.StatusNoContent)
}

// fabricExists is a tiny adapter so the create handler can reuse
// lookupFabricID's mapErr semantics for "fabric missing" without
// adding a one-off SELECT. Returns ErrNoRows when the fabric is
// gone; lookupFabricID then writes a 404 with the supplied msg.
func (h *Handler) fabricExists(ctx context.Context, id uuid.UUID) error {
	_, err := h.Q.GetFabric(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgx.ErrNoRows
	}
	return err
}

// parseIPFamilyParam validates ?ip_family=4|6. Returns the int32 for
// the SQL filter or an error suitable for an HTTP 400 body. Reject
// anything else early — a wrong value would silently return no rows
// because the templates table CHECK rejects bad ip_family values.
func parseIPFamilyParam(s string) (int32, error) {
	switch s {
	case "4":
		return 4, nil
	case "6":
		return 6, nil
	default:
		return 0, errors.New("ip_family must be 4 or 6")
	}
}

// buildScopeTemplateAuditDiff renders the patch payload into the
// `diff` map shape Python's audit.record stores. Only set fields
// appear; nil values for set-but-cleared fields ride through as
// JSON nulls so downstream consumers can distinguish "cleared" from
// "absent". Same posture as Python's exclude_unset=True dump.
func buildScopeTemplateAuditDiff(req *dhcpScopeTemplateUpdateReq) map[string]any {
	diff := map[string]any{}
	if req.Name != nil {
		diff["name"] = *req.Name
	}
	if req.optionsSet {
		// Options is serialized as raw JSON; decode for the audit
		// map so the stored row reads as a structured array, not a
		// quoted string. Python's exclude_unset dump keeps
		// `"options": None` distinct from `"options": []`; preserve
		// that here so auditors can tell "operator cleared via null"
		// apart from "operator posted []".
		if len(req.Options) == 0 {
			diff["options"] = nil
		} else {
			var opts any
			_ = json.Unmarshal(req.Options, &opts)
			diff["options"] = opts
		}
	}
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
	if req.descriptionSet {
		diff["description"] = req.Description
	}
	return diff
}
