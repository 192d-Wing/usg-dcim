// Unit tests for the lease-address → DCIM subnet matcher.
package leasesync

import (
	"testing"

	"github.com/google/uuid"
)

func TestMatchLeaseToSubnet_LongestPrefixWins(t *testing.T) {
	bigID := uuid.New()
	smallID := uuid.New()
	subnets := []Subnet{
		{ID: bigID, Prefix: "10.0.0.0/16"},
		{ID: smallID, Prefix: "10.0.0.0/24"},
	}
	got := MatchLeaseToSubnet("10.0.0.5", subnets)
	if got == nil || got.ID != smallID {
		t.Errorf("got %v, want /24 (smallID=%s)", got, smallID)
	}
}

func TestMatchLeaseToSubnet_NoMatchReturnsNil(t *testing.T) {
	subnets := []Subnet{{ID: uuid.New(), Prefix: "10.0.0.0/24"}}
	if got := MatchLeaseToSubnet("192.168.1.1", subnets); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestMatchLeaseToSubnet_MalformedAddressReturnsNil(t *testing.T) {
	subnets := []Subnet{{ID: uuid.New(), Prefix: "10.0.0.0/24"}}
	if got := MatchLeaseToSubnet("not-an-ip", subnets); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestMatchLeaseToSubnet_UnparseableSubnetSkipped(t *testing.T) {
	// Python at services/kea.py:101-102: subnets with bad prefix
	// strings are silently skipped — one bad DB row mustn't break
	// the sweep.
	goodID := uuid.New()
	subnets := []Subnet{
		{ID: uuid.New(), Prefix: "not-a-cidr"},
		{ID: goodID, Prefix: "10.0.0.0/24"},
	}
	got := MatchLeaseToSubnet("10.0.0.5", subnets)
	if got == nil || got.ID != goodID {
		t.Errorf("got %v, want goodID match (skip the bad row, don't fail)", got)
	}
}

func TestMatchLeaseToSubnet_V6LongestPrefix(t *testing.T) {
	bigID := uuid.New()
	smallID := uuid.New()
	subnets := []Subnet{
		{ID: bigID, Prefix: "2001:db8::/32"},
		{ID: smallID, Prefix: "2001:db8::/64"},
	}
	got := MatchLeaseToSubnet("2001:db8::1", subnets)
	if got == nil || got.ID != smallID {
		t.Errorf("got %v, want /64", got)
	}
}

func TestMatchLeaseToSubnet_EmptySubnetsReturnsNil(t *testing.T) {
	if got := MatchLeaseToSubnet("10.0.0.5", nil); got != nil {
		t.Errorf("got %v, want nil for empty subnets", got)
	}
}

// DCIM subnet rows occasionally carry host bits in the Prefix
// column (e.g. "10.0.0.5/24" instead of "10.0.0.0/24"). Python's
// ipaddress.ip_network(prefix, strict=False) accepts these; the
// matcher must still find them. netip.ParsePrefix handles host
// bits today, but a future refactor to e.g. prefix.Masked()
// equality could silently drop these rows — this test pins the
// real-world behavior.
func TestMatchLeaseToSubnet_HostBitsInPrefixAccepted(t *testing.T) {
	id := uuid.New()
	subnets := []Subnet{{ID: id, Prefix: "10.0.0.5/24"}}
	got := MatchLeaseToSubnet("10.0.0.200", subnets)
	if got == nil || got.ID != id {
		t.Errorf("got %v, want match (host bits in prefix must not block matching)", got)
	}
}

func TestMatchLeaseToSubnet_CrossFamilyDoesntMatch(t *testing.T) {
	// A v4 address must NOT match a v6 subnet (netip.Prefix.Contains
	// guards against the cross-family case correctly).
	subnets := []Subnet{{ID: uuid.New(), Prefix: "2001:db8::/64"}}
	if got := MatchLeaseToSubnet("10.0.0.5", subnets); got != nil {
		t.Errorf("v4 address must not match v6 subnet, got %v", got)
	}
}
