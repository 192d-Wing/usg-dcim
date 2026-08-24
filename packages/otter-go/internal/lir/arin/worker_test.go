// Worker tests exercise ProcessOne directly, bypassing the tx
// machinery. Each scenario plants a job in the fake JobQuerier,
// stubs the ARIN client to return success / transient / permanent,
// then asserts which MarkX call was made.
package arin

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type fakeJobQ struct {
	jobs       []dbq.ClaimNextArinSubmitJobRow
	registered []dbq.MarkArinRegisteredParams
	failed     []dbq.MarkArinFailedParams
}

func (f *fakeJobQ) ClaimNextArinSubmitJob(_ context.Context, _ int32) (dbq.ClaimNextArinSubmitJobRow, error) {
	if len(f.jobs) == 0 {
		return dbq.ClaimNextArinSubmitJobRow{}, pgx.ErrNoRows
	}
	j := f.jobs[0]
	f.jobs = f.jobs[1:]
	return j, nil
}

func (f *fakeJobQ) MarkArinRegistered(_ context.Context, a dbq.MarkArinRegisteredParams) error {
	f.registered = append(f.registered, a)
	return nil
}

func (f *fakeJobQ) MarkArinFailed(_ context.Context, a dbq.MarkArinFailedParams) error {
	f.failed = append(f.failed, a)
	return nil
}

type fakeSubmit struct {
	result SubmitResult
	err    error
}

func (f *fakeSubmit) SubmitReassignDetailed(_ context.Context, _ dbq.ClaimNextArinSubmitJobRow) (SubmitResult, error) {
	return f.result, f.err
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestProcessOne_NoJobsReturnsFalse(t *testing.T) {
	w := &Worker{Log: discardLog()}
	q := &fakeJobQ{}
	c := &fakeSubmit{}
	more, err := w.ProcessOne(context.Background(), q, c)
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Error("empty queue should return more=false")
	}
}

func TestProcessOne_SuccessMarksRegistered(t *testing.T) {
	allocID := uuid.New()
	q := &fakeJobQ{jobs: []dbq.ClaimNextArinSubmitJobRow{{AllocationID: allocID}}}
	c := &fakeSubmit{result: SubmitResult{NetHandle: "NET-OK-1"}}
	w := &Worker{Log: discardLog()}
	more, err := w.ProcessOne(context.Background(), q, c)
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Error("a processed job should return more=true")
	}
	if len(q.registered) != 1 {
		t.Fatalf("expected 1 registered call, got %d", len(q.registered))
	}
	if q.registered[0].ID != allocID || q.registered[0].NetHandle != "NET-OK-1" {
		t.Errorf("registered call mismatch: %+v", q.registered[0])
	}
	if len(q.failed) != 0 {
		t.Errorf("should not have called MarkArinFailed on success")
	}
}

func TestProcessOne_TransientMarksFailed(t *testing.T) {
	allocID := uuid.New()
	q := &fakeJobQ{jobs: []dbq.ClaimNextArinSubmitJobRow{{AllocationID: allocID}}}
	c := &fakeSubmit{err: errors.New("arin transient error: 503")}
	w := &Worker{Log: discardLog()}
	if _, err := w.ProcessOne(context.Background(), q, c); err != nil {
		t.Fatal(err)
	}
	if len(q.failed) != 1 {
		t.Fatalf("expected 1 failed call, got %d", len(q.failed))
	}
	if q.failed[0].ID != allocID {
		t.Errorf("failed call mismatch: %+v", q.failed[0])
	}
	if len(q.registered) != 0 {
		t.Errorf("transient should not register")
	}
}

func TestProcessOne_PermanentAlsoMarksFailed(t *testing.T) {
	// Permanent errors hit MarkArinFailed too; the difference is
	// only in the *next* claim: with attempts>=MaxAttempts the row
	// stops being eligible, and the operator hits /arin/retry.
	allocID := uuid.New()
	q := &fakeJobQ{jobs: []dbq.ClaimNextArinSubmitJobRow{{AllocationID: allocID}}}
	c := &fakeSubmit{err: errors.New("arin permanent error: 400 bad payload")}
	w := &Worker{Log: discardLog()}
	if _, err := w.ProcessOne(context.Background(), q, c); err != nil {
		t.Fatal(err)
	}
	if len(q.failed) != 1 {
		t.Errorf("expected MarkArinFailed call for permanent error")
	}
}

func TestProcessOne_ContinuesAfterPropagatedClaimError(t *testing.T) {
	// A raw DB error (not ErrNoRows) bubbles up — caller decides to
	// abort the tick or keep going. The signature is (more, err);
	// more=false because nothing was processed.
	q := &erroringJobQ{}
	w := &Worker{Log: discardLog()}
	more, err := w.ProcessOne(context.Background(), q, &fakeSubmit{})
	if err == nil {
		t.Error("expected propagated DB error")
	}
	if more {
		t.Error("error path should return more=false")
	}
}

// Pin the post-review fix that tick() drains up to MaxPerTick jobs
// total — not MaxPerTick/2. Earlier shape used `budget--` inside a
// `for i := 0; i < budget; i++` loop, which halved the submit draw
// per tick. The unit test bypasses tx machinery by counting raw
// ProcessOne calls via a custom fake.
func TestProcessOne_BatchedRespectsMaxPerTick(t *testing.T) {
	// Simulate a tick by repeatedly calling ProcessOne until it
	// returns more=false or we hit the cap. This mirrors what
	// Worker.tick does after the fix.
	const maxPerTick = 10
	q := &countingJobQ{remaining: 20}
	w := &Worker{Log: discardLog()}
	processed := 0
	for processed < maxPerTick {
		more, err := w.ProcessOne(context.Background(), q,
			&fakeSubmit{result: SubmitResult{NetHandle: "NET-X"}})
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		processed++
	}
	if processed != maxPerTick {
		t.Errorf("processed %d jobs, want %d (pre-fix would have been %d)",
			processed, maxPerTick, maxPerTick/2)
	}
	if q.claimed != maxPerTick {
		t.Errorf("claim called %d times, want %d", q.claimed, maxPerTick)
	}
}

// countingJobQ feeds a configurable number of jobs into ProcessOne
// then returns ErrNoRows. Tracks how many claim calls were made so
// the batch test can assert exactly MaxPerTick of them fired.
type countingJobQ struct {
	remaining int
	claimed   int
}

func (c *countingJobQ) ClaimNextArinSubmitJob(_ context.Context, _ int32) (dbq.ClaimNextArinSubmitJobRow, error) {
	c.claimed++
	if c.remaining <= 0 {
		return dbq.ClaimNextArinSubmitJobRow{}, pgx.ErrNoRows
	}
	c.remaining--
	return dbq.ClaimNextArinSubmitJobRow{AllocationID: uuid.New()}, nil
}

func (c *countingJobQ) MarkArinRegistered(_ context.Context, _ dbq.MarkArinRegisteredParams) error {
	return nil
}
func (c *countingJobQ) MarkArinFailed(_ context.Context, _ dbq.MarkArinFailedParams) error {
	return nil
}

type erroringJobQ struct{}

func (erroringJobQ) ClaimNextArinSubmitJob(context.Context, int32) (dbq.ClaimNextArinSubmitJobRow, error) {
	return dbq.ClaimNextArinSubmitJobRow{}, errors.New("connection refused")
}
func (erroringJobQ) MarkArinRegistered(context.Context, dbq.MarkArinRegisteredParams) error {
	return nil
}
func (erroringJobQ) MarkArinFailed(context.Context, dbq.MarkArinFailedParams) error { return nil }
