// Computed utilization endpoints (PR 63+). Ports the supernet/subnet
// utilization views from packages/otter Python (services/ipam.py:
// network_capacity + api/ipam.py supernet_utilization). Pure CIDR
// arithmetic on top of a single SELECT per call — no joins, no
// fan-out.
package ipam

import (
	"errors"
	"math"
	"net/http"
	"net/netip"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// int64Max caps very wide IPv6 capacities so the JSON response is
// representable. Mirrors _INT64_MAX in services/ipam.py.
const int64Max = math.MaxInt64

// networkCapacity returns the number of allocatable host addresses
// in a CIDR network, capped at int64. Mirrors Python's
// services.ipam.network_capacity:
//
//   - /31 and /127 are point-to-point — count both addresses (2)
//   - /32 and /128 are host routes — count exactly 1
//   - otherwise subtract network + broadcast (2)
//   - clamp to int64Max for very wide v6
//
// Returns an error if the prefix is not a valid CIDR.
func networkCapacity(prefix string) (int64, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(prefix))
	if err != nil {
		return 0, err
	}
	bits := p.Bits()
	totalBits := p.Addr().BitLen() // 32 for v4, 128 for v6
	hostBits := totalBits - bits
	// /31, /127, /32, /128: full address count, no network/broadcast
	// reduction. Anything else loses 2 for those reserved addresses.
	if p.Addr().Is4() && bits >= 31 || !p.Addr().Is4() && bits >= 127 {
		// 2^hostBits, capped
		if hostBits >= 63 {
			return int64Max, nil
		}
		return int64(1) << hostBits, nil
	}
	if hostBits >= 63 {
		return int64Max, nil
	}
	n := int64(1) << hostBits
	n -= 2
	if n < 0 {
		return 0, nil
	}
	return n, nil
}

// supernetUtilization is the response shape for
// GET /ipam/supernets/{id}/utilization. Mirrors the Python handler's
// JSON keys (and float-with-2-decimal percent semantics) so the
// frontend works against either backend.
type supernetUtilization struct {
	SupernetID               string  `json:"supernet_id"`
	Prefix                   string  `json:"prefix"`
	Capacity                 int64   `json:"capacity"`
	AllocatedSubnetAddresses int64   `json:"allocated_subnet_addresses"`
	Free                     int64   `json:"free"`
	Percent                  float64 `json:"percent"`
	SubnetCount              int     `json:"subnet_count"`
}

func (h *Handler) getSupernetUtilization(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	sn, err := h.Q.GetSupernet(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "supernet not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	prefixes, err := h.Q.ListSubnetPrefixesBySupernet(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	capacity, err := networkCapacity(sn.Prefix)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "supernet prefix unparseable")
		return
	}
	var allocated int64
	for _, p := range prefixes {
		c, err := networkCapacity(p)
		if err != nil {
			// Skip un-parseable subnet rows rather than failing the
			// whole utilization read; the LIST endpoint will surface
			// the bad row separately.
			continue
		}
		// Saturating add: if the supernet is a wide v6 and we hit
		// int64Max, clamp instead of wrapping.
		if allocated > int64Max-c {
			allocated = int64Max
			break
		}
		allocated += c
	}
	free := capacity - allocated
	if free < 0 {
		free = 0
	}
	pct := 0.0
	if capacity > 0 {
		// round(100 * allocated / capacity, 2) — mirrors Python's
		// services.ipam supernet_utilization formula so the response
		// matches across backends to the second decimal.
		pct = math.Round(10000.0*float64(allocated)/float64(capacity)) / 100.0
	}
	httpx.JSON(w, http.StatusOK, supernetUtilization{
		SupernetID:               id.String(),
		Prefix:                   sn.Prefix,
		Capacity:                 capacity,
		AllocatedSubnetAddresses: allocated,
		Free:                     free,
		Percent:                  pct,
		SubnetCount:              len(prefixes),
	})
}
