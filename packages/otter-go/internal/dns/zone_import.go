// PR 82 — POST /dns/zones/{id}/import.
//
// Parses a BIND-format zone file (via miekg/dns) and bulk-inserts
// its records into the target zone, replacing existing source=manual
// rows. IPAM-projected rows (source=ipam / ddns) are left alone —
// they're owned by the sync job.
//
// Dry-run returns the parsed view without committing — useful for
// the UI's import preview / diff.
package dns

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type importPayload struct {
	Text      string `json:"text"`
	DryRun    bool   `json:"dry_run"`
	UpdateSoa bool   `json:"update_soa"`
}

type importDryRunResponse struct {
	ZoneID             string      `json:"zone_id"`
	WouldAdd           int         `json:"would_add"`
	WouldReplaceManual bool        `json:"would_replace_manual"`
	Warnings           []string    `json:"warnings"`
	Parsed             *bindParsed `json:"parsed"`
}

type importApplyResponse struct {
	ZoneID         string   `json:"zone_id"`
	Added          int      `json:"added"`
	RemovedManual  int      `json:"removed_manual"`
	Warnings       []string `json:"warnings"`
}

func (h *Handler) importZone(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	var payload importPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if payload.Text == "" {
		httpx.Error(w, http.StatusBadRequest, "text is required")
		return
	}
	zone, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if !h.enforceFabric(w, r, zone.FabricID, "dns:zones:update") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before importing")
		return
	}
	parsed, err := parseBindZone(payload.Text, zone.Name+".")
	if err != nil {
		// BindImport errors are user-fixable (missing SOA, syntax) —
		// map to 422 so the UI surfaces the message cleanly.
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if payload.DryRun {
		httpx.JSON(w, http.StatusOK, importDryRunResponse{
			ZoneID:             id.String(),
			WouldAdd:           len(parsed.Records),
			WouldReplaceManual: true,
			Warnings:           parsed.Warnings,
			Parsed:             parsed,
		})
		return
	}

	// Replace existing manual rows; preserve IPAM/DDNS-projected ones.
	removed, err := h.Q.DeleteManualRecordsInZone(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}

	added := 0
	for _, rec := range parsed.Records {
		// Record type validation is done in the parser, but the
		// DnsRecordType enum in PG only accepts a fixed set —
		// CreateDnsRecord will reject unknown types at the SQL
		// layer if a future parser change introduces one.
		dataJSON, jerr := json.Marshal(rec.Data)
		if jerr != nil {
			parsed.Warnings = append(parsed.Warnings,
				"failed to encode record data for "+rec.Name)
			continue
		}
		if _, err := h.Q.CreateDnsRecord(r.Context(), dbq.CreateDnsRecordParams{
			ZoneID: id, Name: rec.Name, Type: rec.Type,
			TTL: rec.TTL, Data: dataJSON,
		}); err != nil {
			parsed.Warnings = append(parsed.Warnings,
				"DB insert failed for "+rec.Name+": "+err.Error())
			continue
		}
		added++
	}

	if payload.UpdateSoa {
		// mname/rname store the left-most label only (matches
		// Python: `soa.mname.rstrip(".").split(".", 1)[0]`).
		mname := firstLabel(parsed.Soa.Mname)
		rname := firstLabel(parsed.Soa.Rname)
		// Use pointers because the SQL coalesces NULL → existing.
		mnameP, rnameP := &mname, &rname
		ref, ret, exp, mini := parsed.Soa.Refresh, parsed.Soa.Retry, parsed.Soa.Expire, parsed.Soa.Minimum
		dttl := parsed.DefaultTTL
		if err := h.Q.UpdateDnsZoneSoa(r.Context(), dbq.UpdateDnsZoneSoaParams{
			ID: id, SoaMname: mnameP, SoaRname: rnameP,
			SoaRefresh: &ref, SoaRetry: &ret, SoaExpire: &exp, SoaMinimum: &mini,
			DefaultTTL: &dttl,
		}); err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
	} else {
		// Even without SOA update, bump updated_at so the bundle
		// etag changes and the renderer picks up the new records.
		if _, err := h.Q.TouchDnsZone(r.Context(), id); err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
	}

	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "dns_zone.import_bind",
		TargetType: "dns_zone",
		TargetID:   id.String(),
	})

	httpx.JSON(w, http.StatusOK, importApplyResponse{
		ZoneID:        id.String(),
		Added:         added,
		RemovedManual: len(removed),
		Warnings:      parsed.Warnings,
	})
}

// firstLabel returns the left-most label of a dotted name —
// "ns1.example.com" → "ns1". Empty string in stays empty. Matches
// Python's `mname.rstrip(".").split(".", 1)[0]`.
func firstLabel(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return s[:i]
		}
	}
	return s
}
