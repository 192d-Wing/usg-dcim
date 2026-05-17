// audit.Record writes an append-only row to audit_logs from inside a
// mutation handler. Mirrors Python security/audit.py::record. Failures
// are logged but NOT returned to the caller — an audit-write failure
// must never break the mutation that triggered it, since the mutation
// already succeeded by the time Record is called. The structured log
// line is the fallback ledger.
//
// Usage in a handler:
//
//	audit.Record(r.Context(), h.Audit, audit.Event{
//	    Action: "site.create", TargetType: "site",
//	    TargetID: out.ID.String(), SiteID: &out.ID,
//	})
//
// Adopt Recorder as a Handler field (not a method) so packages that
// already pass a Querier in stay unchanged.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// Recorder is the slice of sqlc methods Record needs. *dbq.Queries
// satisfies it; tests substitute an in-memory fake.
type Recorder interface {
	InsertAuditLog(ctx context.Context, arg dbq.InsertAuditLogParams) error
}

// Event is what a handler hands Record. Action is required; everything
// else is optional. Diff + Metadata are JSON-encodable maps (typically
// the PATCH payload diff and any contextual extras like dropped-power
// counts on a decommission).
type Event struct {
	Action     string
	TargetType string
	TargetID   string
	SiteID     *uuid.UUID
	Success    bool // defaults true if Failure is false
	Failure    bool // explicit false-on-success path
	Diff       map[string]any
	Metadata   map[string]any
	RequestID  string
}

// Record writes one audit row. The Principal is pulled from ctx via
// auth.From; if no principal is present the row records actor_label
// as "anonymous" (which should never happen for an authenticated
// mutation but is safer than dropping the row).
func Record(ctx context.Context, q Recorder, log *slog.Logger, ev Event) {
	if q == nil {
		return
	}
	params := dbq.InsertAuditLogParams{
		Action:    ev.Action,
		TargetID:  optString(ev.TargetID),
		SiteID:    ev.SiteID,
		RequestID: optString(ev.RequestID),
		Success:   !ev.Failure,
	}
	if ev.TargetType != "" {
		params.TargetType = &ev.TargetType
	}
	if p, ok := auth.From(ctx); ok {
		label := p.Label
		if label == "" {
			label = "user:" + p.Subject.String()
		}
		params.ActorLabel = &label
		// PR 46 records the label only; user_id / token_id split lands
		// when the Principal carries a distinguishing flag. Today
		// label="user:..." vs "token:..." prefix is the disambiguator
		// callers can grep on.
		if p.Subject != uuid.Nil {
			sub := p.Subject
			params.ActorUserID = &sub
		}
	} else {
		anon := "anonymous"
		params.ActorLabel = &anon
	}
	if ev.Diff != nil {
		if b, err := json.Marshal(ev.Diff); err == nil {
			params.DiffJson = b
		}
	}
	if ev.Metadata != nil {
		if b, err := json.Marshal(ev.Metadata); err == nil {
			params.MetadataJson = b
		}
	}
	if err := q.InsertAuditLog(ctx, params); err != nil && log != nil {
		// Loud-fail to logs; never return to caller. The mutation has
		// already committed.
		log.Error("audit_write_failed",
			"action", ev.Action,
			"target_type", ev.TargetType,
			"target_id", ev.TargetID,
			"err", err.Error())
	}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
