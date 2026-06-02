// DNS bundle assembler (PR 30 — DNS bundle 7/N). Composes the
// renderers from PRs #247-#251 into the bundle response the
// collector consumes. Pure of DB: takes pre-loaded inputs and
// emits the BundleResult struct. Follow-up PR wires the HTTP
// endpoint + bundle-data loader from the database, then ingress
// cutover.
//
// Mirrors render_bundle_for_server in services/dns.py L2347. This
// PR ports the auth-server path; recursive servers (GoBGP, RPZ,
// _render_recursive_config) land in a follow-up.
package dns

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// BundleResult mirrors Python's render_bundle_for_server dict
// return at services/dns.py L2464. Wire shape matches byte-for-byte
// for the auth-server case; recursive fields (Gobgp,
// AnycastPrefixes) are nil/empty on auth.
type BundleResult struct {
	Engine          string            `json:"engine"`
	Corefile        string            `json:"corefile"`
	Zones           map[string]string `json:"zones"`
	Gobgp           any               `json:"gobgp"`
	KeyFiles        map[string]string `json:"key_files"`
	Etag            string            `json:"etag"`
	DnstapSocket    *string           `json:"dnstap_socket"`
	AnycastPrefixes []string          `json:"anycast_prefixes"`
}

// AuthBundleInput bundles the pre-loaded data the auth-path
// assembler needs. Loading from the DB lives in a follow-up PR;
// keeping the assembler pure-of-DB makes it trivially testable
// with synthetic fixtures + lets the HTTP layer fan out the
// reads however it likes.
type AuthBundleInput struct {
	Server            dbq.DnsServer
	Zones             []dbq.DnsZone
	RecordsByZone     map[uuid.UUID][]dbq.DnsRecordForBundle
	UnhealthyCheckIDs map[uuid.UUID]struct{}
	ExtraLinesByZone  map[uuid.UUID][]string
	// DNSSEC: BIND key-file basenames per zone name (for Corefile
	// signing blocks). KeyFiles is the file-name → text map fed to
	// the bundle response + etag.
	DnssecKeysByZone  map[string][]string
	Nsec3ParamsByZone map[string]*Nsec3Params
	KeyFiles          map[string]string
	// Catalog zone (RFC 9432). Empty CatalogName disables catalog
	// emission entirely (matches Python's "no catalog row or row
	// disabled" branch).
	CatalogName        string
	CatalogMembers     []dbq.DnsZone
	CatalogTransferACL []string
	// DnstapSocket may be nil; CorefileAuth omits the directive
	// when nil.
	DnstapSocket *string
}

// AssembleAuthBundle composes the auth-server bundle. Pure
// function: hand it pre-loaded inputs, get back the response shape
// the HTTP endpoint serializes. Wire-shape parity with Python is
// asserted by bundle-endpoint tests (follow-up PR) that snapshot
// full bundles against fixtures rendered by Python.
func AssembleAuthBundle(in AuthBundleInput) (BundleResult, error) {
	zonesDir := fmt.Sprintf("/var/lib/dcim-dns/%s/zones", in.Server.Role)
	keysDir := fmt.Sprintf("/var/lib/dcim-dns/%s/keys", in.Server.Role)

	zoneFiles := make(map[string]string, len(in.Zones))
	for _, z := range in.Zones {
		filtered := filterRecordsForBundle(in.RecordsByZone[z.ID], in.UnhealthyCheckIDs)
		text, err := renderZoneFileWithExtras(z, filtered, in.ExtraLinesByZone[z.ID])
		if err != nil {
			return BundleResult{}, fmt.Errorf("zone %s: %w", z.Name, err)
		}
		zoneFiles[filenameForZone(z.Name)] = text
	}

	if in.CatalogName != "" {
		// Catalog zone — one per fabric, served alongside the member
		// zones. defaultTTL=0 falls through to the renderer's 3600
		// default; serial=0 → auto from max(member.updated_at).
		catalogFile := RenderCatalogZone(in.CatalogName, in.CatalogMembers, 0, 0, nil)
		zoneFiles[filenameForZone(in.CatalogName)] = catalogFile
	}

	var keysDirPtr *string
	if len(in.DnssecKeysByZone) > 0 {
		keysDirPtr = &keysDir
	}
	corefileZoneNames := corefileZoneNames(in.Zones, in.CatalogName)
	transferACL := map[string][]string{}
	if in.CatalogName != "" && len(in.CatalogTransferACL) > 0 {
		transferACL[in.CatalogName] = in.CatalogTransferACL
	}
	corefile := RenderCorefileAuth(CorefileAuthInput{
		ZoneNames:         corefileZoneNames,
		ZonesDir:          zonesDir,
		KeysDir:           keysDirPtr,
		DnssecKeysByZone:  in.DnssecKeysByZone,
		Nsec3ParamsByZone: in.Nsec3ParamsByZone,
		DnstapSocket:      in.DnstapSocket,
		TransferAclByZone: transferACL,
	})

	etag := BundleEtag(EtagInput{
		Corefile: corefile,
		Zones:    zoneFiles,
		KeyFiles: in.KeyFiles,
	})

	keyFiles := in.KeyFiles
	if keyFiles == nil {
		keyFiles = map[string]string{}
	}

	return BundleResult{
		Engine:          "coredns",
		Corefile:        corefile,
		Zones:           zoneFiles,
		Gobgp:           nil,
		KeyFiles:        keyFiles,
		Etag:            etag,
		DnstapSocket:    in.DnstapSocket,
		AnycastPrefixes: nil,
	}, nil
}

// filterRecordsForBundle drops records whose health_check_id is
// in the unhealthy set (Python's `unhealthy_check_ids` filter at
// render_zone_file L802), then converts the bundle-record shape
// to the renderer-record shape the existing renderZoneFile accepts.
func filterRecordsForBundle(
	records []dbq.DnsRecordForBundle,
	unhealthy map[uuid.UUID]struct{},
) []dbq.DnsRecordForRender {
	out := make([]dbq.DnsRecordForRender, 0, len(records))
	for _, r := range records {
		if r.HealthCheckID != nil {
			if _, hit := unhealthy[*r.HealthCheckID]; hit {
				continue
			}
		}
		out = append(out, dbq.DnsRecordForRender{
			ID: r.ID, Name: r.Name, Type: r.Type, TTL: r.TTL, Data: r.Data,
		})
	}
	return out
}

// renderZoneFileWithExtras wraps renderZoneFile and appends
// operator-supplied extra_lines (DS records for delegated child
// zones + CDNSKEY/CDS for RFC 7344) below the regular records.
// Matches Python's render_zone_file when extra_lines is non-empty
// (services/dns.py L824-L827).
func renderZoneFileWithExtras(
	zone dbq.DnsZone,
	records []dbq.DnsRecordForRender,
	extraLines []string,
) (string, error) {
	base, err := renderZoneFile(zone, records)
	if err != nil {
		return "", err
	}
	if len(extraLines) == 0 {
		return base, nil
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(base, "\n"))
	b.WriteString("\n\n")
	b.WriteString("; --- DS (for DCIM-owned children) + CDS/CDNSKEY (RFC 7344) ---\n")
	for _, line := range extraLines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// filenameForZone — matches Python's _filename_for_zone (`<name>.zone`).
func filenameForZone(zoneName string) string {
	return zoneName + ".zone"
}

// corefileZoneNames — every served zone, including the catalog
// when present. RenderCorefileAuth sorts internally for stability.
func corefileZoneNames(zones []dbq.DnsZone, catalogName string) []string {
	names := make([]string, 0, len(zones)+1)
	for _, z := range zones {
		names = append(names, z.Name)
	}
	if catalogName != "" {
		names = append(names, catalogName)
	}
	return names
}
