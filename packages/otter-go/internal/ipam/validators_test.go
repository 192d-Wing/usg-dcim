package ipam

import (
	"net/netip"
	"testing"

	"github.com/google/uuid"
)

func TestSlugRE(t *testing.T) {
	good := []string{"a", "ab", "us-east-1", "site-001", "x9"}
	for _, s := range good {
		if err := validateSlug(s); err != nil {
			t.Errorf("good slug %q rejected: %v", s, err)
		}
	}
	bad := []string{"", "-x", "x-", "Foo", "foo.bar", "foo bar", "foo_bar"}
	for _, s := range bad {
		if err := validateSlug(s); err == nil {
			t.Errorf("bad slug %q accepted", s)
		}
	}
}

func TestCidrContains(t *testing.T) {
	p := func(s string) netip.Prefix {
		x, err := parseCIDR(s)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if !cidrContains(p("10.0.0.0/8"), p("10.1.0.0/16")) {
		t.Error("/8 should contain /16")
	}
	if cidrContains(p("10.0.0.0/16"), p("10.0.0.0/8")) {
		t.Error("/16 should not contain /8")
	}
	if !cidrContains(p("10.0.0.0/8"), p("10.0.0.0/8")) {
		t.Error("identical prefixes should contain")
	}
	if cidrContains(p("10.0.0.0/8"), p("11.0.0.0/8")) {
		t.Error("non-overlapping should not contain")
	}
	// IPv6
	if !cidrContains(p("fd00::/8"), p("fd00:1234::/32")) {
		t.Error("/8 v6 should contain /32 v6")
	}
}

func TestCidrsOverlap(t *testing.T) {
	p := func(s string) netip.Prefix {
		x, _ := parseCIDR(s)
		return x
	}
	if !cidrsOverlap(p("10.0.0.0/16"), p("10.0.0.0/24")) {
		t.Error("nested prefixes overlap")
	}
	if cidrsOverlap(p("10.0.0.0/24"), p("10.1.0.0/24")) {
		t.Error("disjoint should not overlap")
	}
}

func TestAddressInNetwork(t *testing.T) {
	p, _ := parseCIDR("10.0.0.0/24")
	in, _ := parseAddr("10.0.0.5")
	out, _ := parseAddr("10.0.1.5")
	if !addressInNetwork(in, p) {
		t.Error("10.0.0.5 should be in 10.0.0.0/24")
	}
	if addressInNetwork(out, p) {
		t.Error("10.0.1.5 should not be in 10.0.0.0/24")
	}
}

func TestParseAddrWithMask(t *testing.T) {
	a, err := parseAddr("10.0.0.5/24")
	if err != nil || a.String() != "10.0.0.5" {
		t.Errorf("got %v err=%v", a, err)
	}
}

func TestValidateVni(t *testing.T) {
	for _, ok := range []int32{1, 100, vniMax} {
		if err := validateVni(ok); err != nil {
			t.Errorf("valid vni %d rejected: %v", ok, err)
		}
	}
	for _, bad := range []int32{0, -1, vniMax + 1} {
		if err := validateVni(bad); err == nil {
			t.Errorf("invalid vni %d accepted", bad)
		}
	}
}

func TestValidateVniKind(t *testing.T) {
	vrf := uuid.New()
	vlan := int32(10)

	if err := validateVniKind("l2", &vlan, nil); err != nil {
		t.Errorf("l2+vlan ok: %v", err)
	}
	if err := validateVniKind("l3", nil, &vrf); err != nil {
		t.Errorf("l3+vrf ok: %v", err)
	}
	if err := validateVniKind("l3", &vlan, &vrf); err == nil {
		t.Error("l3+vlan_id should be rejected")
	}
	if err := validateVniKind("l3", nil, nil); err == nil {
		t.Error("l3 without vrf should be rejected")
	}
}

func TestPurposeCompatible(t *testing.T) {
	data := "data"
	mgmt := "mgmt"
	if err := validatePurposeCompatible(nil, &data); err != nil {
		t.Error("parent unset → ok")
	}
	if err := validatePurposeCompatible(&data, nil); err != nil {
		t.Error("child unset → ok")
	}
	if err := validatePurposeCompatible(&data, &data); err != nil {
		t.Error("matching → ok")
	}
	if err := validatePurposeCompatible(&data, &mgmt); err == nil {
		t.Error("mismatch should fail")
	}
}
