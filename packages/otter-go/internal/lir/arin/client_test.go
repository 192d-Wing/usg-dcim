// Unit tests for the ARIN client — XML payload builder, response
// classification, prefix-range math, and the broadcast helper. No
// network calls; tests are pure.
package arin

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ---- broadcastAddr ----

func TestBroadcast_V4_24(t *testing.T) {
	p := netip.MustParsePrefix("10.0.0.0/24")
	got, err := broadcastAddr(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.String() != "10.0.0.255" {
		t.Errorf("got %s, want 10.0.0.255", got)
	}
}

func TestBroadcast_V4_30(t *testing.T) {
	p := netip.MustParsePrefix("192.168.1.4/30")
	got, _ := broadcastAddr(p)
	if got.String() != "192.168.1.7" {
		t.Errorf("got %s, want 192.168.1.7", got)
	}
}

func TestBroadcast_V6_64(t *testing.T) {
	p := netip.MustParsePrefix("2001:db8::/64")
	got, err := broadcastAddr(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.String() != "2001:db8::ffff:ffff:ffff:ffff" {
		t.Errorf("got %s", got)
	}
}

func TestBroadcast_V4_32IsSelf(t *testing.T) {
	p := netip.MustParsePrefix("10.0.0.7/32")
	got, _ := broadcastAddr(p)
	if got.String() != "10.0.0.7" {
		t.Errorf("/32 broadcast should equal self, got %s", got)
	}
}

// ---- parsePrefixRange ----

func TestParsePrefixRange_V4(t *testing.T) {
	start, end, cidr, err := parsePrefixRange("10.0.0.0/24")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if start != "10.0.0.0" || end != "10.0.0.255" || cidr != 24 {
		t.Errorf("got start=%s end=%s cidr=%d", start, end, cidr)
	}
}

func TestParsePrefixRange_V6(t *testing.T) {
	start, end, cidr, err := parsePrefixRange("2001:db8::/48")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cidr != 48 || start != "2001:db8::" || !strings.Contains(end, "ffff") {
		t.Errorf("got start=%s end=%s cidr=%d", start, end, cidr)
	}
}

func TestParsePrefixRange_RejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "10.0.0.0", "not-a-cidr", "10.0.0.0/abc"} {
		if _, _, _, err := parsePrefixRange(in); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

// ---- buildReassignDetailedXML ----

func sampleJob() dbq.ArinSubmitJobRow {
	state := "VA"
	post := "20151"
	return dbq.ArinSubmitJobRow{
		Prefix:          "192.0.2.0/26",
		ParentNetHandle: "NET-192-0-2-0-1",
		OrgName:         "Example Customer",
		AddressLine1:    "123 Main St",
		City:            "Chantilly",
		StateProvince:   &state,
		PostalCode:      &post,
		Country:         "US",
		AdminPocName:    "Pat Admin",
		AdminPocEmail:   "admin@example.com",
		TechPocName:     "Pat Tech",
		TechPocEmail:    "tech@example.com",
		AbusePocName:    "Pat Abuse",
		AbusePocEmail:   "abuse@example.com",
	}
}

func TestBuildXML_IncludesRequiredFields(t *testing.T) {
	x, err := buildReassignDetailedXML(sampleJob())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	s := string(x)
	for _, needle := range []string{
		`<?xml version=`,
		`xmlns="http://www.arin.net/regrws/core/v1"`,
		`<startAddress>192.0.2.0</startAddress>`,
		`<endAddress>192.0.2.63</endAddress>`,
		`<cidrLength>26</cidrLength>`,
		`<type>AR</type>`,
		`<orgName>Example Customer</orgName>`,
		`<city>Chantilly</city>`,
		`<line>123 Main St</line>`,
		`<code2>US</code2>`,
		`<postalCode>20151</postalCode>`,
		`<iso3166-2>VA</iso3166-2>`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("payload missing %q:\n%s", needle, s)
		}
	}
}

func TestBuildXML_OmitsOptionalEmpty(t *testing.T) {
	job := sampleJob()
	job.StateProvince = nil
	job.PostalCode = nil
	x, err := buildReassignDetailedXML(job)
	if err != nil {
		t.Fatal(err)
	}
	s := string(x)
	if strings.Contains(s, "<iso3166-2>") {
		t.Error("state element should be omitted when nil")
	}
	if strings.Contains(s, "<postalCode>") {
		t.Error("postal element should be omitted when nil")
	}
}

func TestBuildXML_RejectsBadPrefix(t *testing.T) {
	job := sampleJob()
	job.Prefix = "not-a-cidr"
	if _, err := buildReassignDetailedXML(job); err == nil {
		t.Error("expected error on bad prefix")
	}
}

// ---- parseNetHandle ----

func TestParseNetHandle_OK(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<net xmlns="http://www.arin.net/regrws/core/v1">
  <handle>NET-192-0-2-0-1-1</handle>
  <startAddress>192.0.2.0</startAddress>
</net>`)
	got, err := parseNetHandle(body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "NET-192-0-2-0-1-1" {
		t.Errorf("got %q", got)
	}
}

func TestParseNetHandle_MissingHandle(t *testing.T) {
	body := []byte(`<net><startAddress>x</startAddress></net>`)
	if _, err := parseNetHandle(body); err == nil {
		t.Error("expected missing-handle error")
	}
}

func TestParseNetHandle_EmptyBody(t *testing.T) {
	if _, err := parseNetHandle(nil); err == nil {
		t.Error("expected empty-body error")
	}
}

func TestParseNetHandle_BadXML(t *testing.T) {
	if _, err := parseNetHandle([]byte("not xml")); err == nil {
		t.Error("expected parse error")
	}
}

// ---- classifyResponse ----

func TestClassify_2xxWithHandleIsSuccess(t *testing.T) {
	body := []byte(`<net><handle>NET-1</handle></net>`)
	res, err := classifyResponse(200, body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.NetHandle != "NET-1" {
		t.Errorf("got %q", res.NetHandle)
	}
}

func TestClassify_2xxWithoutHandleIsTransient(t *testing.T) {
	body := []byte(`<ticketedRequest>...</ticketedRequest>`)
	_, err := classifyResponse(200, body)
	if !errors.Is(err, ErrTransient) {
		t.Errorf("expected transient, got %v", err)
	}
}

func TestClassify_5xxIsTransient(t *testing.T) {
	_, err := classifyResponse(503, []byte("upstream is down"))
	if !errors.Is(err, ErrTransient) {
		t.Errorf("5xx should be transient, got %v", err)
	}
}

func TestClassify_4xxIsPermanent(t *testing.T) {
	_, err := classifyResponse(400, []byte(`<error><message>bad payload</message></error>`))
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("4xx should be permanent, got %v", err)
	}
	if !strings.Contains(err.Error(), "bad payload") {
		t.Errorf("error should include arin message: %v", err)
	}
}

func TestClassify_LongBodyTruncated(t *testing.T) {
	huge := make([]byte, 5000)
	for i := range huge {
		huge[i] = 'x'
	}
	_, err := classifyResponse(400, huge)
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("expected truncated marker, got %v", err)
	}
}

// ---- Submit guards ----

func TestSubmit_DisabledIsPermanent(t *testing.T) {
	c := NewClient(Config{Enabled: false, APIKey: "k"}, nil)
	_, err := c.SubmitReassignDetailed(t.Context(), sampleJob())
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("disabled should be permanent, got %v", err)
	}
}

func TestSubmit_MissingAPIKeyIsPermanent(t *testing.T) {
	c := NewClient(Config{Enabled: true, APIKey: ""}, nil)
	_, err := c.SubmitReassignDetailed(t.Context(), sampleJob())
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("missing key should be permanent, got %v", err)
	}
}
