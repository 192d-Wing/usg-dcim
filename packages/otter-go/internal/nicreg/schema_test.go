package nicreg

import (
	"strings"
	"testing"
)

func TestValidate_UnknownTypeAndAction(t *testing.T) {
	if err := Validate("nope", "N", map[string]any{}); err == nil {
		t.Fatal("expected error for unknown template_type")
	}
	// dnskey has no Reregister action.
	err := Validate("dnskey", "R", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected action-not-allowed error, got %v", err)
	}
}

func TestValidate_RequiredFields(t *testing.T) {
	// Organization missing required org name / address / city / state / agency.
	err := Validate("organization", "N", map[string]any{})
	if err == nil {
		t.Fatal("expected required-field errors")
	}
	for _, want := range []string{"Agency", "Organization Name", "Address Line 1", "City", "State"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}

	ok := Validate("organization", "N", map[string]any{
		"agency": "ARMY", "organization_name": "192 WG", "address_line1": "1 Way",
		"city": "Richmond", "state_code": "VA",
	})
	if ok != nil {
		t.Fatalf("expected valid organization, got %v", ok)
	}
}

func TestValidate_ConditionalCloudFields(t *testing.T) {
	base := map[string]any{
		"org_handle": "ORG-1", "tech_poc_handle": "T", "admin_poc_handle": "A",
		"ip_version": "ipv4", "classification": "unclassified",
		"customer_network_name": "NET1", "cidr": float64(24),
		"hosts_initial": float64(1), "hosts_6mo": float64(2), "hosts_max": float64(3),
		"justification": "need space",
	}
	// aggregator=niprnet → CCS fields hidden, not required.
	p1 := cloneMap(base)
	p1["network_aggregator"] = "niprnet"
	if err := Validate("network", "N", p1); err != nil {
		t.Fatalf("niprnet network should be valid without CCS fields: %v", err)
	}
	// aggregator=cloud → CCS fields revealed + required.
	p2 := cloneMap(base)
	p2["network_aggregator"] = "cloud"
	err := Validate("network", "N", p2)
	if err == nil || !strings.Contains(err.Error(), "CCS Platform") {
		t.Fatalf("cloud network should require CCS fields, got %v", err)
	}
}

func TestValidate_IPv6SectionGatedByVersion(t *testing.T) {
	// ipv4 selected → ipv6-only fields (geophysical_location) not required.
	p := map[string]any{
		"org_handle": "O", "tech_poc_handle": "T", "admin_poc_handle": "A",
		"ip_version": "ipv4", "network_aggregator": "niprnet", "classification": "unclassified",
		"customer_network_name": "N", "cidr": float64(24),
		"hosts_initial": float64(1), "hosts_6mo": float64(1), "hosts_max": float64(1),
		"justification": "j",
	}
	if err := Validate("network", "N", p); err != nil {
		t.Fatalf("ipv4 network must not require ipv6 fields: %v", err)
	}
	// ipv6 selected → geophysical_location + disn_transport + num_48 required.
	p["ip_version"] = "ipv6"
	delete(p, "cidr")
	err := Validate("network", "N", p)
	if err == nil || !strings.Contains(err.Error(), "Geophysical Location") {
		t.Fatalf("ipv6 network must require Geophysical Location, got %v", err)
	}
}

func TestValidate_EnumAndDate(t *testing.T) {
	// invalid enum value.
	err := Validate("network", "N", map[string]any{
		"org_handle": "O", "tech_poc_handle": "T", "admin_poc_handle": "A",
		"ip_version": "ipv4", "network_aggregator": "bogus", "classification": "unclassified",
		"customer_network_name": "N", "cidr": float64(24),
		"hosts_initial": float64(1), "hosts_6mo": float64(1), "hosts_max": float64(1), "justification": "j",
	})
	if err == nil || !strings.Contains(err.Error(), "Network Aggregator") {
		t.Fatalf("expected invalid-enum error, got %v", err)
	}
	// bad dnskey date.
	err = Validate("dnskey", "N", map[string]any{
		"domain_handle": "abc.mil", "start_date": "not-a-date", "end_date": "20270101", "ksk_value": "k",
	})
	if err == nil || !strings.Contains(err.Error(), "Start Date") {
		t.Fatalf("expected date-parse error, got %v", err)
	}
	// good dnskey.
	if err := Validate("dnskey", "N", map[string]any{
		"domain_handle": "abc.mil", "start_date": "20260601", "end_date": "2027-06-01", "ksk_value": "k",
	}); err != nil {
		t.Fatalf("expected valid dnskey, got %v", err)
	}
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
