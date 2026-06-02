// Package leasesync owns the DHCP lease → IPAddress reconciliation
// pipeline. PR 14 lands the pure matcher (lease address → DCIM
// subnet via longest-prefix); PR 15 adds the orchestrator that
// composes kea.ParseLease + MatchLeaseToSubnet + an upsert SQL.
//
// Kept separate from internal/dhcp/kea because matching is a DCIM
// concern: subnets come from the IPAM model, not from Kea. Keeping
// the boundary tight means the kea package stays Kea-shaped (its
// types appear in the wire shape, nothing else) and the leasesync
// package owns the DCIM-side composition.
package leasesync

import (
	"net/netip"

	"github.com/google/uuid"
)

// Subnet is the narrow projection the matcher reads. The handler/
// orchestrator passes whatever subnet projection it loaded — the
// dbq row, a dedicated SubnetForLeaseMatch row, etc. — through this
// shape. Kept package-local to avoid a circular dep on internal/ipam.
type Subnet struct {
	ID     uuid.UUID
	Prefix string // CIDR text form, e.g. "10.0.0.0/24"
}

// MatchLeaseToSubnet picks the most specific subnet whose CIDR
// contains the address. Mirrors Python's
// services/kea.py:92-109 longest-prefix matcher; subnets with
// unparseable prefixes are silently skipped so one bad row doesn't
// break sync for everything else.
//
// Returns the matching subnet or nil if no subnet covers the
// address. The address is parsed with netip.ParseAddr; malformed
// inputs return nil immediately (no false-positive match against
// the first parseable subnet).
func MatchLeaseToSubnet(address string, subnets []Subnet) *Subnet {
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return nil
	}
	var best *Subnet
	bestPrefix := -1
	for i := range subnets {
		s := &subnets[i]
		prefix, err := netip.ParsePrefix(s.Prefix)
		if err != nil {
			continue
		}
		if !prefix.Contains(addr) {
			continue
		}
		// Longest-prefix wins. Ties don't happen in practice
		// (overlapping same-prefix-length subnets shouldn't be in
		// one fabric), but if they do, "first wins" matches
		// Python's stable-sort posture.
		if prefix.Bits() > bestPrefix {
			best = s
			bestPrefix = prefix.Bits()
		}
	}
	return best
}
