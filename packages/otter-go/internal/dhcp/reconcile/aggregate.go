// Package reconcile cross-checks DhcpScope.reservations_json entries
// against the IPAddress rows in the linked subnet. Pure (no DB I/O):
// the HTTP handler does the SELECTs; this package classifies.
//
// Status taxonomy (Python services/dhcp_reconcile.py:1-32):
//   clean         — IP exists in IPAM with source=dhcp or source=
//                   reservation, identifier (mac/duid) matches if
//                   both sides declare one.
//   collision     — IP exists with source=static. An operator
//                   hand-allocated the same address; pushing the
//                   scope would hand out an already-claimed IP.
//   unbacked      — IP isn't in IPAM, OR scope.subnet_id is NULL.
//   mac_mismatch  — v4 reservation declares a MAC that differs from
//                   the lease's dhcp_mac on the same IP.
//   duid_mismatch — v6 reservation declares a DUID that differs from
//                   the lease's dhcp_duid on the same IP.
//
// Status string literals are byte-identical to Python so the wire
// shape (and any audit-stream consumer) reads identically after
// cutover.
package reconcile

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// Status string constants. Defined so callers can switch on them
// without retyping the literals.
type Status string

const (
	StatusClean         Status = "clean"
	StatusCollision     Status = "collision"
	StatusUnbacked      Status = "unbacked"
	StatusMacMismatch   Status = "mac_mismatch"
	StatusDuidMismatch  Status = "duid_mismatch"
)

// statusList pins the fixed-key tally Report.Counts emits — same
// shape Python's reconcile_scope returns at line 204. Dashboards
// reading counts.collision never see a missing key when the bucket
// is empty.
var statusList = []Status{StatusClean, StatusCollision, StatusUnbacked, StatusMacMismatch, StatusDuidMismatch}

// Entry mirrors Python's ReconcileEntry dataclass at line 49.
type Entry struct {
	ReservationIP string  `json:"reservation_ip"`
	Identifier    string  `json:"identifier"`
	Status        string  `json:"status"`
	IPAddressID   *string `json:"ip_address_id"`
	IPSource      *string `json:"ip_source"`
	Note          *string `json:"note"`
}

// Report mirrors Python's ReconcileReport dataclass at line 59.
type Report struct {
	ScopeID  string         `json:"scope_id"`
	SubnetID *string        `json:"subnet_id"`
	Total    int            `json:"total"`
	Counts   map[string]int `json:"counts"`
	Entries  []Entry        `json:"entries"`
}

// emptyCounts returns the fixed-key zero-fill — same shape Python
// hard-codes at services/dhcp_reconcile.py:204-207.
func emptyCounts() map[string]int {
	out := make(map[string]int, len(statusList))
	for _, s := range statusList {
		out[string(s)] = 0
	}
	return out
}

// staticSourceLiteral is the wire-form Python compares against at
// line 155 (`src == IpAddressSource.static.value`). Defined as a
// constant so a future enum rename surfaces here, not silently.
const staticSourceLiteral = "static"

// Reconcile classifies every reservation in scope.reservations_json
// against the IPAddress rows in the linked subnet. The handler loads
// the IP rows via ListIPAddressesInSubnetForReconcile; this function
// stays pure for unit testing.
//
// `subnetID` is the scope's subnet_id (nil when the scope has no
// subnet — every reservation is then bucketed as unbacked with an
// explanatory note, matching Python's behavior at line 148-150).
func Reconcile(scopeID uuid.UUID, subnetID *uuid.UUID, reservationsJSON json.RawMessage, ipRows []dbq.ListIPAddressesInSubnetForReconcileRow) Report {
	report := Report{
		ScopeID: scopeID.String(),
		Counts:  emptyCounts(),
		Entries: []Entry{},
	}
	if subnetID != nil {
		sid := subnetID.String()
		report.SubnetID = &sid
	}

	ipIndex := indexIPRowsByAddress(ipRows)
	reservations := decodeReservations(reservationsJSON)
	for _, r := range reservations {
		entry := classifyOne(r, ipIndex, subnetID != nil)
		report.Counts[entry.Status]++
		report.Entries = append(report.Entries, entry)
	}
	report.Total = len(report.Entries)
	return report
}

// indexIPRowsByAddress builds the map Python's ip_index dict
// equivalent (services/dhcp_reconcile.py:124-128). Address text is
// normalized — Postgres inet stores 10.0.0.5 / 10.0.0.5/32 / etc.;
// netip.ParseAddr collapses the host-form variants into the same
// string for comparison.
func indexIPRowsByAddress(rows []dbq.ListIPAddressesInSubnetForReconcileRow) map[string]dbq.ListIPAddressesInSubnetForReconcileRow {
	out := make(map[string]dbq.ListIPAddressesInSubnetForReconcileRow, len(rows))
	for _, row := range rows {
		// inet text form may carry "/PREFIX"; trim before parsing.
		addr := stripPrefix(row.Address)
		key, ok := normalizeIP(addr)
		if !ok {
			continue
		}
		out[key] = row
	}
	return out
}

// decodeReservations parses the JSON array into a slice of generic
// maps. A malformed payload is returned as nil — the handler
// shouldn't have shipped that scope through CREATE/PATCH because
// validateReservationsAgainstFamily would have caught it; a nil
// result here yields an empty report rather than a 500.
func decodeReservations(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// classifyOne walks the per-reservation matcher Python runs at
// services/dhcp_reconcile.py:131-202. Split out of Reconcile so the
// orchestrator stays under SonarCloud's cognitive-complexity ceiling.
func classifyOne(r map[string]any, ipIndex map[string]dbq.ListIPAddressesInSubnetForReconcileRow, hasSubnet bool) Entry {
	identifier := firstNonEmpty(stringField(r, "mac"), stringField(r, "duid"))
	rawIP := stringField(r, "ip")
	norm, parsable := normalizeIP(rawIP)
	if !parsable {
		return Entry{
			ReservationIP: rawIP, Identifier: identifier,
			Status: string(StatusUnbacked),
			Note:   ptrStr("reservation IP is not parseable"),
		}
	}
	match, found := ipIndex[norm]
	if !found {
		note := "no IPAddress row for this IP in the linked subnet"
		if !hasSubnet {
			note = "scope has no subnet_id — IPAM cross-check skipped"
		}
		return Entry{
			ReservationIP: norm, Identifier: identifier,
			Status: string(StatusUnbacked), Note: ptrStr(note),
		}
	}
	if match.Source == staticSourceLiteral {
		return Entry{
			ReservationIP: norm, Identifier: identifier,
			Status: string(StatusCollision),
			IPAddressID: ptrStr(match.ID.String()),
			IPSource:    ptrStr(match.Source),
			Note:        ptrStr("IPAddress is static — reservation would hand out an already-claimed IP"),
		}
	}
	if msg, ok := checkBindingMismatch(r, match); !ok {
		status := StatusMacMismatch
		if _, isDuid := r["duid"]; isDuid {
			status = StatusDuidMismatch
		}
		return Entry{
			ReservationIP: norm, Identifier: identifier,
			Status: string(status),
			IPAddressID: ptrStr(match.ID.String()),
			IPSource:    ptrStr(match.Source),
			Note:        ptrStr(msg),
		}
	}
	return Entry{
		ReservationIP: norm, Identifier: identifier,
		Status: string(StatusClean),
		IPAddressID: ptrStr(match.ID.String()),
		IPSource:    ptrStr(match.Source),
	}
}

// checkBindingMismatch returns (msg, false) when the v4 MAC or v6
// DUID on the reservation diverges from the lease's. Either-side-nil
// → skip (don't false-alarm on missing data). Mirrors Python at
// lines 163-194.
func checkBindingMismatch(r map[string]any, match dbq.ListIPAddressesInSubnetForReconcileRow) (string, bool) {
	if resMac, rowMac := normalizeMac(stringField(r, "mac")), normalizeMac(deref(match.DhcpMac)); resMac != "" && rowMac != "" && resMac != rowMac {
		return fmt.Sprintf("reservation expects mac=%s but IPAddress has mac=%s", resMac, rowMac), false
	}
	if resDuid, rowDuid := normalizeDuid(stringField(r, "duid")), normalizeDuid(deref(match.DhcpDuid)); resDuid != "" && rowDuid != "" && resDuid != rowDuid {
		return fmt.Sprintf("reservation expects duid=%s but IPAddress has duid=%s", resDuid, rowDuid), false
	}
	return "", true
}

// stripPrefix trims a "/N" suffix from an inet text-form address.
func stripPrefix(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// normalizeIP canonicalizes an IP string — Python uses
// ipaddress.ip_address which collapses 10.0.0.05 → 10.0.0.5 and
// 2001:db8::0001 → 2001:db8::1. netip.ParseAddr gives the same
// behavior for valid hosts; .String() emits the canonical form.
func normalizeIP(s string) (string, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	return addr.String(), true
}

// normalizeMac canonicalizes a MAC for comparison
// (services/dhcp_reconcile.py:78-91). Accepts colon/dash/dot/no-
// separator forms; lowercases hex; returns "" on bad input. The
// 12-hex-digit check rejects EUI-64 (16-hex) on purpose — IEEE 802
// MACs are 48-bit, EUI-64 belongs in dhcp_duid, not dhcp_mac.
func normalizeMac(mac string) string {
	cleaned := keepHex(strings.ToLower(mac))
	if len(cleaned) != 12 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(cleaned[i : i+2])
	}
	return b.String()
}

// normalizeDuid canonicalizes a DUID for comparison
// (services/dhcp_reconcile.py:94-112). RFC 8415 caps at 128 octets
// (256 hex chars); 1 octet (2 hex chars) is the minimum. Even-hex
// only since each octet is two hex digits.
func normalizeDuid(duid string) string {
	cleaned := keepHex(strings.ToLower(duid))
	if len(cleaned) < 2 || len(cleaned) > 256 || len(cleaned)%2 != 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(cleaned); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(cleaned[i : i+2])
	}
	return b.String()
}

// keepHex strips every non-hex character from s. Used by both
// normalizeMac and normalizeDuid to handle the colon/dash/dot/space
// variants operators paste in.
func keepHex(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// stringField fetches a string-typed value from the reservation map.
// JSON numbers/nulls/etc. resolve to "" so the matcher's "if mac …"
// branches behave the same as Python's `r.get("mac") or ""`.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ptrStr(s string) *string { return &s }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
