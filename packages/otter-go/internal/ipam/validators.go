// Validators ported from packages/otter/src/dcim/services/ipam.py.
// Pure/in-process helpers (slug regex, CIDR parsing, address-in-network)
// plus a thin db-backed layer that calls into the existing sqlc Querier
// to walk the supernet tree and check sibling overlap.
//
// All validation errors return a Go error; handlers translate them
// to httpx.Error with the right status (400 for shape/syntax, 409 for
// conflicts).
package ipam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- Pure helpers ----

// slugRE mirrors packages/otter/src/dcim/api/ipam.py:_SLUG_RE.
// Lowercase alphanumeric with optional hyphens; single-char allowed.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

func validateSlug(slug string) error {
	if !slugRE.MatchString(slug) {
		return fmt.Errorf("slug must be lowercase alphanumeric with optional hyphens")
	}
	return nil
}

// parseCIDR accepts "10.0.0.0/8" / "fd00::/64" and returns the parsed
// prefix. Surfaces a clean error so handlers don't leak pg-side parse
// errors to clients.
func parseCIDR(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR %q: %w", s, err)
	}
	return p.Masked(), nil
}

// parseAddr accepts a bare IP ("10.0.0.5") or address-with-mask
// ("10.0.0.5/24"). The Python schema treats `address` as CIDR-typed
// so we accept either form.
func parseAddr(s string) (netip.Addr, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q: %w", s, err)
	}
	return a, nil
}

// cidrsOverlap reports true if either prefix contains the other's
// network address. Mirrors Python ipam.services.cidrs_overlap.
func cidrsOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// cidrContains reports true if parent's prefix covers child's prefix
// entirely (parent is shorter-or-equal prefix length AND parent
// contains child's network address).
func cidrContains(parent, child netip.Prefix) bool {
	if parent.Bits() > child.Bits() {
		return false
	}
	return parent.Contains(child.Addr())
}

// addressInNetwork reports true if addr falls inside prefix.
func addressInNetwork(addr netip.Addr, prefix netip.Prefix) bool {
	return prefix.Contains(addr)
}

// ---- Pure rules ----

const (
	vniMin = 1
	vniMax = (1 << 24) - 2 // 24-bit space, minus 0 and all-ones
)

func validateVni(v int32) error {
	if v < vniMin || v > vniMax {
		return fmt.Errorf("vni must be between %d and %d (24-bit space)", vniMin, vniMax)
	}
	return nil
}

// validateVniKind enforces the L2/L3 invariant: L2 may carry vlan_id;
// L3 requires vrf_id and must not set vlan_id.
func validateVniKind(kind string, vlanID *int32, vrfID *uuid.UUID) error {
	switch kind {
	case "l3":
		if vrfID == nil {
			return errors.New("L3 VNI requires vrf_id")
		}
		if vlanID != nil {
			return errors.New("L3 VNI must not set vlan_id")
		}
	case "l2":
		// vlan_id optional, vrf_id optional
	}
	return nil
}

// validatePurposeCompatible mirrors Python assert_purpose_compatible.
// A supernet without a purpose imposes no constraint; once set, child
// purposes must match or stay unset.
func validatePurposeCompatible(supernetPurpose, subnetPurpose *string) error {
	if supernetPurpose == nil || subnetPurpose == nil {
		return nil
	}
	if *supernetPurpose == "" || *subnetPurpose == "" {
		return nil
	}
	if *supernetPurpose != *subnetPurpose {
		return fmt.Errorf("subnet purpose %q doesn't match parent supernet purpose %q",
			*subnetPurpose, *supernetPurpose)
	}
	return nil
}

// ---- DB-backed checks ----

// assertSupernetInsideParent looks up the parent and refuses if the
// child prefix isn't contained in it, or if parent's (fabric, vrf)
// don't match. Returns the parent so callers can read .Purpose for the
// purpose-inheritance check.
func (h *Handler) assertSupernetInsideParent(
	ctx context.Context, parentID uuid.UUID, prefix netip.Prefix,
	fabricID, vrfID uuid.UUID,
) (*dbq.Supernet, error) {
	parent, err := h.Q.GetSupernet(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("parent supernet %s not found", parentID)
	}
	if parent.FabricID != fabricID || parent.VrfID != vrfID {
		return nil, errors.New("parent supernet must be in the same fabric and VRF")
	}
	parentPrefix, err := parseCIDR(parent.Prefix)
	if err != nil {
		return nil, err
	}
	if !cidrContains(parentPrefix, prefix) {
		return nil, fmt.Errorf("prefix %s is not contained in parent %s", prefix, parent.Prefix)
	}
	return &parent, nil
}

// assertSubnetInsideSupernet — same shape, supernet → subnet.
func (h *Handler) assertSubnetInsideSupernet(
	ctx context.Context, supernetID uuid.UUID, prefix netip.Prefix,
) (*dbq.Supernet, error) {
	parent, err := h.Q.GetSupernet(ctx, supernetID)
	if err != nil {
		return nil, fmt.Errorf("supernet %s not found", supernetID)
	}
	parentPrefix, err := parseCIDR(parent.Prefix)
	if err != nil {
		return nil, err
	}
	if !cidrContains(parentPrefix, prefix) {
		return nil, fmt.Errorf("subnet %s is not contained in supernet %s", prefix, parent.Prefix)
	}
	return &parent, nil
}

// assertAddressInSubnet — IP must be inside subnet's prefix.
func (h *Handler) assertAddressInSubnet(
	ctx context.Context, subnetID uuid.UUID, addr netip.Addr,
) (*dbq.Subnet, error) {
	subnet, err := h.Q.GetSubnet(ctx, subnetID)
	if err != nil {
		return nil, fmt.Errorf("subnet %s not found", subnetID)
	}
	subnetPrefix, err := parseCIDR(subnet.Prefix)
	if err != nil {
		return nil, err
	}
	if !addressInNetwork(addr, subnetPrefix) {
		return nil, fmt.Errorf("address %s is not contained in subnet %s", addr, subnet.Prefix)
	}
	return &subnet, nil
}
