// POST /api/v1/region-deployments/{id}/start. Ports Python
// regiondeploy.py L207-244 — same status gate (pending/failed/
// aborted), same write semantics (status=preflight + clear
// last_error), same on-success enqueue of a `run_region_deploy` arq
// job. Differences from the Python flow are intentional: we tighten
// per-site ABAC on top of CAP_START (Python only checks the cap) and
// we record a `region_deployment.start` audit row (Python's start
// never recorded one).
package regiondeploy

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// arq queue name + function name — must match Python's
// WorkerSettings.functions registration (`run_region_deploy` lives at
// dcim.regiondeploy.orchestrator). The default queue name `arq:queue`
// is arq's `default_queue_name` constant.
const (
	arqDefaultQueueName = "arq:queue"
	arqStartFunction    = "run_region_deploy"
)

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, badIDMsg)
		return
	}
	// Existence + scope check before the conditional UPDATE — an
	// out-of-scope principal must not be able to flip status on a
	// deployment outside their site grants, even by accident.
	pre, err := h.Q.GetRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, pre.SiteID, capStart); serr != nil {
		writeMapped(w, serr)
		return
	}
	// Fail-closed when no arq enqueuer is wired. The handler will
	// flip the DB row to `preflight` but without the worker pickup
	// the orchestrator never runs — better to refuse with 503.
	if h.Arq == nil {
		httpx.Error(w, http.StatusServiceUnavailable,
			"arq enqueuer is not configured")
		return
	}
	res, err := h.Q.StartRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	if res.Updated == 0 {
		// Status was not in the startable set (e.g. provisioning,
		// joining, ready). Match Python's error wording so finch's
		// existing UI surfacing stays consistent through the cutover.
		httpx.Error(w, http.StatusUnprocessableEntity,
			"deployment is "+res.PriorStatus+
				"; abort first to restart")
		return
	}
	// Enqueue the orchestrator job. Failure here is best-effort
	// rollback: the DB write committed, so we record an error event
	// row that the SSE stream surfaces so operators see the failure.
	queue := h.ArqQueueName
	if queue == "" {
		queue = arqDefaultQueueName
	}
	jobID, enqueueErr := h.Arq.EnqueueArqJob(r.Context(), queue, arqStartFunction,
		[]any{id.String()})
	if enqueueErr != nil {
		// Status is now `preflight` but no job was pushed. Record
		// the failure so the deploy doesn't hang; operators see it
		// in /events and can re-press Start.
		writeStartEnqueueErrEvent(r.Context(), h.Q, id, enqueueErr)
		writeMapped(w, enqueueErr)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "region_deployment.start", TargetType: "region_deployment",
		TargetID: id.String(), SiteID: &pre.SiteID,
		Metadata: map[string]any{"arq_job_id": jobID},
	})
	out, err := h.reloadDetail(r.Context(), id)
	if err != nil {
		writeMapped(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// writeStartEnqueueErrEvent appends an error event row when the arq
// enqueue fails after the DB flip succeeded. Best-effort; if the
// event write itself fails we silently drop — the audit log won't
// have the failure trail but the operator can re-press Start.
func writeStartEnqueueErrEvent(ctx context.Context, q Querier, id uuid.UUID, enqueueErr error) {
	_, _ = q.CreateRegionDeploymentEvent(ctx, dbq.CreateRegionDeploymentEventParams{
		DeploymentID: id, Stage: "preflight", Level: "error",
		Message: "arq enqueue failed: " + enqueueErr.Error() +
			". Status is `preflight` but no orchestrator job ran; " +
			"press Start again to retry.",
		Payload: []byte("{}"),
	})
}
