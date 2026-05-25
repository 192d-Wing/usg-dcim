// BIND zone-file rendering (PR 71). Pure functions ported from
// services.dns.render_zone_file. Used by GET /zones/{id}/preview
// and (later) by the bundle assembler. No DB calls, no I/O —
// caller hands in the zone + records and gets back the BIND text.
//
// Output is byte-equivalent with the Python renderer for any
// scope/zone the frontend would diff: same field order, same
// tab separators, same SOA layout, same RR-type-specific RDATA
// formatting. Tests pin the format per record type.
package dns

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// zoneSerial returns the SOA serial. Matches Python's _zone_serial:
// Unix timestamp of the zone's updated_at. Monotonic per zone and
// fits in 32 bits until 2106 — plenty for any DCIM deployment.
func zoneSerial(z dbq.DnsZone) int64 {
	return z.UpdatedAt.Unix()
}

// formatTTL — if the record has an explicit ttl, use it; else fall
// back to the zone's default_ttl.
func formatTTL(recordTTL *int32, zoneDefault int32) string {
	if recordTTL != nil {
		return fmt.Sprintf("%d", *recordTTL)
	}
	return fmt.Sprintf("%d", zoneDefault)
}

// formatRdata emits the type-specific RDATA half of a BIND RR line.
// Mirrors services.dns._format_rdata for every type DCIM supports.
// data is the JSON blob stored in dns_records.data (validated at
// write time by Pydantic / Go schema, so we trust the shape here).
func formatRdata(rtype string, data json.RawMessage) (string, error) {
	switch rtype {
	case "A", "AAAA", "CNAME", "NS", "PTR":
		var d struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", err
		}
		return d.Target, nil
	case "MX":
		var d struct {
			Priority int    `json:"priority"`
			Target   string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %s", d.Priority, d.Target), nil
	case "TXT":
		var d struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", err
		}
		// RFC 1035: quote and escape inner backslashes + quotes.
		t := strings.ReplaceAll(d.Text, `\`, `\\`)
		t = strings.ReplaceAll(t, `"`, `\"`)
		return `"` + t + `"`, nil
	case "SRV":
		var d struct {
			Priority int    `json:"priority"`
			Weight   int    `json:"weight"`
			Port     int    `json:"port"`
			Target   string `json:"target"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d %d %s", d.Priority, d.Weight, d.Port, d.Target), nil
	case "CAA":
		var d struct {
			Flags int    `json:"flags"`
			Tag   string `json:"tag"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", err
		}
		// RFC 6844: value is quoted.
		v := strings.ReplaceAll(d.Value, `"`, `\"`)
		return fmt.Sprintf(`%d %s "%s"`, d.Flags, d.Tag, v), nil
	}
	return "", fmt.Errorf("unknown record type %q", rtype)
}

// formatRecordLine emits one BIND RR line. Leading name uses '@'
// for the apex (matches Python: bare label is "" in DB).
func formatRecordLine(r dbq.DnsRecordForRender, zoneDefault int32) (string, error) {
	name := r.Name
	if name == "" {
		name = "@"
	}
	rdata, err := formatRdata(r.Type, r.Data)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\t%s\tIN\t%s\t%s",
		name, formatTTL(r.TTL, zoneDefault), r.Type, rdata), nil
}

// renderZoneFile assembles the BIND-format text for the preview
// endpoint. Sort order is (name, type) for diffable output.
// Records with malformed data fail the whole render — preview
// surfaces the problem rather than silently dropping the bad row.
func renderZoneFile(zone dbq.DnsZone, records []dbq.DnsRecordForRender) (string, error) {
	sorted := make([]dbq.DnsRecordForRender, len(records))
	copy(sorted, records)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Type < sorted[j].Type
	})

	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN %s.\n", zone.Name)
	fmt.Fprintf(&b, "$TTL %d\n", zone.DefaultTTL)
	// SOA: matches Python's exact formatting (same tabs, same
	// trailing ; serial / refresh / retry / expire / minimum).
	fmt.Fprintf(&b, "@\tIN\tSOA\t%s.%s. %s.%s. (\n",
		zone.SoaMname, zone.Name, zone.SoaRname, zone.Name)
	fmt.Fprintf(&b, "\t\t\t%d\t; serial\n", zoneSerial(zone))
	fmt.Fprintf(&b, "\t\t\t%d\t; refresh\n", zone.SoaRefresh)
	fmt.Fprintf(&b, "\t\t\t%d\t; retry\n", zone.SoaRetry)
	fmt.Fprintf(&b, "\t\t\t%d\t; expire\n", zone.SoaExpire)
	fmt.Fprintf(&b, "\t\t\t%d)\t; minimum\n", zone.SoaMinimum)
	b.WriteString("\n")
	for _, r := range sorted {
		line, err := formatRecordLine(r, zone.DefaultTTL)
		if err != nil {
			return "", err
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), nil
}
