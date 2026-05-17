package organization

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

func mapErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, notFoundMsg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

type createReq struct {
	Name           string  `json:"name"`
	ArinOrgID      *string `json:"arin_org_id"`
	AddressLine1   string  `json:"address_line1"`
	AddressLine2   *string `json:"address_line2"`
	City           string  `json:"city"`
	StateProvince  *string `json:"state_province"`
	PostalCode     *string `json:"postal_code"`
	Country        string  `json:"country"`
	Phone          *string `json:"phone"`
	Email          *string `json:"email"`
	AdminPocName   string  `json:"admin_poc_name"`
	AdminPocEmail  string  `json:"admin_poc_email"`
	AdminPocPhone  *string `json:"admin_poc_phone"`
	TechPocName    string  `json:"tech_poc_name"`
	TechPocEmail   string  `json:"tech_poc_email"`
	TechPocPhone   *string `json:"tech_poc_phone"`
	AbusePocName   string  `json:"abuse_poc_name"`
	AbusePocEmail  string  `json:"abuse_poc_email"`
	AbusePocPhone  *string `json:"abuse_poc_phone"`
	NocPocName     *string `json:"noc_poc_name"`
	NocPocEmail    *string `json:"noc_poc_email"`
	NocPocPhone    *string `json:"noc_poc_phone"`
	Description    *string `json:"description"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.AddressLine1 == "" || req.City == "" || req.Country == "" ||
		req.AdminPocName == "" || req.AdminPocEmail == "" ||
		req.TechPocName == "" || req.TechPocEmail == "" ||
		req.AbusePocName == "" || req.AbusePocEmail == "" {
		httpx.Error(w, http.StatusBadRequest, "required fields missing (name, address_line1, city, country, admin/tech/abuse poc name+email)")
		return
	}
	out, err := h.Q.CreateOrganization(r.Context(), dbq.CreateOrganizationParams{
		Name: req.Name, ArinOrgID: req.ArinOrgID,
		AddressLine1: req.AddressLine1, AddressLine2: req.AddressLine2,
		City: req.City, StateProvince: req.StateProvince, PostalCode: req.PostalCode,
		Country: req.Country, Phone: req.Phone, Email: req.Email,
		AdminPocName: req.AdminPocName, AdminPocEmail: req.AdminPocEmail, AdminPocPhone: req.AdminPocPhone,
		TechPocName: req.TechPocName, TechPocEmail: req.TechPocEmail, TechPocPhone: req.TechPocPhone,
		AbusePocName: req.AbusePocName, AbusePocEmail: req.AbusePocEmail, AbusePocPhone: req.AbusePocPhone,
		NocPocName: req.NocPocName, NocPocEmail: req.NocPocEmail, NocPocPhone: req.NocPocPhone,
		Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "organization not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type updateReq struct {
	// Required-typed columns (caller passes nil to skip).
	Name          *string
	AddressLine1  *string
	City          *string
	Country       *string
	AdminPocName  *string
	AdminPocEmail *string
	TechPocName   *string
	TechPocEmail  *string
	AbusePocName  *string
	AbusePocEmail *string
	// Nullable columns w/ explicit-set tracking.
	ArinOrgID         *string
	arinSet           bool
	AddressLine2      *string
	addr2Set          bool
	StateProvince     *string
	stateSet          bool
	PostalCode        *string
	postalSet         bool
	Phone             *string
	phoneSet          bool
	Email             *string
	emailSet          bool
	AdminPocPhone     *string
	apocPhoneSet      bool
	TechPocPhone      *string
	tpocPhoneSet      bool
	AbusePocPhone     *string
	abpocPhoneSet     bool
	NocPocName        *string
	npocNameSet       bool
	NocPocEmail       *string
	npocEmailSet      bool
	NocPocPhone       *string
	npocPhoneSet      bool
	Description       *string
	descriptionSet    bool
}

func (u *updateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, dst := range map[string]any{
		"name": &u.Name, "address_line1": &u.AddressLine1, "city": &u.City,
		"country": &u.Country,
		"admin_poc_name": &u.AdminPocName, "admin_poc_email": &u.AdminPocEmail,
		"tech_poc_name": &u.TechPocName, "tech_poc_email": &u.TechPocEmail,
		"abuse_poc_name": &u.AbusePocName, "abuse_poc_email": &u.AbusePocEmail,
	} {
		if v, ok := raw[k]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	tracked := []struct {
		key string
		set *bool
		dst any
	}{
		{"arin_org_id", &u.arinSet, &u.ArinOrgID},
		{"address_line2", &u.addr2Set, &u.AddressLine2},
		{"state_province", &u.stateSet, &u.StateProvince},
		{"postal_code", &u.postalSet, &u.PostalCode},
		{"phone", &u.phoneSet, &u.Phone},
		{"email", &u.emailSet, &u.Email},
		{"admin_poc_phone", &u.apocPhoneSet, &u.AdminPocPhone},
		{"tech_poc_phone", &u.tpocPhoneSet, &u.TechPocPhone},
		{"abuse_poc_phone", &u.abpocPhoneSet, &u.AbusePocPhone},
		{"noc_poc_name", &u.npocNameSet, &u.NocPocName},
		{"noc_poc_email", &u.npocEmailSet, &u.NocPocEmail},
		{"noc_poc_phone", &u.npocPhoneSet, &u.NocPocPhone},
		{"description", &u.descriptionSet, &u.Description},
	}
	for _, t := range tracked {
		if v, ok := raw[t.key]; ok {
			*t.set = true
			_ = json.Unmarshal(v, t.dst)
		}
	}
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	var req updateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateOrganization(r.Context(), dbq.UpdateOrganizationParams{
		ID:             id,
		Name:           req.Name,
		ArinSet:        req.arinSet, ArinOrgID: req.ArinOrgID,
		AddressLine1:   req.AddressLine1,
		Addr2Set:       req.addr2Set, AddressLine2: req.AddressLine2,
		City:           req.City,
		StateSet:       req.stateSet, StateProvince: req.StateProvince,
		PostalSet:      req.postalSet, PostalCode: req.PostalCode,
		Country:        req.Country,
		PhoneSet:       req.phoneSet, Phone: req.Phone,
		EmailSet:       req.emailSet, Email: req.Email,
		AdminPocName:   req.AdminPocName, AdminPocEmail: req.AdminPocEmail,
		APocPhoneSet:   req.apocPhoneSet, AdminPocPhone: req.AdminPocPhone,
		TechPocName:    req.TechPocName, TechPocEmail: req.TechPocEmail,
		TPocPhoneSet:   req.tpocPhoneSet, TechPocPhone: req.TechPocPhone,
		AbusePocName:   req.AbusePocName, AbusePocEmail: req.AbusePocEmail,
		AbPocPhoneSet:  req.abpocPhoneSet, AbusePocPhone: req.AbusePocPhone,
		NPocNameSet:    req.npocNameSet, NocPocName: req.NocPocName,
		NPocEmailSet:   req.npocEmailSet, NocPocEmail: req.NocPocEmail,
		NPocPhoneSet:   req.npocPhoneSet, NocPocPhone: req.NocPocPhone,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "organization not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	if _, err := h.Q.GetOrganization(r.Context(), id); err != nil {
		mapErr(w, err, "organization not found")
		return
	}
	n, err := h.Q.CountAsnsForOrganization(r.Context(), id)
	if err != nil {
		mapErr(w, err, "organization not found")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict, "organization still owns ASNs; clear the FK first")
		return
	}
	if err := h.Q.DeleteOrganization(r.Context(), id); err != nil {
		mapErr(w, err, "organization not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
