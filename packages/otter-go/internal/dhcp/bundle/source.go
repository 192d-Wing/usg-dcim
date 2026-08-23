// Source-side glue: the read-and-render dance both the HTTP bundle
// endpoint (internal/ipam) and the rerender cron
// (internal/scheduler/jobs/dhcpbundle) execute for a single server.
// Lifted into this package so the two callers don't carry near-
// duplicate copies (sonar flagged 14.6% duplication when they did).
//
// The Querier shape is the slim union of {scopes, templates} reads
// both call sites need; *dbq.Queries satisfies it. Source.go does
// NOT take the GetDhcpServerBundleRow call — callers load the row
// themselves so their error semantics (404 vs WARN-and-continue)
// stay distinct.
package bundle

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// SourceQuerier is the slim DB surface BuildForServer needs.
// *dbq.Queries (the real implementation produced by sqlc) satisfies
// it; callers compose it with their own larger interfaces.
type SourceQuerier interface {
	ListDhcpScopesForBundle(ctx context.Context, dhcpServerID uuid.UUID) ([]dbq.ListDhcpScopesForBundleRow, error)
	ListDhcpScopeTemplatesByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.DhcpScopeTemplate, error)
}

// BuildForServer loads the scope + template rows that belong to the
// already-loaded DhcpServerBundleRow, runs the FromDbq adapters, and
// returns the rendered KeaBundle. Errors are wrapped with the step
// that failed so the caller's log line points at the right SQL on
// debug.
func BuildForServer(ctx context.Context, q SourceQuerier, srv dbq.GetDhcpServerBundleRowRow) (KeaBundle, error) {
	scopes, err := q.ListDhcpScopesForBundle(ctx, srv.ID)
	if err != nil {
		return KeaBundle{}, fmt.Errorf("list scopes: %w", err)
	}
	tmplIDs := CollectTemplateIDs(scopes)
	templatesByID := map[string]Template{}
	if len(tmplIDs) > 0 {
		rows, err := q.ListDhcpScopeTemplatesByIDs(ctx, tmplIDs)
		if err != nil {
			return KeaBundle{}, fmt.Errorf("list templates: %w", err)
		}
		for _, t := range rows {
			templatesByID[t.ID.String()] = FromDbqTemplate(t)
		}
	}
	mapped := make([]Scope, 0, len(scopes))
	for _, s := range scopes {
		mapped = append(mapped, FromDbqScope(s))
	}
	return RenderKeaBundle(FromDbqServer(srv), mapped, templatesByID)
}

// CollectTemplateIDs dedupes the template_id pointer across the
// scope set. The downstream SQL uses ANY($1::uuid[]) so passing the
// same UUID twice doesn't fault, but keeping the slice tight avoids
// shipping an outsize parameter on a server with many scopes that
// all share one template.
func CollectTemplateIDs(scopes []dbq.ListDhcpScopesForBundleRow) []uuid.UUID {
	seen := map[uuid.UUID]struct{}{}
	out := make([]uuid.UUID, 0, len(scopes))
	for _, s := range scopes {
		if s.TemplateID == nil {
			continue
		}
		if _, ok := seen[*s.TemplateID]; ok {
			continue
		}
		seen[*s.TemplateID] = struct{}{}
		out = append(out, *s.TemplateID)
	}
	return out
}
