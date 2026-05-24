// DNS invariants ported from packages/otter/src/dcim/api/dns.py and
// the per-type data schemas in packages/otter/src/dcim/schemas/dns.py.
//
// Two checks:
//   1. Frozen-zone refusal — mutating records (or other zone-scoped
//      resources) on a zone with frozen=true returns 422. Operators
//      explicitly flip frozen via the deferred /freeze + /unfreeze
//      endpoints; the flag exists to fence off in-flight maintenance.
//   2. Record data shape — `data` is a JSON object whose required keys
//      and value shapes depend on the record `type`. We reject at the
//      API boundary so the renderer downstream can trust the shape.
package dns

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
)

// errZoneFrozen is the canonical error a handler maps to 422.
var errZoneFrozen = errors.New(
	"zone is frozen — unfreeze it before mutating records or other zone-scoped resources",
)

// validateRecordData refuses payloads whose `data` JSON doesn't match
// the per-type schema. Mirrors packages/otter/src/dcim/schemas/dns.py
// _DATA_SCHEMAS. We unmarshal into a per-type struct (instead of
// re-implementing JSON schema) so the validation is the same code the
// renderer would consume.
func validateRecordData(recordType string, data json.RawMessage) error {
	if len(data) == 0 {
		return errors.New("data is required")
	}
	switch recordType {
	case "A":
		var d struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.Target == "" {
			return errors.New("A record data must be {\"target\": \"<IPv4>\"}")
		}
		ip, err := netip.ParseAddr(d.Target)
		if err != nil || !ip.Is4() {
			return fmt.Errorf("A record target %q is not a valid IPv4 address", d.Target)
		}
	case "AAAA":
		var d struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.Target == "" {
			return errors.New("AAAA record data must be {\"target\": \"<IPv6>\"}")
		}
		ip, err := netip.ParseAddr(d.Target)
		if err != nil || !ip.Is6() {
			return fmt.Errorf("AAAA record target %q is not a valid IPv6 address", d.Target)
		}
	case "CNAME", "NS", "PTR":
		var d struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.Target == "" {
			return fmt.Errorf("%s record data must be {\"target\": \"<FQDN>\"}", recordType)
		}
	case "MX":
		var d struct {
			Priority *int   `json:"priority"`
			Target   string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil || d.Priority == nil || d.Target == "" {
			return errors.New("MX record data must be {\"priority\": <0-65535>, \"target\": \"<FQDN>\"}")
		}
		if *d.Priority < 0 || *d.Priority > 65535 {
			return fmt.Errorf("MX priority %d out of range (0-65535)", *d.Priority)
		}
	case "TXT":
		var d struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return errors.New("TXT record data must be {\"text\": \"<raw text>\"}")
		}
		// Empty text is permitted; some operators set "" for placeholder records.
	case "SRV":
		var d struct {
			Priority *int   `json:"priority"`
			Weight   *int   `json:"weight"`
			Port     *int   `json:"port"`
			Target   string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil ||
			d.Priority == nil || d.Weight == nil || d.Port == nil || d.Target == "" {
			return errors.New("SRV record data must be {\"priority\": <0-65535>, \"weight\": <0-65535>, \"port\": <1-65535>, \"target\": \"<FQDN>\"}")
		}
		if *d.Priority < 0 || *d.Priority > 65535 {
			return fmt.Errorf("SRV priority %d out of range (0-65535)", *d.Priority)
		}
		if *d.Weight < 0 || *d.Weight > 65535 {
			return fmt.Errorf("SRV weight %d out of range (0-65535)", *d.Weight)
		}
		if *d.Port < 1 || *d.Port > 65535 {
			return fmt.Errorf("SRV port %d out of range (1-65535)", *d.Port)
		}
	case "CAA":
		var d struct {
			Flags *int   `json:"flags"`
			Tag   string `json:"tag"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(data, &d); err != nil ||
			d.Flags == nil || d.Tag == "" || d.Value == "" {
			return errors.New("CAA record data must be {\"flags\": <0-255>, \"tag\": \"<issue|issuewild|iodef>\", \"value\": \"...\"}")
		}
		if *d.Flags < 0 || *d.Flags > 255 {
			return fmt.Errorf("CAA flags %d out of range (0-255)", *d.Flags)
		}
		switch d.Tag {
		case "issue", "issuewild", "iodef":
		default:
			return fmt.Errorf("CAA tag %q must be one of issue|issuewild|iodef", d.Tag)
		}
	default:
		// Unknown record types pass through; the pg enum cast on the
		// `type` column would have rejected anything we don't know
		// about before reaching this point.
	}
	return nil
}
