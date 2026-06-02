// Lease parsing helpers — Go port of the pure helpers from
// services/kea.py:50-90 + the per-service extractor at line 205.
// The HTTP client (client.go) returns Kea's raw per-service response
// list; these functions distill it into the ParsedLease records the
// sync orchestrator (PR 15) feeds into IPAddress upserts.
//
// Pure: no DB, no HTTP. The orchestrator owns ListLeases4/6 and the
// SQL writes.

package kea

import (
	"encoding/json"
	"strings"
	"time"
)

// Kea lease state codes. From the Kea documentation:
//
//   0 = default (active)
//   1 = declined (DHCP DECLINE — client refused the offer)
//   2 = expired-reclaimed (we explicitly took it back)
//
// Declined and expired-reclaimed leases are SKIPPED — they don't
// represent an active binding, so DCIM shouldn't materialize them as
// active IPAddress rows. Matches services/kea.py:77-78.
const (
	leaseStateActive            = 0
	leaseStateDeclined          = 1
	leaseStateExpiredReclaimed  = 2
)

// ParsedLease mirrors Python's ParsedLease dataclass at
// services/kea.py:38. Address is the canonical IP form (the parser
// trims whitespace; the matcher does any further normalization).
// MAC carries the v4 hw-address; v6 leases substitute duid here since
// they don't have a MAC at this layer (the orchestrator stores it in
// the same column for both families — RFC 8415 DUIDs go in dhcp_mac
// historically, dhcp_duid was added later).
type ParsedLease struct {
	Address    string
	MAC        string
	Hostname   string
	ValidUntil *time.Time
	State      int
}

// LeaseValidUntil computes the lease expiry from `cltt` (client-
// last-transmission-time, unix-seconds) + `valid-lft` (lease
// lifetime in seconds). Either nil → nil expiry; negative inputs →
// nil. Python additionally catches OSError from datetime.fromtime
// stamp (post-2038 on 32-bit, certain Windows edge cases) and
// returns None; Go's time.Unix never fails so those exotic cases
// produce a valid far-future time here. Realistic Kea values are
// sub-2038 so this divergence is theoretical.
//
// nil expiry maps to Python's "expiry unknown, don't age out
// aggressively" posture at services/kea.py:50-63. The dhcp_age_out
// cron (PR 16) only deletes IPAddress rows whose expiry IS set and
// older than the grace window — nil expiry rows stick around.
func LeaseValidUntil(cltt, validLft *int64) *time.Time {
	if cltt == nil || validLft == nil {
		return nil
	}
	if *cltt < 0 || *validLft < 0 {
		return nil
	}
	// time.Unix accepts huge seconds without overflow up to year
	// 292277026596 — Kea returns 32-bit-ish values here so overflow
	// is not a realistic concern.
	t := time.Unix(*cltt, 0).UTC().Add(time.Duration(*validLft) * time.Second)
	return &t
}

// ParseLease maps one raw Kea lease dict (an entry from
// `arguments.leases` in lease4-get-all / lease6-get-all) to a
// ParsedLease. Returns nil for leases to skip:
//
//   - missing `ip-address` (malformed entry)
//   - state ∈ {declined, expired-reclaimed} (no active binding)
//
// Hostname normalization: Python returns the hostname verbatim
// (services/kea.py:80-82 only collapses falsy values to None);
// Go additionally TrimSpace's so whitespace-only hostnames Kea
// sometimes ships for "client didn't send one" collapse to empty
// string. This is a DELIBERATE Go-side cleanup, not Python parity.
// The orchestrator (PR 15) must convert empty hostname → SQL NULL
// when writing IPAddress.dns_name; Python's None vs "" distinction
// disappears at that boundary.
func ParseLease(raw map[string]any) *ParsedLease {
	addr := stringField(raw, "ip-address")
	if addr == "" {
		return nil
	}
	state := intField(raw, "state")
	if state == leaseStateDeclined || state == leaseStateExpiredReclaimed {
		return nil
	}
	mac := stringField(raw, "hw-address")
	if mac == "" {
		// v6 leases carry duid instead — services/kea.py:79 falls
		// back to duid for the identifier slot.
		mac = stringField(raw, "duid")
	}
	hostname := strings.TrimSpace(stringField(raw, "hostname"))
	cltt := optInt64(raw, "cltt")
	validLft := optInt64(raw, "valid-lft")
	return &ParsedLease{
		Address:    strings.TrimSpace(addr),
		MAC:        mac,
		Hostname:   hostname,
		ValidUntil: LeaseValidUntil(cltt, validLft),
		State:      state,
	}
}

// ExtractLeases pulls the per-service `arguments.leases` lists out
// of Kea's response envelope. Kea returns a list of per-service
// dicts; success codes are 0 (ok) and 3 (empty — no leases).
// Other codes mean the service refused the command (1=error,
// 2=unsupported) and contribute zero leases to the output.
//
// Returns an empty slice on bad shape rather than an error — the
// sync orchestrator treats "no leases" the same as "bad response"
// at this layer (it still records last_sync_status="ok" because the
// HTTP call succeeded; per-server lease counts surface the gap).
func ExtractLeases(raw []byte) []map[string]any {
	var envelope []map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	var out []map[string]any
	for _, entry := range envelope {
		// Mirror Python at services/kea.py:213-218: result codes 0 + 3
		// pass; anything else gets dropped.
		if code := intField(entry, "result"); code != 0 && code != 3 {
			continue
		}
		args, ok := entry["arguments"].(map[string]any)
		if !ok {
			continue
		}
		list, ok := args["leases"].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			m, ok := item.(map[string]any)
			if ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// stringField fetches a string from a Kea response map. Wrong-type
// values (Kea sends ints for state, floats for some timing fields)
// resolve to "" so the caller doesn't have to switch on type.
func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// intField fetches an int from a Kea response map. JSON numbers
// decode as float64 by default — convert to int when the value is
// integral; non-numbers return 0.
func intField(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int(f)
}

// optInt64 fetches an optional int64 from a Kea response map. Used
// for cltt + valid-lft which can be present or omitted depending on
// whether the lease has ever been issued.
func optInt64(m map[string]any, key string) *int64 {
	v, ok := m[key]
	if !ok {
		return nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	n := int64(f)
	return &n
}
