// Split-horizon view loader (PR 32 — DNS bundle 9/N). Ports
// Python's _views_and_zone_files_for_split_horizon from
// services/dns.py L1745. For each zone whose records bind to a
// DnsView, emit:
//   - one default zone file (null-view records only — served to
//     clients that don't match any view's CIDR list)
//   - one per-view zone file (records matching that view OR null-
//     view records — the view sees its overrides + the default
//     rrset)
// Plus the per-zone ViewConfig slice the Corefile renderer needs
// to emit one view-scoped server block per view.
package dns

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type viewsQuerier interface {
	ListDnsViewsByFabric(ctx context.Context, fabricID uuid.UUID) ([]dbq.DnsView, error)
}

// SplitHorizonResult bundles what the assembler needs to honor
// view-scoped record sets. ZoneFiles maps filename → text and
// includes BOTH the default file and the per-view files for any
// zone that has view-bound records. ViewsByZone is the per-zone
// ViewConfig slice (priority order) for the Corefile renderer.
type SplitHorizonResult struct {
	ZoneFiles   map[string]string
	ViewsByZone map[string][]ViewConfig
}

// loadSplitHorizonZoneFiles fetches views per fabric, identifies
// zones whose records bind to a view, and renders both the default
// and the per-view zone files.
func loadSplitHorizonZoneFiles(
	ctx context.Context, q viewsQuerier,
	zones []dbq.DnsZone,
	recordsByZone map[uuid.UUID][]dbq.DnsRecordForBundle,
	unhealthy map[uuid.UUID]struct{},
) (SplitHorizonResult, error) {
	out := SplitHorizonResult{
		ZoneFiles:   map[string]string{},
		ViewsByZone: map[string][]ViewConfig{},
	}
	fabricIDs := uniqueFabricIDs(zones)
	if len(fabricIDs) == 0 {
		return out, nil
	}
	viewsByFabric, err := fetchViewsByFabric(ctx, q, fabricIDs)
	if err != nil {
		return out, err
	}
	for _, z := range zones {
		recs := recordsByZone[z.ID]
		if !hasViewBoundRecord(recs) {
			continue
		}
		zoneViews := viewsByFabric[z.FabricID]
		if len(zoneViews) == 0 {
			// Records reference views but none exist for the fabric —
			// the Python helper skips per-view rendering and lets the
			// default render path handle it. Match that here.
			continue
		}
		if err := emitSplitHorizonFiles(&out, z, recs, zoneViews, unhealthy); err != nil {
			return out, err
		}
	}
	return out, nil
}

func uniqueFabricIDs(zones []dbq.DnsZone) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(zones))
	out := make([]uuid.UUID, 0, len(zones))
	for _, z := range zones {
		if _, ok := seen[z.FabricID]; ok {
			continue
		}
		seen[z.FabricID] = struct{}{}
		out = append(out, z.FabricID)
	}
	return out
}

func fetchViewsByFabric(
	ctx context.Context, q viewsQuerier, fabricIDs []uuid.UUID,
) (map[uuid.UUID][]dbq.DnsView, error) {
	out := make(map[uuid.UUID][]dbq.DnsView, len(fabricIDs))
	for _, fid := range fabricIDs {
		views, err := q.ListDnsViewsByFabric(ctx, fid)
		if err != nil {
			return nil, fmt.Errorf("views lookup for fabric %s: %w", fid, err)
		}
		if len(views) > 0 {
			out[fid] = views
		}
	}
	return out, nil
}

func hasViewBoundRecord(recs []dbq.DnsRecordForBundle) bool {
	for _, r := range recs {
		if r.ViewID != nil {
			return true
		}
	}
	return false
}

func emitSplitHorizonFiles(
	out *SplitHorizonResult, z dbq.DnsZone,
	recs []dbq.DnsRecordForBundle, zoneViews []dbq.DnsView,
	unhealthy map[uuid.UUID]struct{},
) error {
	// Default file — null-view records only.
	defaultRecs := filterRecordsForBundle(filterRecordsForView(recs, nil), unhealthy)
	defaultText, err := renderZoneFile(z, defaultRecs)
	if err != nil {
		return fmt.Errorf("zone %s default view: %w", z.Name, err)
	}
	out.ZoneFiles[zoneViewFilenameDefault(z.Name)] = defaultText

	perZone := make([]ViewConfig, 0, len(zoneViews))
	for _, view := range zoneViews {
		viewRecs := filterRecordsForBundle(filterRecordsForView(recs, &view.ID), unhealthy)
		text, err := renderZoneFile(z, viewRecs)
		if err != nil {
			return fmt.Errorf("zone %s view %s: %w", z.Name, view.Name, err)
		}
		viewName := view.Name
		out.ZoneFiles[zoneViewFilename(z.Name, &viewName)] = text
		perZone = append(perZone, ViewConfig{
			Name:  view.Name,
			CIDRs: decodeViewCIDRs(view.MatchCidrs),
		})
	}
	out.ViewsByZone[z.Name] = perZone
	return nil
}

// filterRecordsForView keeps records bound to `viewID` plus all
// null-view records (Python: `r.view_id == view.id or r.view_id is
// None`). Passing viewID=nil filters to null-view only — used for
// the default zone file.
func filterRecordsForView(
	recs []dbq.DnsRecordForBundle, viewID *uuid.UUID,
) []dbq.DnsRecordForBundle {
	out := make([]dbq.DnsRecordForBundle, 0, len(recs))
	for _, r := range recs {
		if viewID == nil {
			if r.ViewID == nil {
				out = append(out, r)
			}
			continue
		}
		if r.ViewID == nil || *r.ViewID == *viewID {
			out = append(out, r)
		}
	}
	return out
}

// decodeViewCIDRs turns the JSONB match_cidrs column into a []string.
// Bad JSON → empty list (the view simply never matches; the
// Corefile renderer's viewExpr falls through to literal `false`).
func decodeViewCIDRs(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// zoneViewFilenameDefault is shorthand for the default-view
// filename (matches Python's _zone_view_filename(name, None)).
func zoneViewFilenameDefault(zoneName string) string {
	return zoneViewFilename(zoneName, nil)
}
