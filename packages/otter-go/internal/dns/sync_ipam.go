// PR 83 — POST /dns/zones/{id}/sync-from-ipam.
//
// Rebuilds source=ipam (and source=ddns) records for a site zone +
// every derived reverse zone. Operator runs this when IPAM rows
// change in ways the live triggers don't catch (bulk import, hand
// edit). The arq cron worker (`dns_sync_from_ipam`) calls the same
// service helper to keep the projection fresh.
//
// Partial-failure note: the existing Go codebase doesn't expose
// pgx transactions on the Querier interface. The drop + bulk
// insert here runs as sequential statements — a connection drop
// mid-write leaves the zone half-rebuilt. Recovery is idempotent:
// re-running drops the partial state and rebuilds. The arq cron
// (every 60s) keeps drift bounded.
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

// syncIPAMRecordsForZone is the service layer — handler-agnostic
// so the cron worker (when ported) can call it the same way.
func (h *Handler) syncIPAMRecordsForZone(ctx context.Context, zone dbq.DnsZone) (added, removed int, err error) {
	// site zones only — apex zones don't project IPAM (they're
	// operator-curated).
	if zone.Kind != "site" || zone.SiteID == nil {
		return 0, 0, nil
	}
	revZones, err := h.Q.ListReverseZonesForSite(ctx, zone.FabricID, *zone.SiteID)
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
	dropCount, err := h.Q.CountIPAMRecordsInZones(ctx, zoneIDs)
	if err != nil {
		return 0, 0, err
	}
	if err := h.Q.DeleteIPAMRecordsInZones(ctx, zoneIDs); err != nil {
		return 0, 0, err
	}
	// Load every IP that should project.
	rows, err := h.Q.ListIPAddressesForSiteWithDnsName(ctx, *zone.SiteID)
	if err != nil {
		return 0, 0, err
	}
	for _, ip := range rows {
		// Skip rows with NULL dns_name (defensive — the query
		// already filters but a future schema change could lift
		// the filter and we shouldn't crash).
		if ip.DnsName == nil || *ip.DnsName == "" {
			continue
		}
		added2, err := h.emitForwardAndReverse(ctx, zone, ip, revByName)
		if err != nil {
			return added, int(dropCount), err
		}
		added += added2
	}
	// Bump updated_at on every touched zone so its bundle etag flips.
	if _, err := h.Q.TouchDnsZone(ctx, zone.ID); err != nil {
		return added, int(dropCount), err
	}
	for _, z := range revByName {
		if _, err := h.Q.TouchDnsZone(ctx, z.ID); err != nil {
			return added, int(dropCount), err
		}
	}
	return added, int(dropCount), nil
}

// emitForwardAndReverse emits one A/AAAA + matching PTR for an
// IP. Returns the count added (0 or 2). revByName is mutated to
// cache auto-created reverse zones across iterations.
func (h *Handler) emitForwardAndReverse(
	ctx context.Context, forward dbq.DnsZone, ip dbq.IPAddressForSyncRow,
	revByName map[string]dbq.DnsZone,
) (int, error) {
	rtype, err := recordTypeForAddr(ip.Address)
	if err != nil {
		return 0, nil // skip unparseable address
	}
	source := "ipam"
	if ip.Source == "dhcp" {
		source = "ddns"
	}
	// Forward A/AAAA.
	forwardName := forwardLabelFor(*ip.DnsName, forward.Name)
	fwdData, _ := json.Marshal(map[string]string{"target": ip.Address})
	ipamID := ip.ID
	if _, err := h.Q.CreateProjectedDnsRecord(ctx, dbq.CreateProjectedDnsRecordParams{
		ZoneID: forward.ID, Name: forwardName, Type: rtype,
		Data: fwdData, Source: source, IpamAddressID: &ipamID,
	}); err != nil {
		return 0, err
	}
	// Reverse PTR. Auto-create the reverse zone if it doesn't exist.
	revOrigin, err := reverseZoneName(ip.Address)
	if err != nil {
		return 1, nil // forward succeeded; skip PTR
	}
	rev, ok := revByName[revOrigin]
	if !ok {
		// Try to fetch first in case a concurrent sync created it.
		existing, gerr := h.Q.GetReverseZoneByName(ctx, forward.FabricID, *forward.SiteID, revOrigin)
		if gerr == nil {
			rev = existing
		} else if errors.Is(gerr, pgx.ErrNoRows) {
			created, cerr := h.Q.CreateReverseZone(ctx, revOrigin, forward.FabricID, *forward.SiteID)
			if cerr != nil {
				return 1, cerr
			}
			rev = created
		} else {
			return 1, gerr
		}
		revByName[revOrigin] = rev
	}
	owner, _ := ptrOwner(ip.Address)
	ptrLabel := ptrLabelIn(strings.TrimSuffix(owner, "."), revOrigin)
	ptrData, _ := json.Marshal(map[string]string{
		"target": ptrTargetFor(*ip.DnsName, forward.Name),
	})
	if _, err := h.Q.CreateProjectedDnsRecord(ctx, dbq.CreateProjectedDnsRecordParams{
		ZoneID: rev.ID, Name: ptrLabel, Type: "PTR",
		Data: ptrData, Source: source, IpamAddressID: &ipamID,
	}); err != nil {
		return 1, err
	}
	return 2, nil
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
	added, removed, err := h.syncIPAMRecordsForZone(r.Context(), zone)
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
