// Mutating reconcile sync: materializes DhcpScope reservations into
// IPAM (Python services/dhcp_reconcile.py:sync_reservations).
//
// Per-reservation decisions:
//   upserted               — IP not in IPAM → INSERT source=reservation
//   promoted               — IP exists as source=dhcp → flip to
//                            source=reservation, backfill mac/duid/
//                            dns_name when the row's column is NULL
//   skipped_collision      — IP exists as source=static; operator-
//                            owned; leave alone
//   skipped_clean          — IP exists as source=reservation already
//   skipped_mac_mismatch   — v4 reservation MAC ≠ lease dhcp_mac;
//                            refuse to promote (masks the conflict)
//   skipped_duid_mismatch  — v6 reservation DUID ≠ lease dhcp_duid;
//                            same posture
//   skipped_no_subnet      — scope.subnet_id is NULL; nothing to
//                            insert against
//   skipped_unparseable    — reservation IP isn't a valid IP literal
//
// Pure-ish: the orchestrator takes a Writer interface for the two
// SQL operations so the unit suite stands up an in-memory fake.
// The HTTP handler wires *dbq.Queries.

package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// Writer is the slim DB surface Sync mutates against. *dbq.Queries
// satisfies it.
type Writer interface {
	InsertReservationIPAddress(ctx context.Context, arg dbq.InsertReservationIPAddressParams) (uuid.UUID, error)
	PromoteDhcpLeaseToReservation(ctx context.Context, arg dbq.PromoteDhcpLeaseToReservationParams) error
}

// SyncReport mirrors Python's SyncReport dataclass at services/
// dhcp_reconcile.py:223. Entries carries the per-reservation
// decision payload the audit log + the HTTP response both consume.
type SyncReport struct {
	ScopeID             string           `json:"scope_id"`
	SubnetID            *string          `json:"subnet_id"`
	Upserted            int              `json:"upserted"`
	Promoted            int              `json:"promoted"`
	SkippedCollision    int              `json:"skipped_collision"`
	SkippedClean        int              `json:"skipped_clean"`
	SkippedMacMismatch  int              `json:"skipped_mac_mismatch"`
	SkippedDuidMismatch int              `json:"skipped_duid_mismatch"`
	SkippedNoSubnet     int              `json:"skipped_no_subnet"`
	Entries             []map[string]any `json:"entries"`
}

// Sync materializes scope.reservations_json into IPAM. The scope's
// reservations + the per-subnet IP rows are loaded by the caller;
// this function classifies each reservation and runs the
// appropriate INSERT or UPDATE.
//
// `subnetID` nil → every reservation lands in skipped_no_subnet
// with `decision: skipped_no_subnet`. Matches Python at
// services/dhcp_reconcile.py:259-270.
func Sync(
	ctx context.Context,
	w Writer,
	scopeID uuid.UUID,
	subnetID *uuid.UUID,
	reservationsJSON json.RawMessage,
	ipRows []dbq.DhcpReconcileIPRow,
) (SyncReport, error) {
	report := SyncReport{ScopeID: scopeID.String(), Entries: []map[string]any{}}
	if subnetID != nil {
		sid := subnetID.String()
		report.SubnetID = &sid
	}
	reservations := decodeReservations(reservationsJSON)
	if subnetID == nil {
		report.SkippedNoSubnet = len(reservations)
		for _, r := range reservations {
			report.Entries = append(report.Entries, map[string]any{
				"reservation_ip": stringField(r, "ip"),
				"decision":       "skipped_no_subnet",
			})
		}
		return report, nil
	}

	ipIndex := indexIPRowsByAddress(ipRows)
	for _, r := range reservations {
		entry, err := syncOne(ctx, w, *subnetID, r, ipIndex)
		if err != nil {
			return SyncReport{}, err
		}
		report.bump(entry["decision"].(string))
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}

// bump updates the counter the per-entry decision lands in. Unknown
// decisions are silently ignored — the function is the single
// emitter so a typo would be caught at compile time.
func (r *SyncReport) bump(decision string) {
	switch decision {
	case "upserted":
		r.Upserted++
	case "promoted":
		r.Promoted++
	case "skipped_collision":
		r.SkippedCollision++
	case "skipped_clean":
		r.SkippedClean++
	case "skipped_mac_mismatch":
		r.SkippedMacMismatch++
	case "skipped_duid_mismatch":
		r.SkippedDuidMismatch++
	}
}

// syncOne is the per-reservation orchestrator. Returns the decision
// entry the report appends. Unparseable IPs short-circuit before
// the matcher; otherwise the matcher walks the same taxonomy as
// reconcile_scope, this time with side effects.
func syncOne(
	ctx context.Context,
	w Writer,
	subnetID uuid.UUID,
	r map[string]any,
	ipIndex map[string]dbq.DhcpReconcileIPRow,
) (map[string]any, error) {
	rawIP := stringField(r, "ip")
	norm, parsable := normalizeIP(rawIP)
	if !parsable {
		return map[string]any{
			"reservation_ip": rawIP, "decision": "skipped_unparseable",
		}, nil
	}
	resMac := nilIfEmpty(normalizeMac(stringField(r, "mac")))
	resDuid := nilIfEmpty(normalizeDuid(stringField(r, "duid")))
	resHostname := nilIfEmpty(strings.TrimSpace(stringField(r, "hostname")))

	match, found := ipIndex[norm]
	if !found {
		id, err := w.InsertReservationIPAddress(ctx, dbq.InsertReservationIPAddressParams{
			SubnetID: subnetID, Address: norm,
			DhcpMac: resMac, DhcpDuid: resDuid, DnsName: resHostname,
		})
		if err != nil {
			return nil, fmt.Errorf("insert reservation %s: %w", norm, err)
		}
		return map[string]any{
			"reservation_ip": norm, "decision": "upserted",
			"ip_address_id": id.String(),
		}, nil
	}
	if match.Source == staticSourceLiteral {
		return map[string]any{
			"reservation_ip": norm, "decision": "skipped_collision",
			"ip_address_id": match.ID.String(),
		}, nil
	}
	if entry, ok := mismatchEntry(r, match, norm); ok {
		return entry, nil
	}
	if match.Source == "dhcp" {
		err := w.PromoteDhcpLeaseToReservation(ctx, dbq.PromoteDhcpLeaseToReservationParams{
			ID: match.ID, DhcpMac: resMac, DhcpDuid: resDuid, DnsName: resHostname,
		})
		if err != nil {
			return nil, fmt.Errorf("promote ip %s: %w", match.ID, err)
		}
		return map[string]any{
			"reservation_ip": norm, "decision": "promoted",
			"ip_address_id": match.ID.String(),
		}, nil
	}
	// source=reservation already — nothing to do.
	return map[string]any{
		"reservation_ip": norm, "decision": "skipped_clean",
		"ip_address_id": match.ID.String(),
	}, nil
}

// mismatchEntry detects a MAC or DUID divergence between the
// reservation and the lease. Returns (entry, true) when the
// reservation must be SKIPPED rather than promoted — promoting
// silently would mask the binding conflict (Python comment at
// services/dhcp_reconcile.py:329-348).
func mismatchEntry(r map[string]any, match dbq.DhcpReconcileIPRow, norm string) (map[string]any, bool) {
	resMac := normalizeMac(stringField(r, "mac"))
	rowMac := normalizeMac(deref(match.DhcpMac))
	if resMac != "" && rowMac != "" && resMac != rowMac {
		return map[string]any{
			"reservation_ip": norm, "decision": "skipped_mac_mismatch",
			"ip_address_id":   match.ID.String(),
			"reservation_mac": resMac, "row_mac": rowMac,
		}, true
	}
	resDuid := normalizeDuid(stringField(r, "duid"))
	rowDuid := normalizeDuid(deref(match.DhcpDuid))
	if resDuid != "" && rowDuid != "" && resDuid != rowDuid {
		return map[string]any{
			"reservation_ip": norm, "decision": "skipped_duid_mismatch",
			"ip_address_id":    match.ID.String(),
			"reservation_duid": resDuid, "row_duid": rowDuid,
		}, true
	}
	return nil, false
}

// nilIfEmpty resolves "" → nil so the SQL params write NULL rather
// than an empty-string identifier (the normalizers return "" on
// invalid input).
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
