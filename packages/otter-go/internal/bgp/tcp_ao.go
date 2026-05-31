// TCP AO key-chains CRUD handlers. Live in production since the
// ingress cutover landed alongside keys + rotate-batch (PR 2);
// before that, these routes were dark code only exercised by Go
// tests.
package bgp

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const tcpAoChainNotFound = "tcp ao key chain not found"

type tcpAoChainsPage struct {
	Items  []dbq.TcpAoKeyChain `json:"items"`
	Total  int64               `json:"total"`
	Limit  int32               `json:"limit"`
	Offset int32               `json:"offset"`
}

func (h *Handler) listTcpAoKeyChains(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListTcpAoKeyChains(r.Context(), dbq.ListTcpAoKeyChainsParams{
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	if items == nil {
		items = []dbq.TcpAoKeyChain{}
	}
	total, err := h.Q.CountTcpAoKeyChains(r.Context())
	if err != nil {
		writeMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tcpAoChainsPage{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

func (h *Handler) getTcpAoKeyChain(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	chain, err := h.Q.GetTcpAoKeyChain(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoChainNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, chain)
}

type tcpAoChainCreateReq struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (h *Handler) createTcpAoKeyChain(w http.ResponseWriter, r *http.Request) {
	var req tcpAoChainCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		httpx.Error(w, http.StatusBadRequest, "name required")
		return
	}
	out, err := h.Q.CreateTcpAoKeyChain(r.Context(), dbq.CreateTcpAoKeyChainParams{
		Name: req.Name, Description: req.Description,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "tcp_ao_key_chain.create", TargetType: "tcp_ao_key_chain",
		TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// tcpAoChainUpdateReq uses the (set, value) flag pattern on the
// nullable description so {"description": null} clears the row,
// matching Pydantic exclude_unset=True semantics on the Python side.
type tcpAoChainUpdateReq struct {
	Name           *string `json:"name,omitempty"`
	Description    *string `json:"description,omitempty"`
	descriptionSet bool
}

func (u *tcpAoChainUpdateReq) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &u.Name); err != nil {
			return err
		}
	}
	if v, ok := raw["description"]; ok {
		u.descriptionSet = true
		if err := json.Unmarshal(v, &u.Description); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) updateTcpAoKeyChain(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetTcpAoKeyChain(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoChainNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	var req tcpAoChainUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	out, err := h.Q.UpdateTcpAoKeyChain(r.Context(), dbq.UpdateTcpAoKeyChainParams{
		ID: id, Name: req.Name,
		DescriptionSet: req.descriptionSet, Description: req.Description,
	})
	if err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "tcp_ao_key_chain.update", TargetType: "tcp_ao_key_chain",
		TargetID: id.String(), Diff: tcpAoChainDiff(req),
	})
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) deleteTcpAoKeyChain(w http.ResponseWriter, r *http.Request) {
	id, ok := parseTcpAoID(w, r)
	if !ok {
		return
	}
	if _, err := h.Q.GetTcpAoKeyChain(r.Context(), id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, tcpAoChainNotFound)
			return
		}
		writeMapped(w, err)
		return
	}
	// Refuse to drop a chain that still has keys so rotation history
	// stays intentional. Mirrors Python's ConflictError; httpx.Mapped
	// translates the sentinel to 409.
	n, err := h.Q.CountKeysInTcpAoKeyChain(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	if n > 0 {
		writeMapped(w, httpx.ErrFKViolation)
		return
	}
	if err := h.Q.DeleteTcpAoKeyChain(r.Context(), id); err != nil {
		writeMapped(w, err)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "tcp_ao_key_chain.delete", TargetType: "tcp_ao_key_chain",
		TargetID: id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}

func tcpAoChainDiff(req tcpAoChainUpdateReq) map[string]any {
	d := map[string]any{}
	if req.Name != nil {
		d["name"] = *req.Name
	}
	if req.descriptionSet {
		d["description"] = req.Description
	}
	return d
}

func parseTcpAoID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func writeMapped(w http.ResponseWriter, err error) {
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}
