// Local audit shim so the auth package can write audit rows without
// importing internal/audit (which would cycle: audit → auth → audit).
// AuditRecorder is intentionally the same shape as audit.Recorder so
// *dbq.Queries satisfies both implicitly.
package auth

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type AuditRecorder interface {
	InsertAuditLog(ctx context.Context, arg dbq.InsertAuditLogParams) error
}

// auditAuth records an audit row scoped to this package. action +
// targetID describe what happened; subject is the principal we have
// in hand (often built mid-flow, e.g. login resolves it from the
// bcrypt check before the JWT exists). success defaults to true;
// failure path callers pass false.
//
// Failures from InsertAuditLog are intentionally swallowed — the
// mutation has already committed by the time we record. We don't
// have a per-handler logger here so swallowed errors land in
// slog.Default() via the dbq layer's own error path (if any). For
// the auth surface where the audit trail is load-bearing, prefer
// callers verifying their AuditRecorder is non-nil at startup.
func (h *Handler) auditAuth(
	ctx context.Context,
	action, targetID string,
	subject *uuid.UUID,
	subjectLabel string,
	success bool,
	metadata map[string]any,
) {
	if h.Audit == nil {
		return
	}
	params := dbq.InsertAuditLogParams{
		Action:  action,
		Success: success,
	}
	if targetID != "" {
		t := targetID
		params.TargetID = &t
		tt := "auth"
		params.TargetType = &tt
	}
	if subject != nil && *subject != uuid.Nil {
		s := *subject
		params.ActorUserID = &s
	}
	if subjectLabel != "" {
		l := subjectLabel
		params.ActorLabel = &l
	}
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			params.MetadataJson = b
		}
	}
	_ = h.Audit.InsertAuditLog(ctx, params)
}
