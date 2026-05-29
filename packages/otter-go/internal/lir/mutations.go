// LirPool write paths: create, update, delete. Audit records emit on
// each successful mutation. Schema CHECK constraints (ck_lir_pool_*)
// are pre-validated here so the API returns a 422 with a useful
// message instead of an opaque constraint-violation 500.
package lir

import (
	"encoding/json"
	"net/http"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const msgPoolNotFound = "lir pool not found"

// ---- create ----

type createPoolReq struct {
	Name                   string     `json:"name"`
	Slug                   string     `json:"slug"`
	Description            *string    `json:"description"`
	IpFamily               int16      `json:"ip_family"`
	FabricID               *string    `json:"fabric_id"`
	Classification         *string    `json:"classification"`
	MinPrefixLength        int16      `json:"min_prefix_length"`
	MaxPrefixLength        int16      `json:"max_prefix_length"`
	DefaultSupernetPurpose *string    `json:"default_supernet_purpose"`
	ArinParentNetHandle    *string    `json:"arin_parent_net_handle"`
	Enabled                *bool      `json:"enabled"`
}

func (h *Handler) createPool(w http.ResponseWriter, r *http.Request) {
	var req createPoolReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Name == "" || req.Slug == "" {
		httpx.Error(w, http.StatusUnprocessableEntity,"name and slug are required")
		return
	}
	if req.MinPrefixLength > req.MaxPrefixLength {
		httpx.Error(w, http.StatusUnprocessableEntity,"min_prefix_length must be ≤ max_prefix_length")
		return
	}
	if err := validateFamilyPrefix(req.IpFamily, req.MaxPrefixLength); err != nil {
		writeValidationError(w, err)
		return
	}
	if err := validateFamilyPrefix(req.IpFamily, req.MinPrefixLength); err != nil {
		writeValidationError(w, err)
		return
	}
	fabricID, ok := parseOptionalUUID(w, req.FabricID, "fabric_id")
	if !ok {
		return
	}
	out, err := h.Q.CreateLirPool(r.Context(), dbq.CreateLirPoolParams{
		Name:                   req.Name,
		Slug:                   req.Slug,
		Description:            req.Description,
		IpFamily:               req.IpFamily,
		FabricID:               fabricID,
		Classification:         req.Classification,
		MinPrefixLength:        req.MinPrefixLength,
		MaxPrefixLength:        req.MaxPrefixLength,
		DefaultSupernetPurpose: req.DefaultSupernetPurpose,
		ArinParentNetHandle:    req.ArinParentNetHandle,
		Enabled:                req.Enabled,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.pool.create", TargetType: "lir_pool", TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// ---- update ----

// updatePoolReq tracks explicit-null vs absent for nullable fields,
// the same trick organization.updateReq uses. Required fields (name,
// slug, prefix bounds, enabled) use plain pointers — nil means
// "don't update" and there's no separate ":null" semantics.
type updatePoolReq struct {
	Name                      *string
	Slug                      *string
	DescriptionSet            bool
	Description               *string
	FabricSet                 bool
	FabricID                  *string
	ClassificationSet         bool
	Classification            *string
	MinPrefixLength           *int16
	MaxPrefixLength           *int16
	PurposeSet                bool
	DefaultSupernetPurpose    *string
	ArinSet                   bool
	ArinParentNetHandle       *string
	Enabled                   *bool
}

func (u *updatePoolReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	plain := map[string]any{
		"name":              &u.Name,
		"slug":              &u.Slug,
		"min_prefix_length": &u.MinPrefixLength,
		"max_prefix_length": &u.MaxPrefixLength,
		"enabled":           &u.Enabled,
	}
	for k, dst := range plain {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	tracked := []struct {
		key string
		set *bool
		dst any
	}{
		{"description", &u.DescriptionSet, &u.Description},
		{"fabric_id", &u.FabricSet, &u.FabricID},
		{"classification", &u.ClassificationSet, &u.Classification},
		{"default_supernet_purpose", &u.PurposeSet, &u.DefaultSupernetPurpose},
		{"arin_parent_net_handle", &u.ArinSet, &u.ArinParentNetHandle},
	}
	for _, t := range tracked {
		if v, ok := raw[t.key]; ok {
			*t.set = true
			_ = json.Unmarshal(v, t.dst)
		}
	}
	return nil
}

func (h *Handler) updatePool(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var req updatePoolReq
	if !decodeBody(w, r, &req) {
		return
	}
	// Pre-fetch so the API can re-validate min ≤ max across partial
	// updates (the schema's per-field validator only fires when both
	// arrive together — PATCH can change one without the other).
	cur, err := h.Q.GetLirPool(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgPoolNotFound)
		return
	}
	newMin := cur.MinPrefixLength
	if req.MinPrefixLength != nil {
		newMin = *req.MinPrefixLength
	}
	newMax := cur.MaxPrefixLength
	if req.MaxPrefixLength != nil {
		newMax = *req.MaxPrefixLength
	}
	if newMin > newMax {
		httpx.Error(w, http.StatusUnprocessableEntity,"min_prefix_length must be ≤ max_prefix_length")
		return
	}
	// Family is immutable; re-validate both bounds against the
	// persisted family so a PATCH that pushes max past the cap fails
	// cleanly with 422.
	if err := validateFamilyPrefix(cur.IpFamily, newMax); err != nil {
		writeValidationError(w, err)
		return
	}
	if err := validateFamilyPrefix(cur.IpFamily, newMin); err != nil {
		writeValidationError(w, err)
		return
	}
	fabricID, ok := parseOptionalUUID(w, req.FabricID, "fabric_id")
	if !ok {
		return
	}
	out, err := h.Q.UpdateLirPool(r.Context(), dbq.UpdateLirPoolParams{
		ID: id,
		Name: req.Name, Slug: req.Slug,
		DescriptionSet: req.DescriptionSet, Description: req.Description,
		FabricSet: req.FabricSet, FabricID: fabricID,
		ClassificationSet: req.ClassificationSet, Classification: req.Classification,
		MinPrefixLength: req.MinPrefixLength, MaxPrefixLength: req.MaxPrefixLength,
		PurposeSet: req.PurposeSet, DefaultSupernetPurpose: req.DefaultSupernetPurpose,
		ArinSet: req.ArinSet, ArinParentNetHandle: req.ArinParentNetHandle,
		Enabled: req.Enabled,
	})
	if err != nil {
		mapErr(w, err, msgPoolNotFound)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.pool.update", TargetType: "lir_pool",
		TargetID: id.String(), Diff: req.diff(),
	})
	httpx.JSON(w, http.StatusOK, out)
}

// diff returns the per-field map the audit row stores. Each tracked
// field either has an explicit set-bool (nullable fields) or a
// non-nil pointer (required fields) signaling the operator touched
// it. Kept off updatePool to keep that handler's cognitive
// complexity inside the linter's budget.
func (u updatePoolReq) diff() map[string]any {
	d := map[string]any{}
	if u.Name != nil {
		d["name"] = *u.Name
	}
	if u.Slug != nil {
		d["slug"] = *u.Slug
	}
	if u.MinPrefixLength != nil {
		d["min_prefix_length"] = *u.MinPrefixLength
	}
	if u.MaxPrefixLength != nil {
		d["max_prefix_length"] = *u.MaxPrefixLength
	}
	if u.Enabled != nil {
		d["enabled"] = *u.Enabled
	}
	if u.DescriptionSet {
		d["description"] = u.Description
	}
	if u.FabricSet {
		d["fabric_id"] = u.FabricID
	}
	if u.ClassificationSet {
		d["classification"] = u.Classification
	}
	if u.PurposeSet {
		d["default_supernet_purpose"] = u.DefaultSupernetPurpose
	}
	if u.ArinSet {
		d["arin_parent_net_handle"] = u.ArinParentNetHandle
	}
	return d
}

// ---- delete ----

func (h *Handler) deletePool(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.Q.GetLirPool(r.Context(), id); err != nil {
		mapErr(w, err, msgPoolNotFound)
		return
	}
	// Refuse if any allocation still references the pool. The FK
	// would block the DELETE anyway, but a 409 here gives a clearer
	// error than the underlying constraint violation envelope.
	n, err := h.Q.CountAllocationsForPool(r.Context(), id)
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict,
			"pool still has allocations; return or detach them first")
		return
	}
	// Explicit detach of source supernets so the audit trail captures
	// the cascade. The FK's ON DELETE SET NULL would do the same
	// effect but without a visible record.
	if err := h.Q.DetachAllPoolSupernets(r.Context(), id); err != nil {
		mapErr(w, err, "")
		return
	}
	if err := h.Q.DeleteLirPool(r.Context(), id); err != nil {
		mapErr(w, err, msgPoolNotFound)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.pool.delete", TargetType: "lir_pool", TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
