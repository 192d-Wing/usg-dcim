// Catalog + DNSSEC + extras loaders for the bundle endpoint
// (PR 31 — DNS bundle 8/N). These functions read from the DB and
// translate into the AuthBundleInput sub-maps the assembler consumes.
// Pure of HTTP — the handler stitches them together.
//
// Mirrors:
//   - _add_catalog_zone_files (services/dns.py L1844)
//   - _catalog_transfer_acl_map (L1826)
//   - _dnssec_artifacts_for_zones (L1898)
//   - _zone_extra_lines (L2326)
//   - _cdnskey_cds_lines_by_zone (L2262)
//   - _child_ds_lines_by_parent (L2289)
package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// catalogQuerier is the slice of methods the catalog loader needs.
type catalogQuerier interface {
	GetEnabledDnsCatalogZoneByFabric(ctx context.Context, fabricID uuid.UUID) (dbq.DnsCatalogZone, error)
	ListEnabledAuthDnsServersByFabric(ctx context.Context, fabricID uuid.UUID) ([]dbq.ListEnabledAuthDnsServersByFabricRow, error)
}

// loadCatalogForBundle resolves the fabric's catalog zone (if any),
// the auth-server unicast IPs that get embedded as RFC 9432 §4.2.3
// `primaries` records, and the member-zone set. Returns ("", nil,
// nil) when the fabric has no catalog row or the row is disabled —
// the assembler treats that as "no catalog emission".
func loadCatalogForBundle(
	ctx context.Context, q catalogQuerier, fabricID uuid.UUID, zones []dbq.DnsZone,
) (string, []dbq.DnsZone, []string, error) {
	catalog, err := q.GetEnabledDnsCatalogZoneByFabric(ctx, fabricID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, nil, nil
		}
		return "", nil, nil, fmt.Errorf("catalog lookup: %w", err)
	}
	servers, err := q.ListEnabledAuthDnsServersByFabric(ctx, fabricID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("catalog primaries lookup: %w", err)
	}
	// Members: every non-frozen zone in the fabric. The caller's
	// `zones` slice is already non-frozen-filtered, so just hand it
	// through (matching Python's `if not getattr(z, "frozen", False)`
	// which is also a no-op on the post-filter).
	members := zones
	primaries := make([]string, 0, len(servers))
	for _, s := range servers {
		if s.UnicastIP != "" {
			// inet textcast preserves any CIDR suffix; strip before
			// emitting the property record (matches Python's
			// `str(s.unicast_ip).split("/", 1)[0]`).
			bare := s.UnicastIP
			if i := strings.IndexByte(bare, '/'); i >= 0 {
				bare = bare[:i]
			}
			primaries = append(primaries, bare)
		}
	}
	if len(primaries) == 0 {
		primaries = nil
	}
	return catalog.Name, members, primaries, nil
}

// dnssecQuerier reads DNSSEC keys for the signed zones.
type dnssecQuerier interface {
	ListDnsKeysByZoneIDs(ctx context.Context, zoneIDs []uuid.UUID) ([]dbq.DnsKey, error)
}

// DnssecArtifacts bundles the three artifacts the assembler consumes
// for DNSSEC: per-filename key-file text, per-zone-name basenames
// the Corefile signing block references, and per-zone NSEC3 params
// when the zone runs in NSEC3 mode.
type DnssecArtifacts struct {
	KeyFiles          map[string]string
	DnssecKeysByZone  map[string][]string
	Nsec3ParamsByZone map[string]*Nsec3Params
}

// loadDnssecArtifacts mirrors Python's _dnssec_artifacts_for_zones
// (L1898). Returns empty maps when no zone is signed.
//
// `decryptPEM` is the caller-supplied decryption hook for at-rest
// PEM unwrap (the bundle assembler stays pure of Fernet/settings
// state). When the caller passes nil, the encrypted PEM is fed
// directly to the renderer — RenderBindPrivateKeyFile then 5xxs on
// the malformed PEM, which is the correct behavior for unconfigured
// at-rest encryption (catches the misconfiguration loudly).
func loadDnssecArtifacts(
	ctx context.Context, q dnssecQuerier, zones []dbq.DnsZone,
	decryptPEM func(string) string,
) (DnssecArtifacts, error) {
	out := DnssecArtifacts{
		KeyFiles:          map[string]string{},
		DnssecKeysByZone:  map[string][]string{},
		Nsec3ParamsByZone: map[string]*Nsec3Params{},
	}
	signed := signedZones(zones)
	if len(signed) == 0 {
		return out, nil
	}
	byZone, err := fetchKeysByZone(ctx, q, signed)
	if err != nil {
		return out, err
	}
	for _, z := range signed {
		if err := mergeDnssecForZone(&out, z, byZone[z.ID], decryptPEM); err != nil {
			return out, err
		}
	}
	return out, nil
}

func signedZones(zones []dbq.DnsZone) []dbq.DnsZone {
	out := make([]dbq.DnsZone, 0, len(zones))
	for _, z := range zones {
		if z.Signed {
			out = append(out, z)
		}
	}
	return out
}

func fetchKeysByZone(
	ctx context.Context, q dnssecQuerier, zones []dbq.DnsZone,
) (map[uuid.UUID][]dbq.DnsKey, error) {
	ids := make([]uuid.UUID, len(zones))
	for i, z := range zones {
		ids[i] = z.ID
	}
	all, err := q.ListDnsKeysByZoneIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("dnssec keys lookup: %w", err)
	}
	out := make(map[uuid.UUID][]dbq.DnsKey, len(zones))
	for _, k := range all {
		if k.ZoneID == nil {
			continue
		}
		out[*k.ZoneID] = append(out[*k.ZoneID], k)
	}
	return out, nil
}

func mergeDnssecForZone(
	out *DnssecArtifacts, z dbq.DnsZone, zoneKeys []dbq.DnsKey,
	decryptPEM func(string) string,
) error {
	if len(zoneKeys) == 0 {
		return nil
	}
	pems := decryptedPemMap(zoneKeys, decryptPEM)
	files, err := RenderDnssecKeyFiles(z.Name, zoneKeys, pems)
	if err != nil {
		return fmt.Errorf("dnssec render for zone %s: %w", z.Name, err)
	}
	basenames := make([]string, 0, len(files)/2)
	for _, e := range files {
		out.KeyFiles[e.Filename] = e.Content
		if strings.HasSuffix(e.Filename, ".key") {
			basenames = append(basenames, strings.TrimSuffix(e.Filename, ".key"))
		}
	}
	out.DnssecKeysByZone[z.Name] = basenames
	if z.Nsec3Salt != nil {
		out.Nsec3ParamsByZone[z.Name] = &Nsec3Params{
			Salt:       *z.Nsec3Salt,
			Iterations: z.Nsec3Iterations,
			OptOut:     z.Nsec3OptOut,
		}
	}
	return nil
}

func decryptedPemMap(keys []dbq.DnsKey, decryptPEM func(string) string) map[int32]string {
	out := make(map[int32]string, len(keys))
	for _, k := range keys {
		pem := k.PrivatePem
		if decryptPEM != nil {
			pem = decryptPEM(pem)
		}
		out[k.KeyTag] = pem
	}
	return out
}

// loadZoneExtraLines builds the per-zone extras map combining DS
// records for delegated child zones (zone as parent) and RFC 7344
// CDNSKEY/CDS records (zone as signed child).
//
// Mirrors _zone_extra_lines + _child_ds_lines_by_parent +
// _cdnskey_cds_lines_by_zone.
func loadZoneExtraLines(
	ctx context.Context, q dnssecQuerier, zones []dbq.DnsZone,
) (map[uuid.UUID][]string, error) {
	if len(zones) == 0 {
		return map[uuid.UUID][]string{}, nil
	}
	keysByZone, err := fetchKeysByZone(ctx, q, zones)
	if err != nil {
		return nil, fmt.Errorf("extras keys lookup: %w", err)
	}
	cdsByZone := buildCdsByZone(zones, keysByZone)
	dsByParent := buildChildDSByParent(zones, keysByZone)

	// Merge — parent DS first, then child CDS/CDNSKEY (matches
	// Python's `(child_ds.get(z.id) or []) + (cds.get(z.id) or [])`).
	out := map[uuid.UUID][]string{}
	for _, z := range zones {
		merged := append([]string(nil), dsByParent[z.ID]...)
		merged = append(merged, cdsByZone[z.ID]...)
		if len(merged) > 0 {
			out[z.ID] = merged
		}
	}
	return out, nil
}

func buildCdsByZone(
	zones []dbq.DnsZone, keysByZone map[uuid.UUID][]dbq.DnsKey,
) map[uuid.UUID][]string {
	out := map[uuid.UUID][]string{}
	for _, z := range zones {
		if !z.Signed || !z.PublishCds {
			continue
		}
		lines := RenderCdnskeyCdsLines(z.Name, keysByZone[z.ID])
		if len(lines) > 0 {
			out[z.ID] = lines
		}
	}
	return out
}

func buildChildDSByParent(
	zones []dbq.DnsZone, keysByZone map[uuid.UUID][]dbq.DnsKey,
) map[uuid.UUID][]string {
	byName := make(map[string]dbq.DnsZone, len(zones))
	for _, z := range zones {
		byName[normalizeZoneName(z.Name)] = z
	}
	out := map[uuid.UUID][]string{}
	for _, child := range zones {
		parent, ok := findDirectParent(child, byName)
		if !ok {
			continue
		}
		for _, line := range renderChildDSLines(child, keysByZone[child.ID]) {
			out[parent.ID] = append(out[parent.ID], line)
		}
	}
	return out
}

// findDirectParent returns the in-set parent zone of `child` when
// the child is signed and the parent is a direct single-label
// ancestor (e.g. site.example.com → example.com). Multi-label
// nesting is intentionally not supported.
func findDirectParent(child dbq.DnsZone, byName map[string]dbq.DnsZone) (dbq.DnsZone, bool) {
	if !child.Signed {
		return dbq.DnsZone{}, false
	}
	childName := normalizeZoneName(child.Name)
	dot := strings.IndexByte(childName, '.')
	if dot < 0 {
		return dbq.DnsZone{}, false
	}
	parentName := childName[dot+1:]
	parent, ok := byName[parentName]
	if !ok || parent.ID == child.ID {
		return dbq.DnsZone{}, false
	}
	return parent, true
}

// renderChildDSLines computes the DS BIND-format lines for a child
// zone's active KSKs. Uses the existing computeDSRecord (PR #79)
// for byte-equivalent digests with the GET /zones/{id}/ds-records
// endpoint.
func renderChildDSLines(child dbq.DnsZone, keys []dbq.DnsKey) []string {
	var out []string
	for _, k := range keys {
		if k.Role != "ksk" {
			continue
		}
		if k.RetiredAt != nil {
			continue
		}
		ds, err := computeDSRecord(child.Name, k)
		if err != nil {
			continue
		}
		out = append(out, ds.RR)
	}
	return out
}

// normalizeZoneName strips trailing dot + lowercases to match
// Python's `name.rstrip(".").lower()` (services/dns.py L2306).
func normalizeZoneName(name string) string {
	return strings.ToLower(strings.TrimRight(name, "."))
}

