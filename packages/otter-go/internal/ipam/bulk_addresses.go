// Bulk IP address create (PR 68). Ports POST /ipam/addresses/bulk
// from api/ipam.py:1000. Each row runs the same subnet-containment
// + ABAC checks as the single-row create; uniqueness conflicts go
// to `skipped` (idempotent re-runs), other failures to `errors[]`.
package ipam

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// bulkAddressRow accepts the same fields as the single-row
// addressCreateReq. role/status/source default to the same values
// the single-row handler uses so a bulk import without those fields
// behaves identically to N single POSTs.
type bulkAddressRow struct {
	SubnetID    uuid.UUID  `json:"subnet_id"`
	AssetID     *uuid.UUID `json:"asset_id"`
	Address     string     `json:"address"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	Source      string     `json:"source"`
	DnsName     *string    `json:"dns_name"`
	Description *string    `json:"description"`
}

func (h *Handler) bulkCreateAddresses(w http.ResponseWriter, r *http.Request) {
	var rows []bulkAddressRow
	if err := json.NewDecoder(r.Body).Decode(&rows); err != nil {
		httpx.Error(w, http.StatusBadRequest, "payload must be a JSON array")
		return
	}
	out := bulkResult{Errors: []map[string]any{}}
	for i, row := range rows {
		h.bulkAddressRow(r.Context(), i, row, &out)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) bulkAddressRow(
	ctx context.Context, i int, row bulkAddressRow, out *bulkResult,
) {
	addErr := func(msg string) {
		out.Failed++
		out.Errors = append(out.Errors, map[string]any{
			"row":       i,
			"address":   row.Address,
			"subnet_id": row.SubnetID.String(),
			"error":     msg,
		})
	}
	if row.SubnetID == uuid.Nil || row.Address == "" {
		addErr("subnet_id and address required")
		return
	}
	addr, err := parseAddr(row.Address)
	if err != nil {
		addErr(err.Error())
		return
	}
	subnet, err := h.assertAddressInSubnet(ctx, row.SubnetID, addr)
	if err != nil {
		addErr(err.Error())
		return
	}
	// Per-row ABAC, gated on the shared bulk capability — matches
	// Python's _CAP_BULK = "ipam:bulk:execute". Cross-fabric rows
	// fail just their own row, not the batch.
	p, _ := auth.From(ctx)
	if err := auth.EnforceFabricScope(p, subnet.FabricID, "ipam:bulk:execute"); err != nil {
		addErr(err.Error())
		return
	}
	role := row.Role
	if role == "" {
		role = "data"
	}
	status := row.Status
	if status == "" {
		status = "active"
	}
	source := row.Source
	if source == "" {
		source = "static"
	}
	_, err = h.Q.CreateIPAddress(ctx, dbq.CreateIPAddressParams{
		SubnetID:    row.SubnetID,
		AssetID:     row.AssetID,
		Address:     row.Address,
		Role:        role,
		Status:      status,
		Source:      source,
		DnsName:     row.DnsName,
		Description: row.Description,
	})
	if err != nil {
		if isUniqueViolation(err) {
			out.Skipped++
			return
		}
		addErr(err.Error())
		return
	}
	out.Inserted++
}
