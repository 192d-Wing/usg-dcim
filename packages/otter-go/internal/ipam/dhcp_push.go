// Go port of Python's per-scope DHCP push surface
// (api/ipam.py:2371/2415/2460). Three thin HTTP wrappers around the
// push/diff orchestrators in internal/dhcp/push and internal/dhcp/diff:
//
//   POST /api/v1/ipam/dhcp/scopes/{id}/push
//   GET  /api/v1/ipam/dhcp/scopes/{id}/diff
//   GET  /api/v1/ipam/dhcp/scopes/{id}/push-history?limit=N
//
// ABAC is enforced by resolving the scope's transitive fabric_id
// (GetDhcpScopeFabricID, 2-hop scope→server→fabric) and running it
// through EnforceFabricScope. Wire shapes are byte-identical to the
// Python responses so any client (the dhcp-site UI, scope.spec-driven
// tooling) cuts over without a contract change.
package ipam

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/diff"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/push"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const errDhcpScopeNotFound = "dhcp scope not found"

// pushDhcpScope mirrors api/ipam.py:2460. Loads the scope's fabric,
// enforces ipam:dhcp-scopes:push on it, then runs push.PushScope.
// Audit metadata matches Python (dhcp_server_id / kea_subnet_id /
// status / error) so post-cutover the two systems' audit streams
// are queryable as one.
func (h *Handler) pushDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpScopeFabric(w, r, id, "ipam:dhcp-scopes:push") {
		return
	}
	// PushScope loads the scope internally; pull the server id off
	// the result via a single extra fetch on the success path so the
	// audit metadata can record dhcp_server_id without a wasted
	// pre-call lookup on every request. The orchestrator returns
	// kea.StatusError + an error body for missing scopes, so a 404-
	// shaped Result keeps the wire shape consistent.
	result, err := push.PushScope(r.Context(), h.Q, h.pushKeaBuilder(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	serverID, err := h.dhcpServerIDForScope(r.Context(), id)
	if err != nil {
		// Scope raced (deleted between PushScope and now). The push
		// already wrote its history row + scope state, so we record
		// the audit row without the server_id rather than dropping
		// the audit event entirely — operators need to see the push
		// happened even when the post-call lookup raced.
		serverID = uuid.Nil
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dhcp_scope.push", TargetType: "dhcp_scope", TargetID: id.String(),
		Metadata: map[string]any{
			"dhcp_server_id": serverIDString(serverID),
			"kea_subnet_id":  result.KeaSubnetID,
			"status":         string(result.Status),
			"error":          nilIfEmpty(result.Error),
		},
	})
	httpx.JSON(w, http.StatusOK, newPushResultBody(result))
}

// serverIDString renders a uuid.UUID for the audit metadata payload,
// preserving Python's `metadata["dhcp_server_id"]` shape (a string)
// while keeping uuid.Nil → null so a race-deleted scope shows up as
// `"dhcp_server_id": null` rather than the misleading all-zeros UUID.
func serverIDString(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id.String()
}

// dhcpServerIDForScope is the narrow lookup used by the push handler's
// audit metadata path. Separate from the ABAC fabric lookup so read
// paths (diff, push-history) don't pay for a column they don't need.
func (h *Handler) dhcpServerIDForScope(ctx context.Context, scopeID uuid.UUID) (uuid.UUID, error) {
	scope, err := h.Q.GetDhcpScopeForPush(ctx, scopeID)
	if err != nil {
		return uuid.Nil, err
	}
	return scope.DhcpServerID, nil
}

// diffDhcpScope mirrors api/ipam.py:2371. Runs diff.DiffScope to
// produce the in_sync/drifted/missing_from_kea/never_pushed/error
// classification, then persists last_diff_* via PersistDiffState so a
// subsequent LIST or push-drifted sees the fresh state without
// re-pulling from Kea.
func (h *Handler) diffDhcpScope(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpScopeFabric(w, r, id, "ipam:dhcp-scopes:read") {
		return
	}
	result, err := diff.DiffScope(r.Context(), h.Q, h.diffKeaBuilder(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if perr := diff.PersistDiffState(r.Context(), h.Q, result); perr != nil {
		status, msg := httpx.Mapped(perr)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, newDiffResultBody(result))
}

// listDhcpScopePushHistory mirrors api/ipam.py:2415. Returns the
// most-recent N attempts (default 50, max 500) for one scope. The
// (scope_id, attempted_at DESC) index from migration 0064 makes the
// fetch index-only.
func (h *Handler) listDhcpScopePushHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	if !h.enforceDhcpScopeFabric(w, r, id, "ipam:dhcp-scopes:read") {
		return
	}
	limit := parseInt32(r.URL.Query().Get("limit"), 50, 1, 500)
	rows, err := h.Q.ListDhcpScopePushHistoryByScope(r.Context(),
		dbq.ListDhcpScopePushHistoryByScopeParams{ScopeID: id, Limit: limit})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	entries := make([]pushHistoryEntry, len(rows))
	for i, row := range rows {
		entries[i] = pushHistoryEntry{
			ID:          row.ID.String(),
			Operation:   row.Operation,
			KeaSubnetID: row.KeaSubnetID,
			Status:      row.Status,
			Error:       row.Error,
			DurationMS:  row.DurationMS,
			// Python's column is DateTime(timezone=True) and isoformat()
			// emits a mandatory 6-digit microseconds field + signed
			// offset (UTC → "+00:00"). The format reference below
			// mirrors that exactly: ".000000" forces all six digits
			// (the 9s form would drop trailing zeros), "-07:00"
			// produces "+00:00" for UTC (Z07:00 would print "Z"
			// instead — wrong for parity).
			AttemptedAt: row.AttemptedAt.UTC().Format("2006-01-02T15:04:05.000000-07:00"),
		}
	}
	httpx.JSON(w, http.StatusOK, pushHistoryBody{
		ScopeID: id.String(),
		Entries: entries,
	})
}

// enforceDhcpScopeFabric runs the 2-hop scope → server → fabric lookup
// (GetDhcpScopeFabricID) and the EnforceFabricScope check in one step.
// Returns false when a response has been written.
//
// The fabric lookup INNER-JOINs dhcp_servers so a missing scope OR a
// scope pointing at a deleted server both surface as 404 with the same
// "dhcp scope not found" body — collapsing the two cases matches
// Python's `db.get(DhcpScope, id) is None` posture (it never
// distinguished "scope gone" from "server gone").
//
// Per-path note: the push handler additionally calls
// dhcpServerIDForScope after PushScope to fill the audit metadata's
// dhcp_server_id field; diff and push-history don't need that lookup.
// Splitting the responsibilities here keeps the ABAC fast-path one
// narrow query for every endpoint.
func (h *Handler) enforceDhcpScopeFabric(w http.ResponseWriter, r *http.Request, scopeID uuid.UUID, capCode string) bool {
	fid, ok := h.lookupFabricID(w, r.Context(),
		func(ctx context.Context) (uuid.UUID, error) { return h.Q.GetDhcpScopeFabricID(ctx, scopeID) },
		errDhcpScopeNotFound)
	if !ok {
		return false
	}
	return h.enforceFabric(w, r, fid, capCode)
}

// pushResultBody mirrors Python's _push_result_dict at
// api/ipam.py:2496. Kept as a struct (rather than map[string]any) so
// the wire shape lives in code, not in a renderer at request time.
type pushResultBody struct {
	ScopeID     string  `json:"scope_id"`
	KeaSubnetID *int32  `json:"kea_subnet_id"`
	Status      string  `json:"status"`
	Error       *string `json:"error"`
}

func newPushResultBody(r push.Result) pushResultBody {
	return pushResultBody{
		ScopeID:     r.ScopeID.String(),
		KeaSubnetID: r.KeaSubnetID,
		Status:      string(r.Status),
		Error:       nilIfEmpty(r.Error),
	}
}

// diffResultBody mirrors Python's _diff_result_dict at
// api/ipam.py:2505.
type diffResultBody struct {
	ScopeID     string         `json:"scope_id"`
	KeaSubnetID *int32         `json:"kea_subnet_id"`
	Status      string         `json:"status"`
	DCIMSubnet  map[string]any `json:"dcim_subnet"`
	KeaSubnet   map[string]any `json:"kea_subnet"`
	Delta       map[string]any `json:"delta"`
	Error       *string        `json:"error"`
}

func newDiffResultBody(r diff.Result) diffResultBody {
	// Python's DiffResult is `delta: dict` (not Optional) and every
	// construction path passes delta={} for the four non-drifted
	// statuses (services/dhcp_push.py:865/902/917/922). The Go
	// diff.Result leaves Delta nil on those paths since nil carries
	// the same semantic meaning internally; coerce here so the wire
	// shape stays byte-identical with Python (\"delta\":{} not null).
	// dcim_subnet / kea_subnet remain Optional in Python (None on
	// never_pushed / missing_from_kea / error), so nil → null is
	// correct for those.
	delta := r.Delta
	if delta == nil {
		delta = map[string]any{}
	}
	return diffResultBody{
		ScopeID:     r.ScopeID.String(),
		KeaSubnetID: r.KeaSubnetID,
		Status:      string(r.Status),
		DCIMSubnet:  r.DCIMSubnet,
		KeaSubnet:   r.KeaSubnet,
		Delta:       delta,
		Error:       nilIfEmpty(r.Error),
	}
}

// pushHistoryEntry mirrors the per-row dict shape at api/ipam.py:2447.
// scope_id is omitted per-row (top-level only) so the JSON matches
// Python byte-for-byte; the column is denormalized into the table but
// the response factors it out for compactness.
type pushHistoryEntry struct {
	ID          string  `json:"id"`
	Operation   string  `json:"operation"`
	KeaSubnetID *int32  `json:"kea_subnet_id"`
	Status      string  `json:"status"`
	Error       *string `json:"error"`
	DurationMS  *int32  `json:"duration_ms"`
	AttemptedAt string  `json:"attempted_at"`
}

type pushHistoryBody struct {
	ScopeID string             `json:"scope_id"`
	Entries []pushHistoryEntry `json:"entries"`
}

// nilIfEmpty preserves Python's `error or None` semantics — an empty
// string in the Result becomes JSON `null`, not `""`, so downstream
// consumers can switch on a single nullish check.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pushKeaBuilder + diffKeaBuilder return the production builder
// unless the Handler was configured with a test override. Indirection
// here (rather than Handler.PushKea / Handler.DiffKea being mandatory
// fields) keeps the production wiring one-line in main.go.
func (h *Handler) pushKeaBuilder() push.KeaClientBuilder {
	if h.PushKea != nil {
		return h.PushKea
	}
	return push.DefaultKeaClientBuilder
}

func (h *Handler) diffKeaBuilder() diff.KeaClientBuilder {
	if h.DiffKea != nil {
		return h.DiffKea
	}
	return diff.DefaultKeaClientBuilder
}
