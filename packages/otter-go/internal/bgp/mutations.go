// BGP policy mutations (PR 44). Basic CRUD for asns, prefix lists,
// community lists, route maps, and their entry tables. TCP AO and the
// /asns/bulk-rotate action are intentionally deferred (TCP AO is
// security-sensitive; bulk-rotate is its own concern).
package bgp

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

func idFromURL(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func mapErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, notFoundMsg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// ---- ASNs ----

type asnCreateReq struct {
	Asn            int64      `json:"asn"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	OrganizationID *uuid.UUID `json:"organization_id"`
	Description    *string    `json:"description"`
}

func (h *Handler) createAsn(w http.ResponseWriter, r *http.Request) {
	var req asnCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.Name == "" || req.Kind == "" || req.Asn == 0 {
		httpx.Error(w, http.StatusBadRequest, "asn, name, kind required")
		return
	}
	out, err := h.Q.CreateAsn(r.Context(), dbq.CreateAsnParams{
		Asn: req.Asn, Name: req.Name, Kind: req.Kind,
		OrganizationID: req.OrganizationID, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "asn not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type asnUpdateReq struct {
	Name           *string
	Kind           *string
	OrganizationID *uuid.UUID
	orgSet         bool
	Description    *string
	descriptionSet bool
}

func (u *asnUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["kind"]; ok {
		_ = json.Unmarshal(v, &u.Kind)
	}
	if v, ok := raw["organization_id"]; ok {
		u.orgSet = true
		_ = json.Unmarshal(v, &u.OrganizationID)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateAsn(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req asnUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateAsn(r.Context(), dbq.UpdateAsnParams{
		ID: id, Name: req.Name, Kind: req.Kind,
		OrgSet: req.orgSet, OrganizationID: req.OrganizationID,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "asn not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteAsn(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteAsn(r.Context(), id); err != nil {
		mapErr(w, err, "asn not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Prefix lists ----

type plCreateReq struct {
	Name        string  `json:"name"`
	Family      string  `json:"family"`
	Description *string `json:"description"`
}

func (h *Handler) createPrefixList(w http.ResponseWriter, r *http.Request) {
	var req plCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Family == "" {
		httpx.Error(w, http.StatusBadRequest, "name and family required")
		return
	}
	out, err := h.Q.CreatePrefixList(r.Context(), dbq.CreatePrefixListParams{
		Name: req.Name, Family: req.Family, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "prefix list not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type plUpdateReq struct {
	Name           *string
	Family         *string
	Description    *string
	descriptionSet bool
}

func (u *plUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["family"]; ok {
		_ = json.Unmarshal(v, &u.Family)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updatePrefixList(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req plUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdatePrefixList(r.Context(), dbq.UpdatePrefixListParams{
		ID: id, Name: req.Name, Family: req.Family,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "prefix list not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deletePrefixList(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeletePrefixList(r.Context(), id); err != nil {
		mapErr(w, err, "prefix list not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Prefix list entries ----

type pleCreateReq struct {
	PrefixListID uuid.UUID `json:"prefix_list_id"`
	Seq          int32     `json:"seq"`
	Action       string    `json:"action"`
	Prefix       string    `json:"prefix"`
	Ge           *int32    `json:"ge"`
	Le           *int32    `json:"le"`
	Description  *string   `json:"description"`
}

func (h *Handler) createPrefixListEntry(w http.ResponseWriter, r *http.Request) {
	var req pleCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.PrefixListID == uuid.Nil || req.Action == "" || req.Prefix == "" {
		httpx.Error(w, http.StatusBadRequest, "prefix_list_id, action, prefix required")
		return
	}
	out, err := h.Q.CreatePrefixListEntry(r.Context(), dbq.CreatePrefixListEntryParams{
		PrefixListID: req.PrefixListID, Seq: req.Seq, Action: req.Action,
		Prefix: req.Prefix, Ge: req.Ge, Le: req.Le, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type pleUpdateReq struct {
	Seq            *int32
	Action         *string
	Prefix         *string
	Ge             *int32
	geSet          bool
	Le             *int32
	leSet          bool
	Description    *string
	descriptionSet bool
}

func (u *pleUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["seq"]; ok {
		_ = json.Unmarshal(v, &u.Seq)
	}
	if v, ok := raw["action"]; ok {
		_ = json.Unmarshal(v, &u.Action)
	}
	if v, ok := raw["prefix"]; ok {
		_ = json.Unmarshal(v, &u.Prefix)
	}
	if v, ok := raw["ge"]; ok {
		u.geSet = true
		_ = json.Unmarshal(v, &u.Ge)
	}
	if v, ok := raw["le"]; ok {
		u.leSet = true
		_ = json.Unmarshal(v, &u.Le)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updatePrefixListEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req pleUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdatePrefixListEntry(r.Context(), dbq.UpdatePrefixListEntryParams{
		ID: id, Seq: req.Seq, Action: req.Action, Prefix: req.Prefix,
		GeSet: req.geSet, Ge: req.Ge, LeSet: req.leSet, Le: req.Le,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deletePrefixListEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeletePrefixListEntry(r.Context(), id); err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Community lists ----

type clCreateReq struct {
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Description *string `json:"description"`
}

func (h *Handler) createCommunityList(w http.ResponseWriter, r *http.Request) {
	var req clCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name required")
		return
	}
	if req.Kind == "" {
		req.Kind = "standard"
	}
	out, err := h.Q.CreateCommunityList(r.Context(), dbq.CreateCommunityListParams{
		Name: req.Name, Kind: req.Kind, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "community list not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type clUpdateReq struct {
	Name           *string
	Kind           *string
	Description    *string
	descriptionSet bool
}

func (u *clUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["kind"]; ok {
		_ = json.Unmarshal(v, &u.Kind)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateCommunityList(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req clUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateCommunityList(r.Context(), dbq.UpdateCommunityListParams{
		ID: id, Name: req.Name, Kind: req.Kind,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "community list not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteCommunityList(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteCommunityList(r.Context(), id); err != nil {
		mapErr(w, err, "community list not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Community list entries ----

type cleCreateReq struct {
	CommunityListID uuid.UUID `json:"community_list_id"`
	Seq             int32     `json:"seq"`
	Action          string    `json:"action"`
	Value           string    `json:"value"`
	Description     *string   `json:"description"`
}

func (h *Handler) createCommunityListEntry(w http.ResponseWriter, r *http.Request) {
	var req cleCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.CommunityListID == uuid.Nil || req.Action == "" || req.Value == "" {
		httpx.Error(w, http.StatusBadRequest, "community_list_id, action, value required")
		return
	}
	out, err := h.Q.CreateCommunityListEntry(r.Context(), dbq.CreateCommunityListEntryParams{
		CommunityListID: req.CommunityListID, Seq: req.Seq, Action: req.Action,
		Value: req.Value, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type cleUpdateReq struct {
	Seq            *int32
	Action         *string
	Value          *string
	Description    *string
	descriptionSet bool
}

func (u *cleUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["seq"]; ok {
		_ = json.Unmarshal(v, &u.Seq)
	}
	if v, ok := raw["action"]; ok {
		_ = json.Unmarshal(v, &u.Action)
	}
	if v, ok := raw["value"]; ok {
		_ = json.Unmarshal(v, &u.Value)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateCommunityListEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req cleUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateCommunityListEntry(r.Context(), dbq.UpdateCommunityListEntryParams{
		ID: id, Seq: req.Seq, Action: req.Action, Value: req.Value,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteCommunityListEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteCommunityListEntry(r.Context(), id); err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Route maps ----

type rmCreateReq struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (h *Handler) createRouteMap(w http.ResponseWriter, r *http.Request) {
	var req rmCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name required")
		return
	}
	out, err := h.Q.CreateRouteMap(r.Context(), dbq.CreateRouteMapParams{
		Name: req.Name, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "route map not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type rmUpdateReq struct {
	Name           *string
	Description    *string
	descriptionSet bool
}

func (u *rmUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &u.Name)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateRouteMap(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req rmUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateRouteMap(r.Context(), dbq.UpdateRouteMapParams{
		ID: id, Name: req.Name,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "route map not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRouteMap(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteRouteMap(r.Context(), id); err != nil {
		mapErr(w, err, "route map not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Route map entries ----

type rmeCreateReq struct {
	RouteMapID           uuid.UUID  `json:"route_map_id"`
	Seq                  int32      `json:"seq"`
	Action               string     `json:"action"`
	MatchPrefixListID    *uuid.UUID `json:"match_prefix_list_id"`
	MatchCommunityListID *uuid.UUID `json:"match_community_list_id"`
	MatchAsPathRegex     *string    `json:"match_as_path_regex"`
	SetLocalPref         *int32     `json:"set_local_pref"`
	SetMed               *int32     `json:"set_med"`
	SetCommunity         *string    `json:"set_community"`
	Description          *string    `json:"description"`
}

func (h *Handler) createRouteMapEntry(w http.ResponseWriter, r *http.Request) {
	var req rmeCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
		req.RouteMapID == uuid.Nil || req.Action == "" {
		httpx.Error(w, http.StatusBadRequest, "route_map_id and action required")
		return
	}
	out, err := h.Q.CreateRouteMapEntry(r.Context(), dbq.CreateRouteMapEntryParams{
		RouteMapID: req.RouteMapID, Seq: req.Seq, Action: req.Action,
		MatchPrefixListID: req.MatchPrefixListID, MatchCommunityListID: req.MatchCommunityListID,
		MatchAsPathRegex: req.MatchAsPathRegex,
		SetLocalPref:     req.SetLocalPref, SetMed: req.SetMed, SetCommunity: req.SetCommunity,
		Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type rmeUpdateReq struct {
	Seq                  *int32
	Action               *string
	MatchPrefixListID    *uuid.UUID
	mplSet               bool
	MatchCommunityListID *uuid.UUID
	mclSet               bool
	MatchAsPathRegex     *string
	aspSet               bool
	SetLocalPref         *int32
	slpSet               bool
	SetMed               *int32
	medSet               bool
	SetCommunity         *string
	scSet                bool
	Description          *string
	descriptionSet       bool
}

func (u *rmeUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["seq"]; ok {
		_ = json.Unmarshal(v, &u.Seq)
	}
	if v, ok := raw["action"]; ok {
		_ = json.Unmarshal(v, &u.Action)
	}
	if v, ok := raw["match_prefix_list_id"]; ok {
		u.mplSet = true
		_ = json.Unmarshal(v, &u.MatchPrefixListID)
	}
	if v, ok := raw["match_community_list_id"]; ok {
		u.mclSet = true
		_ = json.Unmarshal(v, &u.MatchCommunityListID)
	}
	if v, ok := raw["match_as_path_regex"]; ok {
		u.aspSet = true
		_ = json.Unmarshal(v, &u.MatchAsPathRegex)
	}
	if v, ok := raw["set_local_pref"]; ok {
		u.slpSet = true
		_ = json.Unmarshal(v, &u.SetLocalPref)
	}
	if v, ok := raw["set_med"]; ok {
		u.medSet = true
		_ = json.Unmarshal(v, &u.SetMed)
	}
	if v, ok := raw["set_community"]; ok {
		u.scSet = true
		_ = json.Unmarshal(v, &u.SetCommunity)
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		_ = json.Unmarshal(v, &u.Description)
	}
	return nil
}

func (h *Handler) updateRouteMapEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	var req rmeUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateRouteMapEntry(r.Context(), dbq.UpdateRouteMapEntryParams{
		ID: id, Seq: req.Seq, Action: req.Action,
		MPLSet: req.mplSet, MatchPrefixListID: req.MatchPrefixListID,
		MCLSet: req.mclSet, MatchCommunityListID: req.MatchCommunityListID,
		ASPSet: req.aspSet, MatchAsPathRegex: req.MatchAsPathRegex,
		SLPSet: req.slpSet, SetLocalPref: req.SetLocalPref,
		MedSet: req.medSet, SetMed: req.SetMed,
		SCSet:  req.scSet, SetCommunity: req.SetCommunity,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRouteMapEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if err := h.Q.DeleteRouteMapEntry(r.Context(), id); err != nil {
		mapErr(w, err, "entry not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
