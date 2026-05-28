// Tests for the deassignment direction: RemoveReassignment client
// guards, classifyRemoveResponse status-code mapping, and the
// worker's ProcessOneRemove dispatch.
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

// ---- RemoveReassignment guards ----

func TestRemove_DisabledIsPermanent(t *testing.T) {
	c := NewClient(Config{Enabled: false, APIKey: "k"}, nil)
	err := c.RemoveReassignment(t.Context(), "NET-PARENT", "NET-CHILD")
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("got %v", err)
	}
}

func TestRemove_MissingAPIKeyIsPermanent(t *testing.T) {
	c := NewClient(Config{Enabled: true, APIKey: ""}, nil)
	err := c.RemoveReassignment(t.Context(), "NET-PARENT", "NET-CHILD")
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("got %v", err)
	}
}

func TestRemove_EmptyNetHandleIsPermanent(t *testing.T) {
	// A row in arin_status='removing' that lost its handle is a data
	// bug — surface as permanent so the operator sees it.
	c := NewClient(Config{Enabled: true, APIKey: "k"}, nil)
	err := c.RemoveReassignment(t.Context(), "NET-PARENT", "")
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("got %v", err)
	}
}

// ---- classifyRemoveResponse ----

func TestRemoveClassify_2xxIsNil(t *testing.T) {
	if err := classifyRemoveResponse(200, []byte("")); err != nil {
		t.Errorf("got %v", err)
	}
}

func TestRemoveClassify_5xxIsTransient(t *testing.T) {
	err := classifyRemoveResponse(503, []byte("upstream down"))
	if !errors.Is(err, ErrTransient) {
		t.Errorf("got %v", err)
	}
}

func TestRemoveClassify_4xxIsPermanent(t *testing.T) {
	err := classifyRemoveResponse(404, []byte("handle not found"))
	if !errors.Is(err, ErrPermanent) {
		t.Errorf("got %v", err)
	}
}

// ---- ProcessOneRemove ----

type fakeRemoveJobQ struct {
	jobs     []dbq.ArinRemoveJobRow
	removed  []uuid.UUID
	failed   []dbq.MarkArinFailedParams
}

func (f *fakeRemoveJobQ) ClaimNextArinRemoveJob(_ context.Context, _ int32) (dbq.ArinRemoveJobRow, error) {
	if len(f.jobs) == 0 {
		return dbq.ArinRemoveJobRow{}, pgx.ErrNoRows
	}
	j := f.jobs[0]
	f.jobs = f.jobs[1:]
	return j, nil
}

func (f *fakeRemoveJobQ) MarkArinRemoved(_ context.Context, id uuid.UUID) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeRemoveJobQ) MarkArinFailed(_ context.Context, a dbq.MarkArinFailedParams) error {
	f.failed = append(f.failed, a)
	return nil
}

type fakeRemoveClient struct {
	err error
}

func (f *fakeRemoveClient) RemoveReassignment(_ context.Context, _, _ string) error {
	return f.err
}

func discardLogRemove() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestProcessOneRemove_NoJobsReturnsFalse(t *testing.T) {
	w := &Worker{Log: discardLogRemove()}
	more, err := w.ProcessOneRemove(context.Background(), &fakeRemoveJobQ{}, &fakeRemoveClient{})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Error("empty queue should return more=false")
	}
}

func TestProcessOneRemove_SuccessMarksRemoved(t *testing.T) {
	allocID := uuid.New()
	q := &fakeRemoveJobQ{jobs: []dbq.ArinRemoveJobRow{{
		AllocationID: allocID, NetHandle: "NET-OK-1", ParentNetHandle: "NET-PARENT-1",
	}}}
	w := &Worker{Log: discardLogRemove()}
	more, err := w.ProcessOneRemove(context.Background(), q, &fakeRemoveClient{})
	if err != nil {
		t.Fatal(err)
	}
	if !more {
		t.Error("processed job should return more=true")
	}
	if len(q.removed) != 1 || q.removed[0] != allocID {
		t.Errorf("expected MarkArinRemoved on %s, got %+v", allocID, q.removed)
	}
	if len(q.failed) != 0 {
		t.Error("success path should not mark failed")
	}
}

func TestProcessOneRemove_TransientMarksFailed(t *testing.T) {
	allocID := uuid.New()
	q := &fakeRemoveJobQ{jobs: []dbq.ArinRemoveJobRow{{AllocationID: allocID, NetHandle: "NET-x"}}}
	w := &Worker{Log: discardLogRemove()}
	if _, err := w.ProcessOneRemove(context.Background(), q,
		&fakeRemoveClient{err: errors.New("arin transient: 503")}); err != nil {
		t.Fatal(err)
	}
	if len(q.failed) != 1 || q.failed[0].ID != allocID {
		t.Errorf("expected MarkArinFailed on %s, got %+v", allocID, q.failed)
	}
	if len(q.removed) != 0 {
		t.Error("transient should not mark removed")
	}
}
