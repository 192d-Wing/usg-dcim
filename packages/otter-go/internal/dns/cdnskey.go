// RFC 7344 CDNSKEY + CDS apex records (PR 26 — DNS bundle 3/N).
// Pure helper extracted from render_cdnskey_cds_lines in
// services/dns.py L527. Returns the BIND-format presentation lines
// the zone file appends below its regular records when the zone
// has publish_cds=true.
package dns

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// RenderCdnskeyCdsLines emits CDNSKEY + CDS RRs (one pair per
// active KSK) at the zone apex. Retired KSKs are skipped — emitting
// a stale KSK's CDS would mislead RFC 8078 parent scanners into
// keeping a dead key.
//
// Returns an empty slice when no KSK qualifies (no active KSKs,
// or all KSKs retired).
func RenderCdnskeyCdsLines(zoneName string, keys []dbq.DnsKey) []string {
	fqdn := strings.TrimRight(zoneName, ".") + "."
	nameWire := dnsWireName(fqdn)

	var out []string
	for _, k := range keys {
		if k.Role != "ksk" {
			continue
		}
		if k.RetiredAt != nil {
			continue
		}
		alg := dnssecAlgNumber(k.Algorithm)
		if alg == 0 {
			continue
		}
		flags := keyFlags(k.Role)
		pubBytes, err := base64.StdEncoding.DecodeString(k.PublicKeyB64)
		if err != nil {
			continue
		}

		// CDNSKEY @ apex — same wire format as DNSKEY.
		out = append(out, fmt.Sprintf("@\tIN\tCDNSKEY\t%d 3 %d %s",
			flags, alg, k.PublicKeyB64))

		// CDS @ apex — SHA-256 digest of canonical name + DNSKEY rdata.
		rdata := make([]byte, 0, 4+len(pubBytes))
		rdata = append(rdata, byte(flags>>8), byte(flags&0xff))
		rdata = append(rdata, 3, byte(alg))
		rdata = append(rdata, pubBytes...)
		digest := strings.ToUpper(hex.EncodeToString(sha256Digest(append(nameWire, rdata...))))
		out = append(out, fmt.Sprintf("@\tIN\tCDS\t%d %d 2 %s",
			k.KeyTag, alg, digest))
	}
	return out
}

func sha256Digest(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
