// Backoff schedule for ARIN Reg-RWS retries. The worker's claim
// query enforces the same interval table via a CASE expression, but
// keeping the Go side authoritative lets unit tests pin the cadence
// without spinning up Postgres.
//
// MaxAttempts = 5 total attempts before the row sits permanently in
// arin_status='failed' and only the manual retry endpoint
// (POST /lir/allocations/{id}/arin/retry) can reset it.
//
// Backoff intervals are *between* attempts:
//   after attempt 1 fails → wait 1m   before attempt 2
//   after attempt 2 fails → wait 5m   before attempt 3
//   after attempt 3 fails → wait 30m  before attempt 4
//   after attempt 4 fails → wait 2h   before attempt 5
//   after attempt 5 fails → permanent (no more auto-retries)
package arin

import "time"

// MaxAttempts caps how many times the worker will auto-retry a
// failing allocation. Wire this value into the SQL claim query's
// max_attempts parameter so the two stay in sync.
const MaxAttempts = 5

// BackoffAfterAttempt returns how long the worker must wait after
// a failure on attempt N before attempt N+1 is eligible. Returns
// 0 when attempts is at or beyond MaxAttempts (the row stays
// permanently failed; manual retry only).
func BackoffAfterAttempt(attempts int32) time.Duration {
	switch attempts {
	case 1:
		return 1 * time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 30 * time.Minute
	case 4:
		return 2 * time.Hour
	default:
		// attempts >= 5 → no more retries.
		return 0
	}
}

// ShouldRetry reports whether a row with `attempts` failures should
// be picked up again. False once attempts hit MaxAttempts.
func ShouldRetry(attempts int32) bool {
	return attempts < MaxAttempts
}
