package cables

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const capCablesUpdate = "inventory:cables:update"

type createReq struct {
	AAssetID uuid.UUID `json:"a_asset_id"`
	APort    *string   `json:"a_port"`
	BAssetID uuid.UUID `json:"b_asset_id"`
	BPort    *string   `json:"b_port"`
	Medium   *string   `json:"medium"`
	Color    *string   `json:"color"`
	LengthM  *string   `json:"length_m"`
	Label    *string   `json:"label"`
	Face     *string   `json:"face"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	// Decode failures and missing-field failures answer differently on
	// purpose: a wire-type mismatch (e.g. {"length_m": 5} — number
	// where NUMERIC-as-string is expected) used to fall through to the
	// misleading "a_asset_id and b_asset_id required".
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.AAssetID == uuid.Nil || req.BAssetID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "a_asset_id and b_asset_id required")
		return
	}
	if err := validateEndpoints(req.AAssetID, req.BAssetID); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	aAsset, err := h.Q.GetAsset(r.Context(), req.AAssetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "a_asset_id not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	bAsset, err := h.Q.GetAsset(r.Context(), req.BAssetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "b_asset_id not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if err := validatePortInRange(aAsset.Name, aAsset.PortCount, req.APort, "a"); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePortInRange(bAsset.Name, bAsset.PortCount, req.BPort, "b"); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.validatePortNotInUse(r.Context(), req.AAssetID, req.APort, uuid.Nil, "a"); err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.validatePortNotInUse(r.Context(), req.BAssetID, req.BPort, uuid.Nil, "b"); err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return
	}
	siteID := aAsset.SiteID
	out, err := h.Q.CreateCable(r.Context(), dbq.CreateCableParams{
		SiteID: siteID, AAssetID: req.AAssetID, APort: req.APort,
		BAssetID: req.BAssetID, BPort: req.BPort,
		Medium: req.Medium, Color: req.Color, LengthM: req.LengthM,
		Label: req.Label, Face: req.Face,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "cable.create", TargetType: "cable", TargetID: out.ID.String(), SiteID: &sid,
		Metadata: map[string]any{
			"a_asset_id": out.AAssetID.String(),
			"b_asset_id": out.BAssetID.String(),
		},
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// updateReq mirrors Pydantic's CableUpdate(exclude_unset=True). Each
// nullable column carries an explicit "set" flag so {"medium": null}
// clears the row while an omitted key keeps the current value.
type updateReq struct {
	AAssetID    *uuid.UUID
	BAssetID    *uuid.UUID
	APort       *string
	aPortSet    bool
	BPort       *string
	bPortSet    bool
	Medium      *string
	mediumSet   bool
	Color       *string
	colorSet    bool
	LengthM     *string
	lengthMSet  bool
	Label       *string
	labelSet    bool
	Face        *string
	faceSet     bool
}

func (u *updateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Non-nullable foreign keys. {"a_asset_id": null} is a client
	// mistake (the column can't be cleared) and must fail loud rather
	// than silently keep the current value via COALESCE — Python's
	// _validate_cable_endpoints raised on this; we mirror it here.
	for _, p := range []struct {
		key string
		dst **uuid.UUID
	}{
		{"a_asset_id", &u.AAssetID},
		{"b_asset_id", &u.BAssetID},
	} {
		if v, ok := raw[p.key]; ok {
			if err := json.Unmarshal(v, p.dst); err != nil {
				return err
			}
			if *p.dst == nil {
				return errors.New(p.key + " cannot be null")
			}
		}
	}
	// Nullable string columns — track set/unset distinctly.
	for _, p := range []struct {
		key string
		set *bool
		val **string
	}{
		{"a_port", &u.aPortSet, &u.APort},
		{"b_port", &u.bPortSet, &u.BPort},
		{"medium", &u.mediumSet, &u.Medium},
		{"color", &u.colorSet, &u.Color},
		{"label", &u.labelSet, &u.Label},
		{"face", &u.faceSet, &u.Face},
	} {
		if v, ok := raw[p.key]; ok {
			*p.set = true
			if err := json.Unmarshal(v, p.val); err != nil {
				return err
			}
		}
	}
	if v, ok := raw["length_m"]; ok {
		u.lengthMSet = true
		// Accept either JSON number or string — pgx Numeric round-trips
		// as string in this codebase, but Python accepts float bodies.
		var f *float64
		if err := json.Unmarshal(v, &f); err != nil {
			// Fall back to direct string decode for clients that already
			// stringified the value.
			if serr := json.Unmarshal(v, &u.LengthM); serr != nil {
				return err
			}
		} else if f != nil {
			s := strconvFloat(*f)
			u.LengthM = &s
		}
	}
	return nil
}

// strconvFloat formats a float64 without scientific notation so pg's
// NUMERIC accepts every in-range value (NUMERIC(6,2) for length_m).
// %g would emit e.g. 1e+06 for 1000000, %f-style stays decimal.
func strconvFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	existing, ok := h.loadCable(w, r, id)
	if !ok {
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, existing.SiteID, capCablesUpdate); serr != nil {
		writeMapped(w, serr)
		return
	}
	var req updateReq
	// Surfaces the real decode error (including updateReq.UnmarshalJSON's
	// "a_asset_id cannot be null") instead of a flat "bad request body".
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	newA := resolveEndpoint(req.AAssetID, existing.AAssetID)
	newB := resolveEndpoint(req.BAssetID, existing.BAssetID)
	if err := validateEndpoints(newA, newB); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	newSiteID, ok := h.resolveNewSiteID(w, r, p, req.AAssetID, existing.AAssetID)
	if !ok {
		return
	}
	newAPort := resolvePort(req.aPortSet, req.APort, existing.APort)
	newBPort := resolvePort(req.bPortSet, req.BPort, existing.BPort)
	if placementChanged(req) {
		if !h.revalidatePlacement(w, r, req, id, newA, newB, newAPort, newBPort) {
			return
		}
	}
	out, err := h.Q.UpdateCable(r.Context(), dbq.UpdateCableParams{
		ID: id, SiteID: newSiteID,
		AAssetID: req.AAssetID, BAssetID: req.BAssetID,
		APortSet: req.aPortSet, APort: req.APort,
		BPortSet: req.bPortSet, BPort: req.BPort,
		MediumSet: req.mediumSet, Medium: req.Medium,
		ColorSet: req.colorSet, Color: req.Color,
		LengthMSet: req.lengthMSet, LengthM: req.LengthM,
		LabelSet: req.labelSet, Label: req.Label,
		FaceSet: req.faceSet, Face: req.Face,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	sid := out.SiteID
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "cable.update", TargetType: "cable", TargetID: out.ID.String(), SiteID: &sid,
		Diff: req.diff(),
	})
	httpx.JSON(w, http.StatusOK, out)
}

// diff returns a map of just the fields the patch actually set. Drives
// the audit_log row's diff column (Python's `diff=diff` parity).
func (u updateReq) diff() map[string]any {
	d := map[string]any{}
	if u.AAssetID != nil {
		d["a_asset_id"] = u.AAssetID.String()
	}
	if u.BAssetID != nil {
		d["b_asset_id"] = u.BAssetID.String()
	}
	if u.aPortSet {
		d["a_port"] = u.APort
	}
	if u.bPortSet {
		d["b_port"] = u.BPort
	}
	if u.mediumSet {
		d["medium"] = u.Medium
	}
	if u.colorSet {
		d["color"] = u.Color
	}
	if u.lengthMSet {
		d["length_m"] = u.LengthM
	}
	if u.labelSet {
		d["label"] = u.Label
	}
	if u.faceSet {
		d["face"] = u.Face
	}
	return d
}

// loadCable fetches the existing row; writes 404/5xx and returns
// (zero, false) on error.
func (h *Handler) loadCable(w http.ResponseWriter, r *http.Request, id uuid.UUID) (dbq.Cable, bool) {
	c, err := h.Q.GetCable(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "cable not found")
			return dbq.Cable{}, false
		}
		writeMapped(w, err)
		return dbq.Cable{}, false
	}
	return c, true
}

func resolveEndpoint(patch *uuid.UUID, current uuid.UUID) uuid.UUID {
	if patch != nil {
		return *patch
	}
	return current
}

func resolvePort(set bool, patch *string, current *string) *string {
	if set {
		return patch
	}
	return current
}

func placementChanged(req updateReq) bool {
	return req.AAssetID != nil || req.BAssetID != nil || req.aPortSet || req.bPortSet
}

func writeMapped(w http.ResponseWriter, err error) {
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// resolveNewSiteID returns the new site_id when the a-end changes,
// else nil. Scope-checks the new a-end site. On failure, writes the
// response and returns (nil, false).
func (h *Handler) resolveNewSiteID(w http.ResponseWriter, r *http.Request, p auth.Principal, newAAsset *uuid.UUID, existingAAsset uuid.UUID) (*uuid.UUID, bool) {
	if newAAsset == nil || *newAAsset == existingAAsset {
		return nil, true
	}
	aAsset, err := h.Q.GetAsset(r.Context(), *newAAsset)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusBadRequest, "a_asset_id not found")
		} else {
			writeMapped(w, err)
		}
		return nil, false
	}
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, aAsset.SiteID, capCablesUpdate); serr != nil {
		writeMapped(w, serr)
		return nil, false
	}
	s := aAsset.SiteID
	return &s, true
}

// revalidatePlacement re-runs port-in-range + port-unused for both
// ends. Also EnforceSiteScope on each end's site_id when the caller
// is changing the corresponding endpoint — a scoped principal can't
// wire a cable to an asset in a site they don't own. Returns false
// (after writing the response) when validation fails.
func (h *Handler) revalidatePlacement(w http.ResponseWriter, r *http.Request, req updateReq, id uuid.UUID, newA, newB uuid.UUID, newAPort, newBPort *string) bool {
	p, _ := auth.From(r.Context())
	aAsset, err := h.Q.GetAsset(r.Context(), newA)
	if err != nil {
		writeMapped(w, err)
		return false
	}
	bAsset, err := h.Q.GetAsset(r.Context(), newB)
	if err != nil {
		writeMapped(w, err)
		return false
	}
	// Scope-check the b-end's site whenever b_asset_id is being
	// changed (the a-end side is already covered by resolveNewSiteID).
	if req.BAssetID != nil {
		if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, bAsset.SiteID, capCablesUpdate); serr != nil {
			writeMapped(w, serr)
			return false
		}
	}
	if err := validatePortInRange(aAsset.Name, aAsset.PortCount, newAPort, "a"); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := validatePortInRange(bAsset.Name, bAsset.PortCount, newBPort, "b"); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return false
	}
	// excludeID skips this cable's own row so a PATCH that only
	// touches label/medium doesn't false-positive on its own port.
	if err := h.validatePortNotInUse(r.Context(), newA, newAPort, id, "a"); err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return false
	}
	if err := h.validatePortNotInUse(r.Context(), newB, newBPort, id, "b"); err != nil {
		httpx.Error(w, http.StatusConflict, err.Error())
		return false
	}
	return true
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	if _, err := h.Q.GetCable(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "cable not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.DeleteCable(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "cable.delete", TargetType: "cable", TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
