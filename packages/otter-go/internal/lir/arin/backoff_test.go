// Backoff schedule tests. Pinning the exact intervals keeps the SQL
// CASE expression in lir_arin.sql and the Go BackoffAfterAttempt
// function in sync — a refactor of one without the other shows up
// here as a failed pin.
package arin

import (
	"testing"
	"time"
)

func TestBackoff_FirstFailureWaits1Minute(t *testing.T) {
	if got := BackoffAfterAttempt(1); got != time.Minute {
		t.Errorf("got %v want 1m", got)
	}
}

func TestBackoff_FullSchedule(t *testing.T) {
	for _, c := range []struct {
		attempts int32
		want     time.Duration
	}{
		{1, 1 * time.Minute},
		{2, 5 * time.Minute},
		{3, 30 * time.Minute},
		{4, 2 * time.Hour},
	} {
		if got := BackoffAfterAttempt(c.attempts); got != c.want {
			t.Errorf("attempts=%d: got %v want %v", c.attempts, got, c.want)
		}
	}
}

func TestBackoff_BeyondCapReturnsZero(t *testing.T) {
	for _, n := range []int32{5, 6, 100} {
		if got := BackoffAfterAttempt(n); got != 0 {
			t.Errorf("attempts=%d should be 0, got %v", n, got)
		}
	}
}

func TestShouldRetry_BoundaryAt5(t *testing.T) {
	if !ShouldRetry(4) {
		t.Error("attempts=4 should still retry")
	}
	if ShouldRetry(5) {
		t.Error("attempts=5 (= MaxAttempts) should NOT retry")
	}
	if ShouldRetry(6) {
		t.Error("attempts=6 should NOT retry")
	}
}

func TestMaxAttempts_Is5(t *testing.T) {
	if MaxAttempts != 5 {
		t.Errorf("MaxAttempts changed: %d — update lir_arin.sql CASE and docs", MaxAttempts)
	}
}
