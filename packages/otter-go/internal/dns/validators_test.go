package dns

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateRecordData_A(t *testing.T) {
	if err := validateRecordData("A", json.RawMessage(`{"target":"10.0.0.5"}`)); err != nil {
		t.Errorf("valid A rejected: %v", err)
	}
	if err := validateRecordData("A", json.RawMessage(`{"target":"::1"}`)); err == nil {
		t.Error("IPv6 in A record should be rejected")
	}
	if err := validateRecordData("A", json.RawMessage(`{"target":"not-an-ip"}`)); err == nil {
		t.Error("garbage target should be rejected")
	}
	if err := validateRecordData("A", json.RawMessage(`{}`)); err == nil {
		t.Error("missing target should be rejected")
	}
}

func TestValidateRecordData_AAAA(t *testing.T) {
	if err := validateRecordData("AAAA", json.RawMessage(`{"target":"fd00::1"}`)); err != nil {
		t.Errorf("valid AAAA rejected: %v", err)
	}
	if err := validateRecordData("AAAA", json.RawMessage(`{"target":"10.0.0.5"}`)); err == nil {
		t.Error("IPv4 in AAAA record should be rejected")
	}
}

func TestValidateRecordData_MX(t *testing.T) {
	if err := validateRecordData("MX", json.RawMessage(`{"priority":10,"target":"mail.example.com"}`)); err != nil {
		t.Errorf("valid MX rejected: %v", err)
	}
	if err := validateRecordData("MX", json.RawMessage(`{"target":"mail.example.com"}`)); err == nil {
		t.Error("MX without priority should be rejected")
	}
	if err := validateRecordData("MX", json.RawMessage(`{"priority":99999,"target":"mail.example.com"}`)); err == nil {
		t.Error("MX priority out of range should be rejected")
	}
}

func TestValidateRecordData_SRV(t *testing.T) {
	good := `{"priority":0,"weight":5,"port":443,"target":"web.example.com"}`
	if err := validateRecordData("SRV", json.RawMessage(good)); err != nil {
		t.Errorf("valid SRV rejected: %v", err)
	}
	bad := `{"priority":0,"weight":5,"port":99999,"target":"x"}`
	if err := validateRecordData("SRV", json.RawMessage(bad)); err == nil {
		t.Error("SRV port out of range should be rejected")
	}
}

func TestValidateRecordData_CAA(t *testing.T) {
	if err := validateRecordData("CAA", json.RawMessage(`{"flags":0,"tag":"issue","value":"letsencrypt.org"}`)); err != nil {
		t.Errorf("valid CAA rejected: %v", err)
	}
	if err := validateRecordData("CAA", json.RawMessage(`{"flags":0,"tag":"bogus","value":"x"}`)); err == nil {
		t.Error("invalid CAA tag should be rejected")
	}
}

func TestValidateRecordData_CNAME(t *testing.T) {
	if err := validateRecordData("CNAME", json.RawMessage(`{"target":"alias.example.com"}`)); err != nil {
		t.Errorf("valid CNAME rejected: %v", err)
	}
	if err := validateRecordData("CNAME", json.RawMessage(`{"target":""}`)); err == nil {
		t.Error("empty CNAME target should be rejected")
	}
}

func TestValidateRecordData_UnknownTypePasses(t *testing.T) {
	// The pg enum cast rejects unknown record types before we see them,
	// so the validator passes through anything it doesn't recognize.
	if err := validateRecordData("DNSKEY", json.RawMessage(`{"key":"..."}`)); err != nil {
		t.Errorf("unknown type unexpectedly rejected: %v", err)
	}
}

func TestValidateRecordData_MissingDataRejected(t *testing.T) {
	if err := validateRecordData("A", nil); err == nil || !strings.Contains(err.Error(), "data is required") {
		t.Errorf("got %v", err)
	}
}
