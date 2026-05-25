// Bulk subnet create (PR 67). Ports POST /ipam/subnets/bulk from
// api/ipam.py:780. Each row runs the same containment + ABAC +
// purpose checks as the single-row create. Failures isolate to
// that row — the rest of the batch still commits.
//
// Skip vs fail semantics match the Python: (vrf, prefix) uniqueness
// conflicts go to `skipped` (re-run of the same CSV is idempotent);
// any other error lands in `errors[]` with a row-keyed message.
package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// bulkResult mirrors schemas.common.BulkResult so the wire shape is
// identical across backends. `updated` is included for parity with
// the Python field set even though bulk-create never updates.
type bulkResult struct {
	Inserted int              `json:"inserted"`
	Updated  int              `json:"updated"`
	Skipped  int              `json:"skipped"`
	Failed   int              `json:"failed"`
	Errors   []map[string]any `json:"errors"`
}

// bulkSubnetRow accepts the same fields as the single-row
// subnetCreateReq plus the JSON tags the Python BulkResult schema
// echoes back in `errors[].prefix` / `.supernet_id` references.
type bulkSubnetRow struct {
	SupernetID  uuid.UUID  `json:"supernet_id"`
	Prefix      string     `json:"prefix"`
	SiteID      *uuid.UUID `json:"site_id"`
	VniID       *uuid.UUID `json:"vni_id"`
	Name        *string    `json:"name"`
	Description *string    `json:"description"`
	Purpose     *string    `json:"purpose"`
	VlanID      *int32     `json:"vlan_id"`
	Gateway     *string    `json:"gateway"`
}

// isUniqueViolation returns true for pgx unique-constraint
// failures (Postgres SQLSTATE 23505). Bulk treats those as skip,
// not fail — matches the Python ConflictError → skipped path.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func (h *Handler) bulkCreateSubnets(w http.ResponseWriter, r *http.Request) {
	var rows []bulkSubnetRow
	if err := json.NewDecoder(r.Body).Decode(&rows); err != nil {
		httpx.Error(w, http.StatusBadRequest, "payload must be a JSON array")
		return
	}
	out := bulkResult{Errors: []map[string]any{}}
	for i, row := range rows {
		h.bulkSubnetRow(r.Context(), r, i, row, &out)
	}
	httpx.JSON(w, http.StatusOK, out)
}

// bulkSubnetRow runs the per-row validation + insert. Split out from
// the loop so the per-row error-recording is one place — keeps the
// loop body small and the cognitive complexity in line with the
// codebase posture.
func (h *Handler) bulkSubnetRow(
	ctx context.Context, r *http.Request,
	i int, row bulkSubnetRow, out *bulkResult,
) {
	addErr := func(msg string) {
		out.Failed++
		out.Errors = append(out.Errors, map[string]any{
			"row":         i,
			"prefix":      row.Prefix,
			"supernet_id": row.SupernetID.String(),
			"error":       msg,
		})
	}
	if row.SupernetID == uuid.Nil || row.Prefix == "" {
		addErr("supernet_id and prefix required")
		return
	}
	prefix, err := parseCIDR(row.Prefix)
	if err != nil {
		addErr(err.Error())
		return
	}
	supernet, err := h.assertSubnetInsideSupernet(ctx, row.SupernetID, prefix)
	if err != nil {
		addErr(err.Error())
		return
	}
	// ABAC fabric-scope check per row, using the bulk capability
	// (matches Python's _CAP_BULK = "ipam:bulk:execute"). A row
	// whose parent fabric is outside the caller's scope fails just
	// that row, not the whole batch.
	p, _ := auth.From(ctx)
	if err := auth.EnforceFabricScope(p, supernet.FabricID, "ipam:bulk:execute"); err != nil {
		addErr(err.Error())
		return
	}
	if perr := validatePurposeCompatible(supernet.Purpose, row.Purpose); perr != nil {
		addErr(perr.Error())
		return
	}
	_, err = h.Q.CreateSubnet(ctx, dbq.CreateSubnetParams{
		SupernetID:  row.SupernetID,
		FabricID:    supernet.FabricID,
		VrfID:       supernet.VrfID,
		SiteID:      row.SiteID,
		VniID:       row.VniID,
		Prefix:      row.Prefix,
		Name:        row.Name,
		Description: row.Description,
		Purpose:     row.Purpose,
		VlanID:      row.VlanID,
		Gateway:     row.Gateway,
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
