// PR 83 — POST /dns/zones/{id}/sync-from-ipam.
//
// Rebuilds source=ipam (and source=ddns) records for a site zone +
// every derived reverse zone. Operator runs this when IPAM rows
// change in ways the live triggers don't catch (bulk import, hand
// edit). The Go scheduler job at internal/scheduler/jobs/dnssync
// (Python's `dns_sync_from_ipam` arq cron) calls the same service
// helper every 5 min to keep the projection fresh — drift bound is
// therefore ~5 min, not 60s as the original arq cadence was.
//
// Partial-failure note: the existing Go codebase doesn't expose
// pgx transactions on the Querier interface. The drop + bulk
// insert here runs as sequential statements — a connection drop
// mid-write leaves the zone half-rebuilt. Recovery is idempotent:
// re-running drops the partial state and rebuilds. Cross-zone
// behavior diverges from Python here: Python wraps the whole
// `for z in zones: …` loop in one `db.commit()`, so a mid-loop
// failure rolls everything back. Go's autocommit-per-statement
// leaves zones 1..N-1 committed and zone N half-committed. The
// next cron tick re-DELETEs and rebuilds, healing the divergence.
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- pure helpers ----

// reverseZoneName returns the reverse-zone origin (not the PTR's
// owner) for an address. /24 for v4 (3 reversed octets) and /64
// for v6 (16 reversed nibbles). Matches services.dns.reverse_zone_name.
func reverseZoneName(addr string) (string, error) {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return "", err
	}
	if a.Is4() {
		b := a.As4()
		return fmt.Sprintf("%d.%d.%d.in-addr.arpa", b[2], b[1], b[0]), nil
	}
	// v6 /64: first 16 nibbles reversed.
	b := a.As16()
	hex := make([]byte, 0, 32)
	for _, by := range b[:8] { // 8 bytes = 16 nibbles
		hex = append(hex, "0123456789abcdef"[by>>4])
		hex = append(hex, "0123456789abcdef"[by&0x0F])
	}
	parts := make([]string, len(hex))
	for i, c := range hex {
		// reverse order
		parts[len(hex)-1-i] = string(c)
	}
	return strings.Join(parts, ".") + ".ip6.arpa", nil
}

// ptrOwner returns the fully-qualified PTR owner name for an
// address (e.g. "5.0.0.10.in-addr.arpa." for 10.0.0.5).
func ptrOwner(addr string) (string, error) {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return "", err
	}
	if a.Is4() {
		b := a.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", b[3], b[2], b[1], b[0]), nil
	}
	b := a.As16()
	hex := make([]byte, 0, 32)
	for _, by := range b {
		hex = append(hex, "0123456789abcdef"[by>>4])
		hex = append(hex, "0123456789abcdef"[by&0x0F])
	}
	parts := make([]string, len(hex))
	for i, c := range hex {
		parts[len(hex)-1-i] = string(c)
	}
	return strings.Join(parts, ".") + ".ip6.arpa.", nil
}

// ptrLabelIn strips the zone origin off a PTR owner, returning the
// relative label DnsRecord stores. Matches _ptr_label_in.
func ptrLabelIn(owner, zoneOrigin string) string {
	suffix := "." + zoneOrigin
	if strings.HasSuffix(owner, suffix) {
		return owner[:len(owner)-len(suffix)]
	}
	return owner
}

// forwardLabelFor strips the zone suffix from an FQDN, or collapses
// to "@" when the name IS the zone origin.
func forwardLabelFor(dnsName, zoneName string) string {
	suffix := "." + zoneName
	if strings.HasSuffix(dnsName, suffix) {
		return dnsName[:len(dnsName)-len(suffix)]
	}
	if dnsName == zoneName {
		return "@"
	}
	return dnsName
}

// ptrTargetFor builds the PTR's target FQDN: dnsName if already
// absolute, else label + forward-zone origin.
func ptrTargetFor(dnsName, zoneName string) string {
	if strings.HasSuffix(dnsName, ".") {
		return dnsName
	}
	return dnsName + "." + zoneName + "."
}

// recordTypeForAddr returns "A" or "AAAA" based on the address.
func recordTypeForAddr(addr string) (string, error) {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return "", err
	}
	if a.Is4() {
		return "A", nil
	}
	return "AAAA", nil
}

// ---- syncer ----

type syncResponse struct {
	ZoneID  string `json:"zone_id"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// SyncQuerier is the minimal Querier surface SyncIPAMRecordsForZone
// needs. Exported so packages outside `dns` (the scheduler job in
// internal/scheduler/jobs/dnssync, specifically) can satisfy it
// without depending on the larger handler.Querier interface.
type SyncQuerier interface {
	ListReverseZonesForSite(ctx context.Context, arg dbq.ListReverseZonesForSiteParams) ([]dbq.DnsZone, error)
	GetReverseZoneByName(ctx context.Context, arg dbq.GetReverseZoneByNameParams) (dbq.DnsZone, error)
	CreateReverseZone(ctx context.Context, arg dbq.CreateReverseZoneParams) (dbq.DnsZone, error)
	ListIPAddressesForSiteWithDnsName(ctx context.Context, siteID uuid.UUID) ([]dbq.ListIPAddressesForSiteWithDnsNameRow, error)
	DeleteIPAMRecordsInZones(ctx context.Context, zoneIDs []uuid.UUID) error
	CountIPAMRecordsInZones(ctx context.Context, zoneIDs []uuid.UUID) (int64, error)
	CreateProjectedDnsRecord(ctx context.Context, arg dbq.CreateProjectedDnsRecordParams) (uuid.UUID, error)
	TouchDnsZone(ctx context.Context, id uuid.UUID) (int64, error)
}

// SyncIPAMRecordsForZone rebuilds source=ipam/ddns DNS records for a
// site zone + every derived reverse zone. Returns (added, removed)
// row counts. Apex zones short-circuit to (0, 0) — they're
// operator-curated and the cron skips them anyway. Handler-agnostic
// so both the POST /dns/zones/{id}/sync-from-ipam endpoint and the
// dns_sync_from_ipam scheduler job invoke the same code path.
func SyncIPAMRecordsForZone(ctx context.Context, q SyncQuerier, zone dbq.DnsZone) (added, removed int, err error) {
	if zone.Kind != "site" || zone.SiteID == nil {
		return 0, 0, nil
	}
	revZones, err := q.ListReverseZonesForSite(ctx, dbq.ListReverseZonesForSiteParams{FabricID: zone.FabricID, SiteID: *zone.SiteID})
	if err != nil {
		return 0, 0, err
	}
	revByName := make(map[string]dbq.DnsZone, len(revZones))
	for _, z := range revZones {
		revByName[z.Name] = z
	}
	// Drop existing source=ipam/ddns rows in forward + reverse zones.
	zoneIDs := make([]uuid.UUID, 0, 1+len(revZones))
	zoneIDs = append(zoneIDs, zone.ID)
	for _, z := range revZones {
		zoneIDs = append(zoneIDs, z.ID)
	}
	dropCount, err := q.CountIPAMRecordsInZones(ctx, zoneIDs)
	if err != nil {
		return 0, 0, err
	}
	if err := q.DeleteIPAMRecordsInZones(ctx, zoneIDs); err != nil {
		return 0, 0, err
	}
	// Load every IP that should project.
	rows, err := q.ListIPAddressesForSiteWithDnsName(ctx, *zone.SiteID)
	if err != nil {
		return 0, 0, err
	}
	// Track which reverse zones we actually emitted PTRs into so we
	// only touch (bump SOA / etag) the zones that genuinely changed
	// — matches Python's `touched_zone_ids` set at
	// services/dns.py:2682. Without this, a 5-min cron over quiet
	// zones bumps every reverse zone's serial every tick, causing
	// needless downstream resolver churn.
	touchedRev := make(map[uuid.UUID]struct{})
	for _, ip := range rows {
		// Skip rows with NULL dns_name (defensive — the query
		// already filters but a future schema change could lift
		// the filter and we shouldn't crash).
		if ip.DnsName == nil || *ip.DnsName == "" {
			continue
		}
		added2, revZoneID, err := emitForwardAndReverse(ctx, q, zone, ip, revByName)
		if err != nil {
			return added, int(dropCount), err
		}
		added += added2
		if revZoneID != uuid.Nil {
			touchedRev[revZoneID] = struct{}{}
		}
	}
	// Forward zone's serial only moves if we actually changed it
	// (added rows, OR removed rows in the drop step). Matches
	// Python's `if added > 0 or removed > 0: touched_zone_ids.add(zone.id)`.
	if added > 0 || dropCount > 0 {
		if _, err := q.TouchDnsZone(ctx, zone.ID); err != nil {
			return added, int(dropCount), err
		}
	}
	for revID := range touchedRev {
		if _, err := q.TouchDnsZone(ctx, revID); err != nil {
			return added, int(dropCount), err
		}
	}
	return added, int(dropCount), nil
}

// emitForwardAndReverse emits one A/AAAA + matching PTR for an
// IP. Returns (added, revZoneID, err) where added is 0/1/2 and
// revZoneID is the reverse zone that received the PTR (or uuid.Nil
// if no PTR was emitted — unparseable address, malformed reverse
// origin, or PTR-side errors that didn't reach the INSERT).
// revByName is mutated to cache auto-created reverse zones across
// iterations. The returned revZoneID feeds the caller's
// touchedRev set so we don't bump SOA on reverse zones we didn't
// actually write to.
func emitForwardAndReverse(
	ctx context.Context, q SyncQuerier, forward dbq.DnsZone, ip dbq.ListIPAddressesForSiteWithDnsNameRow,
	revByName map[string]dbq.DnsZone,
) (int, uuid.UUID, error) {
	rtype, err := recordTypeForAddr(ip.Address)
	if err != nil {
		return 0, uuid.Nil, nil // skip unparseable address
	}
	source := "ipam"
	if ip.Source == "dhcp" {
		source = "ddns"
	}
	// Forward A/AAAA.
	forwardName := forwardLabelFor(*ip.DnsName, forward.Name)
	fwdData, _ := json.Marshal(map[string]string{"target": ip.Address})
	ipamID := ip.ID
	if _, err := q.CreateProjectedDnsRecord(ctx, dbq.CreateProjectedDnsRecordParams{
		ZoneID: forward.ID, Name: forwardName, Type: rtype,
		Data: fwdData, Source: source, IpamAddressID: &ipamID,
	}); err != nil {
		return 0, uuid.Nil, err
	}
	// Reverse PTR. Auto-create the reverse zone if it doesn't exist.
	revOrigin, err := reverseZoneName(ip.Address)
	if err != nil {
		return 1, uuid.Nil, nil // forward succeeded; skip PTR
	}
	rev, ok := revByName[revOrigin]
	if !ok {
		// Try to fetch first in case a concurrent sync created it.
		existing, gerr := q.GetReverseZoneByName(ctx, dbq.GetReverseZoneByNameParams{FabricID: forward.FabricID, SiteID: *forward.SiteID, Name: revOrigin})
		if gerr == nil {
			rev = existing
		} else if errors.Is(gerr, pgx.ErrNoRows) {
			created, cerr := q.CreateReverseZone(ctx, dbq.CreateReverseZoneParams{Name: revOrigin, FabricID: forward.FabricID, SiteID: *forward.SiteID})
			if cerr != nil {
				return 1, uuid.Nil, cerr
			}
			rev = created
		} else {
			return 1, uuid.Nil, gerr
		}
		revByName[revOrigin] = rev
	}
	owner, _ := ptrOwner(ip.Address)
	ptrLabel := ptrLabelIn(strings.TrimSuffix(owner, "."), revOrigin)
	ptrData, _ := json.Marshal(map[string]string{
		"target": ptrTargetFor(*ip.DnsName, forward.Name),
	})
	if _, err := q.CreateProjectedDnsRecord(ctx, dbq.CreateProjectedDnsRecordParams{
		ZoneID: rev.ID, Name: ptrLabel, Type: "PTR",
		Data: ptrData, Source: source, IpamAddressID: &ipamID,
	}); err != nil {
		return 1, uuid.Nil, err
	}
	return 2, rev.ID, nil
}

// ---- handler ----

func (h *Handler) syncFromIPAM(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	zone, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if !h.enforceFabric(w, r, zone.FabricID, "dns:zones:update") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before syncing")
		return
	}
	added, removed, err := SyncIPAMRecordsForZone(r.Context(), h.Q, zone)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dns_zone.sync_ipam", TargetType: "dns_zone", TargetID: id.String(),
	})
	httpx.JSON(w, http.StatusOK, syncResponse{
		ZoneID: id.String(), Added: added, Removed: removed,
	})
}
