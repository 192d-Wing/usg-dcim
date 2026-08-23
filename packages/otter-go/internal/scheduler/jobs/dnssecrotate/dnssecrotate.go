// Package dnssecrotate is the Go port of Python's dns_rotate_zsks arq
// cron (worker.py:387, calling services.dns.auto_rotate_due_zsks).
// Once a day at 03:17 UTC it walks every signed zone whose
// zsk_rotation_days policy is set, checks the most-recently-active
// ZSK's age, and rotates if the age has crossed the policy threshold.
//
// Rotation calls dns.RotateZoneKey — the same service helper the
// operator-driven POST /dns/zones/{id}/rotate-key/zsk endpoint uses.
// That helper handles key generation, retirement of prior active
// keys, and the zone.updated_at bump so the SOA serial moves.
//
// Frozen zones are intentionally skipped (counted in "checked" but
// not rotated). This is a deliberate divergence from Python:
// services/dns.py:610 rotate_zone_key has no frozen check, only the
// HTTP endpoint enforces it, so Python's cron would rotate a frozen
// zone's ZSK. The Go behavior matches operator intent — a freeze
// pauses all background mutations on that zone, including key
// rollovers — and is consistent with the HTTP endpoint's 422 on
// frozen. If a freeze outlives zsk_rotation_days the zone overstays
// its policy threshold; this is acceptable because freezes are
// short-lived operator-initiated states.
//
// Per-zone failures (DB error inside RotateZoneKey, ListActive
// failure) are logged at WARN and the loop continues — one bad zone
// shouldn't block rotations on the rest of the fleet. Python's
// for-loop would abort on the first exception; we choose the more
// conservative shape here.
package dnssecrotate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dns"
)

const Name = "dns_rotate_zsks"

// Role we rotate. Python's cron is ZSK-only; KSKs roll on the
// operator-driven path (different parent-DS coordination flow).
const role = "zsk"

// Querier is the slim DB surface this job needs. Embeds
// dns.RotationQuerier because the per-zone rotation call goes
// through that helper.
type Querier interface {
	dns.RotationQuerier
	ListSignedZonesWithZskRotation(ctx context.Context) ([]dbq.DnsZone, error)
}

type Job struct {
	Q   Querier
	Log *slog.Logger
	// Now exists for tests; production leaves it nil → time.Now.
	Now func() time.Time
}

func (j *Job) Name() string { return Name }

// Run walks every signed zone with zsk_rotation_days > 0, checks the
// active ZSK age, and rotates the ones that are due. Returns
// {checked, rotated}.
//
// Per-zone failures are logged and counted but don't abort the loop —
// a single misconfigured zone (e.g. a signed zone with no active ZSK,
// which Python skips silently) shouldn't block rotations on the rest
// of the fleet. The Python cron has the same shape: it skips zones
// with no active key and falls through on per-zone exceptions only
// by virtue of the loop not catching them; we choose the more
// conservative "log and continue" form here.
func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dnssecrotate: Querier is nil")
	}
	logger := j.Log
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	zones, err := j.Q.ListSignedZonesWithZskRotation(ctx)
	if err != nil {
		return nil, fmt.Errorf("list signed zones: %w", err)
	}
	// Hoist the reference time to a single sample taken before the
	// loop — every zone in this tick gets compared against the same
	// instant. Matches Python's `now = datetime.now(UTC)` at
	// services/dns.py:740. Otherwise a slow loop on a large fleet
	// could see a zone at threshold-0.5s skip on iteration 1 and
	// rotate on iteration 500.
	nowT := now().UTC()
	rotated := 0
	for _, z := range zones {
		if z.Frozen {
			continue
		}
		active, err := j.Q.ListActiveDnsKeysForZoneAndRole(ctx, dbq.ListActiveDnsKeysForZoneAndRoleParams{ZoneID: z.ID, Role: role})
		if err != nil {
			logger.Warn("dns_rotate_zsks_list_keys_failed",
				"zone_id", z.ID, "zone", z.Name, "err", err)
			continue
		}
		if len(active) == 0 {
			// Signed zone without an active ZSK — operator manually
			// retired everything; rotating here would mask a
			// misconfiguration. Matches Python's `if active_zsk is None: continue`.
			continue
		}
		ageDays := nowT.Sub(active[0].ActiveFrom).Hours() / 24
		if ageDays < float64(z.ZskRotationDays) {
			continue
		}
		if _, err := dns.RotateZoneKey(ctx, j.Q, z, role); err != nil {
			logger.Warn("dns_rotate_zsks_rotation_failed",
				"zone_id", z.ID, "zone", z.Name, "err", err)
			continue
		}
		rotated++
		logger.Info("dns_rotate_zsks_rotated",
			"zone_id", z.ID, "zone", z.Name, "age_days", ageDays)
	}
	return map[string]any{
		"checked": len(zones),
		"rotated": rotated,
	}, nil
}

// Compile-time check: *dbq.Queries satisfies our Querier interface.
// Without this, a future sqlc regen could silently drop one of the
// methods and the cron binary would only fail at link time, far from
// the change site.
var _ Querier = (*dbq.Queries)(nil)
