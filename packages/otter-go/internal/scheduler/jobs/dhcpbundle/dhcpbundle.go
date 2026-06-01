// Package dhcpbundle is the Go port of Python's rerender_dhcp_bundle
// arq function (worker.py:84). The Python original is event-driven —
// every DhcpScope create/update/delete enqueues a per-server task via
// pool.enqueue_job("rerender_dhcp_bundle", str(server_id)). The Go
// scheduler is cron-only (per the PR #208 scaffolding decision), so
// this port runs as a polling cron that walks every enabled DhcpServer
// once per tick: render, compare new etag vs cached etag, write the
// cache only when they differ.
//
// The polling design is correctness-equivalent to Python's event
// model — the HTTP bundle endpoint (PR #218) falls back to live render
// on a cache miss, so a freshly-rendered cache row is just an
// optimization. Polling adds bounded staleness (≤ cron interval)
// versus Python's mutation-bound freshness, but the staleness is
// hidden behind the live-render fallback: a request hitting a stale
// cache before the cron catches up still gets the correct bundle,
// just with the live-render latency cost.
//
// Per-server failures are LOGGED and the loop continues — one bad
// server (FK constraint hit during a concurrent delete, transient
// pgx error) shouldn't block re-renders on the rest of the fleet.
package dhcpbundle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/bundle"
)

const Name = "dhcp_bundle_rerender"

// Querier is the slim DB surface this job needs — `*dbq.Queries`
// satisfies it. ListEnabledDhcpServerIDs walks the fleet;
// GetDhcpServerBundleRow + ListDhcpScopesForBundle +
// ListDhcpScopeTemplatesByIDs are the read shape PR #218 already
// established; WriteDhcpBundleCache is the new sink.
type Querier interface {
	ListEnabledDhcpServerIDs(ctx context.Context) ([]uuid.UUID, error)
	GetDhcpServerBundleRow(ctx context.Context, id uuid.UUID) (dbq.DhcpServerBundleRow, error)
	ListDhcpScopesForBundle(ctx context.Context, dhcpServerID uuid.UUID) ([]dbq.DhcpScope, error)
	ListDhcpScopeTemplatesByIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.DhcpScopeTemplate, error)
	WriteDhcpBundleCache(ctx context.Context, arg dbq.WriteDhcpBundleCacheParams) error
}

type Job struct {
	Q   Querier
	Log *slog.Logger
}

func (j *Job) Name() string { return Name }

// Run walks every enabled DhcpServer, renders the bundle, and writes
// the cache if the etag changed. Returns {checked, rendered, written}
// for the scheduler harness's structured log.
//
// `checked` is the number of servers walked (sanity check against the
// fleet size); `rendered` is the count that produced a bundle without
// error; `written` is the count whose etag differed from the cached
// value (no-change ticks skip the UPDATE entirely so the cron is
// near-free on a stable fleet). The three counters together let the
// operator distinguish "fleet healthy + nothing changed" from "fleet
// broken" from "many concurrent changes" by scanning one log line.
func (j *Job) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("dhcpbundle: Querier is nil")
	}
	logger := j.Log
	if logger == nil {
		logger = slog.Default()
	}
	serverIDs, err := j.Q.ListEnabledDhcpServerIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled servers: %w", err)
	}
	checked, rendered, written := 0, 0, 0
	for _, id := range serverIDs {
		// Honor context cancellation between servers — without
		// this, a SIGTERM mid-tick walks every remaining ID
		// emitting a WARN line per server (each underlying pgx
		// call returns context.Canceled). Bails cleanly on
		// drain/rolling-restart. Returns the partial counts so
		// the scheduler harness's structured log still reflects
		// the work that did complete.
		if err := ctx.Err(); err != nil {
			return map[string]any{
				"checked": checked, "rendered": rendered, "written": written,
			}, nil
		}
		checked++
		// rerenderOne reports didRender/didWrite as flags, NOT as
		// counters that depend on err == nil. A server can render
		// successfully and fail on the cache UPDATE; the render
		// still counts as rendered. The log captures the failure
		// reason regardless.
		didRender, didWrite, err := j.rerenderOne(ctx, id)
		if didRender {
			rendered++
		}
		if didWrite {
			written++
		}
		if err != nil {
			logger.Warn("dhcp_bundle_rerender_server_failed",
				"server_id", id, "err", err)
		}
	}
	return map[string]any{
		"checked":  checked,
		"rendered": rendered,
		"written":  written,
	}, nil
}

// rerenderOne loads + renders a single server, writing the cache
// only when the etag actually changes. Split out of Run so each
// step has a single responsibility and the per-server failure path
// is easy to follow (one error return = log + continue).
func (j *Job) rerenderOne(ctx context.Context, id uuid.UUID) (didRender, didWrite bool, err error) {
	srv, err := j.Q.GetDhcpServerBundleRow(ctx, id)
	if err != nil {
		return false, false, fmt.Errorf("load server: %w", err)
	}
	b, err := j.renderForServer(ctx, srv)
	if err != nil {
		return false, false, err
	}
	didRender = true
	// Etag-unchanged short-circuit skips the UPDATE so the cron
	// doesn't churn the DB on a stable fleet. The JSON-non-empty
	// guard is critical: a pre-PR row with bundle_cache_etag set
	// but bundle_cache_json NULL (botched bootstrap, manual SQL,
	// downgrade/re-up sequence) would otherwise match the etag
	// every tick and the cron would never repopulate the JSON.
	// The HTTP handler's cache guard checks both columns too
	// (ipam/dhcp_bundle.go: `BundleCacheEtag != nil && *etag != ""
	// && len(BundleCacheJSON) > 0`), so without this matching
	// check here the request keeps live-rendering forever and the
	// cron's "warm the cache" reason for existing is silently lost.
	if srv.BundleCacheEtag != nil && *srv.BundleCacheEtag == b.Etag && len(srv.BundleCacheJSON) > 0 {
		return didRender, false, nil
	}
	encoded, err := bundle.EncodeForCache(b)
	if err != nil {
		return didRender, false, fmt.Errorf("encode bundle for cache: %w", err)
	}
	if err := j.Q.WriteDhcpBundleCache(ctx, dbq.WriteDhcpBundleCacheParams{
		ID:              srv.ID,
		BundleCacheEtag: b.Etag,
		BundleCacheJSON: encoded,
	}); err != nil {
		return didRender, false, fmt.Errorf("write cache: %w", err)
	}
	return didRender, true, nil
}

// renderForServer loads the scope + template rows + runs the renderer.
// Mirrors the shape PR #218's liveRenderBundle uses on the HTTP path.
func (j *Job) renderForServer(ctx context.Context, srv dbq.DhcpServerBundleRow) (bundle.KeaBundle, error) {
	scopes, err := j.Q.ListDhcpScopesForBundle(ctx, srv.ID)
	if err != nil {
		return bundle.KeaBundle{}, fmt.Errorf("list scopes: %w", err)
	}
	tmplIDs := collectTemplateIDs(scopes)
	templatesByID := map[string]bundle.Template{}
	if len(tmplIDs) > 0 {
		rows, err := j.Q.ListDhcpScopeTemplatesByIDs(ctx, tmplIDs)
		if err != nil {
			return bundle.KeaBundle{}, fmt.Errorf("list templates: %w", err)
		}
		for _, t := range rows {
			templatesByID[t.ID.String()] = bundle.FromDbqTemplate(t)
		}
	}
	mapped := make([]bundle.Scope, 0, len(scopes))
	for _, s := range scopes {
		mapped = append(mapped, bundle.FromDbqScope(s))
	}
	return bundle.RenderKeaBundle(bundle.FromDbqServer(srv), mapped, templatesByID)
}

// collectTemplateIDs dedupes the template_id pointer across the scope
// set. Same logic as ipam.collectTemplateIDs (PR #218); duplicated
// here to keep the dhcpbundle package self-contained without taking
// an import dependency on internal/ipam.
func collectTemplateIDs(scopes []dbq.DhcpScope) []uuid.UUID {
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

// Compile-time check: *dbq.Queries satisfies our Querier interface so
// a future sqlc regen that drops one of these methods fails at the
// change site instead of at link time. Same pattern as PR #213's
// dnssecrotate job.
var _ Querier = (*dbq.Queries)(nil)
