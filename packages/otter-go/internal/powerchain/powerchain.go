// Package powerchain holds the rack-level power-graph rollup used by
// the rack-detail dashboard. Mirrors packages/otter/src/dcim/services/
// power_chain.py byte-for-byte. Walks the asset → PDU → outlet graph
// to produce the per-asset connection list + redundancy verdict and
// the PDU summary's used-outlets counts.
package powerchain

import (
	"context"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// Result is the {per_asset, pdus} wire shape from compute_power_chain.
// Maps stay keyed on string-stringified UUID to match the Python dict
// keys (finch JSON parses them as strings).
type Result struct {
	PerAsset map[string]AssetEntry `json:"per_asset"`
	PDUs     []PduSummary          `json:"pdus"`
}

// AssetEntry is the per-asset rollup — which PDU sides feed it,
// the populated connections, and the redundancy verdict.
type AssetEntry struct {
	SidesCovered []string     `json:"sides_covered"`
	Connections  []Connection `json:"connections"`
	Redundancy   string       `json:"redundancy"`
}

// Connection mirrors Python's _connection_row — id+name of the PDU,
// the side label, the outlet identity, and the psu_index of the
// attached asset.
type Connection struct {
	PduID          string  `json:"pdu_id"`
	PduName        string  `json:"pdu_name"`
	PduSide        *string `json:"pdu_side"`
	OutletID       string  `json:"outlet_id"`
	OutletPosition int32   `json:"outlet_position"`
	OutletLabel    *string `json:"outlet_label"`
	PsuIndex       int32   `json:"psu_index"`
}

// PduSummary mirrors Python's _pdu_summary_row.
type PduSummary struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Side         *string `json:"side"`
	Mount        string  `json:"mount"`
	Face         string  `json:"face"`
	TotalOutlets int     `json:"total_outlets"`
	UsedOutlets  int     `json:"used_outlets"`
}

// Querier is the slice of dbq methods Compute needs. The existing
// ListPowerConnectionsByOutletIDs (returns []PowerConnection) is
// reused; ListOutletsByPduIDs is the new bulk lookup.
type Querier interface {
	ListOutletsByPduIDs(ctx context.Context, pduIDs []uuid.UUID) ([]dbq.OutletForPowerChainRow, error)
	ListPowerConnectionsByOutletIDs(ctx context.Context, outletIDs []uuid.UUID) ([]dbq.PowerConnection, error)
}

// Compute returns the power-chain rollup for the given assets. Mirrors
// services/power_chain.py::compute_power_chain.
func Compute(ctx context.Context, q Querier, assets []dbq.Asset) (Result, error) {
	if len(assets) == 0 {
		return Result{PerAsset: map[string]AssetEntry{}, PDUs: []PduSummary{}}, nil
	}
	pdus := pduAssetsOf(assets)
	outlets, conns, err := loadOutletsAndConnections(ctx, q, pdus)
	if err != nil {
		return Result{}, err
	}
	outletByID, outletsByPdu := indexOutlets(outlets)
	pduByID := indexPdusByID(pdus)
	perAsset := emptyPerAsset(assets)

	for _, c := range conns {
		outlet, okO := outletByID[c.OutletID]
		if !okO {
			continue
		}
		pdu, okP := pduByID[outlet.PduAssetID]
		if !okP {
			continue
		}
		key := c.AssetID.String()
		entry := perAsset[key]
		if entry.Connections == nil {
			entry = emptyEntry()
		}
		entry.Connections = append(entry.Connections, buildConnection(pdu, outlet, c))
		perAsset[key] = entry
	}

	for _, a := range assets {
		key := a.ID.String()
		entry := perAsset[key]
		sides, verdict := ClassifyRedundancy(a.Kind, entry.Connections)
		entry.SidesCovered = sides
		entry.Redundancy = verdict
		perAsset[key] = entry
	}

	usedOutletIDs := usedOutletIDsFrom(conns)
	pduSummary := make([]PduSummary, 0, len(pdus))
	for _, p := range pdus {
		pduSummary = append(pduSummary, buildPduSummary(p, outletsByPdu[p.ID], usedOutletIDs))
	}
	return Result{PerAsset: perAsset, PDUs: pduSummary}, nil
}

// ClassifyRedundancy is the pure classifier — given a device's kind
// and its connections, return (sides_covered, verdict). Extracted so
// tests don't need a DB. Mirrors services/power_chain.py exactly:
//
//	PDU itself                            → ([], "n/a")
//	no connections                        → (sides_seen, "unpowered")
//	≥2 distinct PDU sides                 → (sides_seen, "redundant")
//	≥1 connection, single PDU side seen   → (sides_seen, "single")
func ClassifyRedundancy(kind string, connections []Connection) ([]string, string) {
	if kind == "pdu" {
		return []string{}, "n/a"
	}
	sides := dedupedSides(connections)
	if len(connections) == 0 {
		return sides, "unpowered"
	}
	if len(sides) >= 2 {
		return sides, "redundant"
	}
	return sides, "single"
}

// ---- internal helpers ----

func pduAssetsOf(assets []dbq.Asset) []dbq.Asset {
	out := []dbq.Asset{}
	for _, a := range assets {
		if a.Kind == "pdu" {
			out = append(out, a)
		}
	}
	return out
}

func loadOutletsAndConnections(ctx context.Context, q Querier, pdus []dbq.Asset) (
	[]dbq.OutletForPowerChainRow, []dbq.PowerConnection, error,
) {
	if len(pdus) == 0 {
		return nil, nil, nil
	}
	pduIDs := make([]uuid.UUID, len(pdus))
	for i, p := range pdus {
		pduIDs[i] = p.ID
	}
	outlets, err := q.ListOutletsByPduIDs(ctx, pduIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(outlets) == 0 {
		return outlets, nil, nil
	}
	outletIDs := make([]uuid.UUID, len(outlets))
	for i, o := range outlets {
		outletIDs[i] = o.ID
	}
	conns, err := q.ListPowerConnectionsByOutletIDs(ctx, outletIDs)
	if err != nil {
		return nil, nil, err
	}
	return outlets, conns, nil
}

func indexOutlets(outlets []dbq.OutletForPowerChainRow) (
	map[uuid.UUID]dbq.OutletForPowerChainRow, map[uuid.UUID][]dbq.OutletForPowerChainRow,
) {
	byID := make(map[uuid.UUID]dbq.OutletForPowerChainRow, len(outlets))
	byPdu := map[uuid.UUID][]dbq.OutletForPowerChainRow{}
	for _, o := range outlets {
		byID[o.ID] = o
		byPdu[o.PduAssetID] = append(byPdu[o.PduAssetID], o)
	}
	return byID, byPdu
}

func indexPdusByID(pdus []dbq.Asset) map[uuid.UUID]dbq.Asset {
	out := make(map[uuid.UUID]dbq.Asset, len(pdus))
	for _, p := range pdus {
		out[p.ID] = p
	}
	return out
}

func emptyPerAsset(assets []dbq.Asset) map[string]AssetEntry {
	out := make(map[string]AssetEntry, len(assets))
	for _, a := range assets {
		out[a.ID.String()] = emptyEntry()
	}
	return out
}

func emptyEntry() AssetEntry {
	return AssetEntry{
		SidesCovered: []string{},
		Connections:  []Connection{},
		Redundancy:   "n/a",
	}
}

func buildConnection(pdu dbq.Asset, outlet dbq.OutletForPowerChainRow, c dbq.PowerConnection) Connection {
	return Connection{
		PduID:          pdu.ID.String(),
		PduName:        pdu.Name,
		PduSide:        pdu.PduSide,
		OutletID:       outlet.ID.String(),
		OutletPosition: outlet.Position,
		OutletLabel:    outlet.Label,
		PsuIndex:       c.PsuIndex,
	}
}

func buildPduSummary(p dbq.Asset, outletsForP []dbq.OutletForPowerChainRow, usedOutletIDs map[uuid.UUID]struct{}) PduSummary {
	used := 0
	for _, o := range outletsForP {
		if _, ok := usedOutletIDs[o.ID]; ok {
			used++
		}
	}
	return PduSummary{
		ID:           p.ID.String(),
		Name:         p.Name,
		Side:         p.PduSide,
		Mount:        p.Mount,
		Face:         p.Face,
		TotalOutlets: len(outletsForP),
		UsedOutlets:  used,
	}
}

func usedOutletIDsFrom(conns []dbq.PowerConnection) map[uuid.UUID]struct{} {
	out := make(map[uuid.UUID]struct{}, len(conns))
	for _, c := range conns {
		out[c.OutletID] = struct{}{}
	}
	return out
}

// dedupedSides returns the sorted, deduplicated PDU-side labels seen
// in the connection list. Mirrors Python's
// `sorted({c["pdu_side"] for c in connections if c.get("pdu_side")})`.
func dedupedSides(conns []Connection) []string {
	seen := map[string]struct{}{}
	for _, c := range conns {
		if c.PduSide == nil || *c.PduSide == "" {
			continue
		}
		seen[*c.PduSide] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	// sort lexicographically (Python's sorted())
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
