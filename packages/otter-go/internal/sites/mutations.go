package sites

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type createReq struct {
	RegionID       uuid.UUID  `json:"region_id"`
	Name           string     `json:"name"`
	Code           string     `json:"code"`
	Address        *string    `json:"address"`
	Latitude       *string    `json:"latitude"`
	Longitude      *string    `json:"longitude"`
	Timezone       *string    `json:"timezone"`
	Majcom         *string    `json:"majcom"`
	OrganizationID *uuid.UUID `json:"organization_id"`
	MissionOwner   *string    `json:"mission_owner"`
	Enclave        *string    `json:"enclave"`
	Classification *string    `json:"classification"`
	LifecycleState string     `json:"lifecycle_state"`
	MetadataJson   json.RawMessage `json:"metadata_json"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Code == "" || req.RegionID == uuid.Nil {
		httpx.Error(w, http.StatusBadRequest, "region_id, name, code required")
		return
	}
	// PR 61 — caller must own both the enclave and classification they're
	// tagging the site with. Nil/empty values are gated to global per the
	// "unlabeled = needs global scope" policy in auth.EnforceEnclave.
	p, _ := auth.From(r.Context())
	if err := auth.EnforceEnclave(p, req.Enclave, "inventory:sites:create"); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	if err := auth.EnforceClassification(p, req.Classification, "inventory:sites:create"); err != nil {
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	}
	if req.LifecycleState == "" {
		req.LifecycleState = "active"
	}
	out, err := h.Q.CreateSite(r.Context(), dbq.CreateSiteParams{
		RegionID: req.RegionID, Name: req.Name, Code: req.Code,
		Address: req.Address, Latitude: req.Latitude, Longitude: req.Longitude,
		Timezone: req.Timezone, Majcom: req.Majcom,
		OrganizationID: req.OrganizationID,
		MissionOwner: req.MissionOwner, Enclave: req.Enclave, Classification: req.Classification,
		LifecycleState: req.LifecycleState, MetadataJson: req.MetadataJson,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "site.create", TargetType: "site", TargetID: out.ID.String(), SiteID: &out.ID,
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// updateReq tracks per-field "was the JSON key present" via a custom
// UnmarshalJSON so nullable fields can be explicitly cleared without
// affecting unaddressed columns. Mirrors Pydantic
// model_dump(exclude_unset=True).
type updateReq struct {
	Name              *string
	Address           *string
	addressSet        bool
	Majcom            *string
	majcomSet         bool
	OrganizationID    *uuid.UUID
	organizationIDSet bool
	MissionOwner      *string
	missionOwnerSet   bool
	Enclave           *string
	enclaveSet        bool
	LifecycleState    *string
	MetadataJson      json.RawMessage
}

func (u *updateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &u.Name); err != nil {
			return err
		}
	}
	if v, ok := raw["address"]; ok {
		u.addressSet = true
		if err := json.Unmarshal(v, &u.Address); err != nil {
			return err
		}
	}
	if v, ok := raw["majcom"]; ok {
		u.majcomSet = true
		if err := json.Unmarshal(v, &u.Majcom); err != nil {
			return err
		}
	}
	if v, ok := raw["organization_id"]; ok {
		u.organizationIDSet = true
		if err := json.Unmarshal(v, &u.OrganizationID); err != nil {
			return err
		}
	}
	if v, ok := raw["mission_owner"]; ok {
		u.missionOwnerSet = true
		if err := json.Unmarshal(v, &u.MissionOwner); err != nil {
			return err
		}
	}
	if v, ok := raw["enclave"]; ok {
		u.enclaveSet = true
		if err := json.Unmarshal(v, &u.Enclave); err != nil {
			return err
		}
	}
	if v, ok := raw["lifecycle_state"]; ok {
		if err := json.Unmarshal(v, &u.LifecycleState); err != nil {
			return err
		}
	}
	if v, ok := raw["metadata_json"]; ok {
		u.MetadataJson = v
	}
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, id, "inventory:sites:update"); serr != nil {
		httpx.Error(w, http.StatusForbidden, serr.Error())
		return
	}
	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	// PR 61 — caller can't reassign a site into an enclave they don't
	// own. Classification isn't currently updatable via this endpoint
	// (set-once at create), so no classification check here.
	if req.enclaveSet {
		if err := auth.EnforceEnclave(p, req.Enclave, "inventory:sites:update"); err != nil {
			httpx.Error(w, http.StatusForbidden, err.Error())
			return
		}
	}
	out, err := h.Q.UpdateSite(r.Context(), dbq.UpdateSiteParams{
		ID: id, Name: req.Name,
		AddressSet: req.addressSet, Address: req.Address,
		MajcomSet: req.majcomSet, Majcom: req.Majcom,
		OrganizationIDSet: req.organizationIDSet, OrganizationID: req.OrganizationID,
		MissionOwnerSet: req.missionOwnerSet, MissionOwner: req.MissionOwner,
		EnclaveSet: req.enclaveSet, Enclave: req.Enclave,
		LifecycleState: req.LifecycleState, MetadataJson: req.MetadataJson,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "site not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "site.update", TargetType: "site", TargetID: id.String(), SiteID: &id,
	})
	httpx.JSON(w, http.StatusOK, out)
}
