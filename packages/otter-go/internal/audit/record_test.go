package audit

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type recRecorder struct {
	gotParams dbq.InsertAuditLogParams
	gotCalls  int
	err       error
}

func (r *recRecorder) InsertAuditLog(_ context.Context, arg dbq.InsertAuditLogParams) error {
	r.gotCalls++
	r.gotParams = arg
	return r.err
}

func nopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRecord_NoPrincipal_Anonymous(t *testing.T) {
	r := &recRecorder{}
	Record(context.Background(), r, nopLogger(), Event{Action: "site.create"})
	if r.gotCalls != 1 {
		t.Fatalf("calls: %d", r.gotCalls)
	}
	if r.gotParams.ActorLabel == nil || *r.gotParams.ActorLabel != "anonymous" {
		t.Errorf("anon label: %+v", r.gotParams.ActorLabel)
	}
}

func TestRecord_WithUserPrincipal(t *testing.T) {
	uid := uuid.New()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Subject: uid, Label: "user:" + uid.String(),
	})
	r := &recRecorder{}
	Record(ctx, r, nopLogger(), Event{
		Action: "site.update", TargetType: "site", TargetID: "abc",
		Diff: map[string]any{"name": "newname"},
	})
	if r.gotParams.ActorUserID == nil || *r.gotParams.ActorUserID != uid {
		t.Errorf("actor_user_id: %v", r.gotParams.ActorUserID)
	}
	if r.gotParams.ActorLabel == nil || *r.gotParams.ActorLabel != "user:"+uid.String() {
		t.Errorf("label: %+v", r.gotParams.ActorLabel)
	}
	if r.gotParams.TargetType == nil || *r.gotParams.TargetType != "site" {
		t.Errorf("target_type: %+v", r.gotParams.TargetType)
	}
	if r.gotParams.TargetID == nil || *r.gotParams.TargetID != "abc" {
		t.Errorf("target_id: %+v", r.gotParams.TargetID)
	}
	if !r.gotParams.Success {
		t.Error("expected success=true by default")
	}
	var diff map[string]any
	_ = json.Unmarshal(r.gotParams.DiffJson, &diff)
	if diff["name"] != "newname" {
		t.Errorf("diff: %+v", diff)
	}
}

func TestRecord_FailureFlag(t *testing.T) {
	r := &recRecorder{}
	Record(context.Background(), r, nopLogger(), Event{Action: "x", Failure: true})
	if r.gotParams.Success {
		t.Error("expected success=false when Failure=true")
	}
}

func TestRecord_NilRecorder_NoPanic(t *testing.T) {
	// Handlers in dev mode may opt out; nil recorder should be a no-op,
	// not a crash.
	Record(context.Background(), nil, nopLogger(), Event{Action: "noop"})
}

func TestRecord_BackendErrorSwallowed(t *testing.T) {
	// Audit-write failure must not surface to callers. We just verify
	// Record returns normally (it has no return value); the structured
	// log is the audit-trail fallback.
	r := &recRecorder{err: io.EOF}
	Record(context.Background(), r, nopLogger(), Event{Action: "x"})
}
