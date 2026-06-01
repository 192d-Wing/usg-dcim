// Package push is the Go port of Python's services/dhcp_push.py
// push_scope orchestrator (lines 392-500). Walks a single
// DhcpScope onto its parent Kea server: load row → load server →
// load template → render → claim kea_subnet_id if first push →
// call Subnet{4,6}{Add,Update} → ConfigWrite → interpret the
// Kea response → write back last_push_at/status/error → record
// history → return Result.
//
// Delete (delete_scope_from_kea), diff (diff_scope), bulk endpoints,
// HTTP handlers, and the dhcp_sync / dhcp_age_out crons come in
// follow-up PRs that compose this orchestrator.
//
// Design notes carried from Python:
//   - "ok" / "error" / "unsupported" status strings travel through
//     audit logs unchanged so a Python→Go cutover doesn't reshape
//     records mid-flight (kea.Status uses the same literals).
//   - kea_subnet_id is allocated by an O(n) scan against the
//     scopes that already claim one. A production fleet might
//     persist a per-server sequence, but the scan matches Python
//     and is fine until a server has thousands of scopes.
//   - On an `add` (first push) that fails, the optimistic id claim
//     is rolled back to NULL so a retry doesn't burn an id. Update
//     pushes leave kea_subnet_id alone because the id was already
//     claimed in a prior successful push.
//   - last_push_* on the server row is updated on EVERY outcome —
//     transport error, kea error, kea ok. Operators see the failure
//     in the UI without tail -f'ing logs.
//   - Successful push clears the scope's last_diff_* (it's now
//     in-sync with Kea by construction). Error leaves it alone so
//     the previous diff is still the operator's best information.
package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/bundle"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// Querier is the slim DB surface PushScope needs. *dbq.Queries
// satisfies it; the HTTP handler in a follow-up PR composes this
// with the larger ipam.Querier via embedding.
type Querier interface {
	GetDhcpScopeForPush(ctx context.Context, id uuid.UUID) (dbq.DhcpScopeForPushRow, error)
	GetDhcpServerForPush(ctx context.Context, id uuid.UUID) (dbq.DhcpServerForPushRow, error)
	GetDhcpScopeTemplateForPush(ctx context.Context, id uuid.UUID) (dbq.DhcpScopeTemplate, error)
	ListKeaSubnetIDsForServer(ctx context.Context, dhcpServerID uuid.UUID) ([]int32, error)
	UpdateDhcpScopeKeaSubnetID(ctx context.Context, arg dbq.UpdateDhcpScopeKeaSubnetIDParams) error
	UpdateDhcpScopeAfterSuccessfulPush(ctx context.Context, id uuid.UUID) error
	UpdateDhcpServerLastPush(ctx context.Context, arg dbq.UpdateDhcpServerLastPushParams) error
	InsertDhcpScopePushHistory(ctx context.Context, arg dbq.InsertDhcpScopePushHistoryParams) error
}

// KeaClient is the slim view of *kea.Client the orchestrator uses.
// An interface (rather than the concrete client) so tests can inject
// a fake without standing up an httptest.Server every time. Methods
// match kea.Client's signatures 1:1.
type KeaClient interface {
	Subnet4Add(ctx context.Context, subnet map[string]any) ([]byte, error)
	Subnet4Update(ctx context.Context, subnet map[string]any) ([]byte, error)
	Subnet4Del(ctx context.Context, subnetID int64) ([]byte, error)
	Subnet6Add(ctx context.Context, subnet map[string]any) ([]byte, error)
	Subnet6Update(ctx context.Context, subnet map[string]any) ([]byte, error)
	Subnet6Del(ctx context.Context, subnetID int64) ([]byte, error)
	ConfigWrite(ctx context.Context, services []string) ([]byte, error)
}

// KeaClientBuilder constructs a KeaClient for a given server row.
// Production wires *kea.Client; tests inject a stub. The builder
// pattern (rather than a single *kea.Client passed in) means each
// PushScope call gets a client configured for the right server
// without the orchestrator caring about credential storage.
type KeaClientBuilder func(server dbq.DhcpServerForPushRow) KeaClient

// DefaultKeaClientBuilder wires production *kea.Client instances.
// The HTTP handler passes this in; tests pass a stub builder that
// returns a fake.
func DefaultKeaClientBuilder(server dbq.DhcpServerForPushRow) KeaClient {
	user, pass := "", ""
	if server.AuthUsername != nil {
		user = *server.AuthUsername
	}
	if server.AuthPassword != nil {
		pass = *server.AuthPassword
	}
	return kea.New(server.KeaURL, user, pass)
}

// Result is the per-call return shape. Mirrors Python's PushResult
// dataclass (services/dhcp_push.py:74). RawResponse is the raw bytes
// the Kea CA returned so the caller can record them in audit logs;
// nil on a transport-error branch where no response arrived.
type Result struct {
	ScopeID     uuid.UUID
	KeaSubnetID *int32
	Status      kea.Status
	Error       string
	RawResponse []byte
}

// errMaxLen mirrors Python's `error[:2048]` truncation at
// services/dhcp_push.py:570 — the dhcp_servers.last_push_error
// column is VARCHAR(2048).
const errMaxLen = 2048

// ErrServerDisabled is the sentinel returned when an operator-
// disabled server gets a push request. Surfaces as Result.Status="error"
// with the err string in Result.Error; the HTTP handler (PR 3) maps
// it to 422 because the request was well-formed but can't proceed
// without operator action.
var ErrServerDisabled = errors.New("dhcp server disabled; refusing to push")

// AllocateKeaSubnetID returns the lowest unused positive int32 for
// this server. Kea rejects id=0 (reserved as "unspecified" in some
// commands), so we start at 1. Matches Python's
// _allocate_kea_subnet_id at services/dhcp_push.py:328.
func AllocateKeaSubnetID(ctx context.Context, q Querier, serverID uuid.UUID) (int32, error) {
	used, err := q.ListKeaSubnetIDsForServer(ctx, serverID)
	if err != nil {
		return 0, fmt.Errorf("list claimed kea_subnet_ids: %w", err)
	}
	taken := make(map[int32]struct{}, len(used))
	for _, id := range used {
		taken[id] = struct{}{}
	}
	candidate := int32(1)
	for {
		if _, ok := taken[candidate]; !ok {
			return candidate, nil
		}
		candidate++
	}
}

// PushScope is the orchestrator entry point. Loads the scope +
// server + (optional) template, allocates a kea_subnet_id if the
// scope hasn't been pushed before, renders the Kea subnet object,
// calls the appropriate Kea command, and records the outcome.
//
// Returns Result with Status="ok"/"error"/"unsupported" matching
// Python's literals. Non-nil error is returned ONLY for unexpected
// internal failures (DB unreachable, history insert errored, etc.);
// pre-push refusals and transport-to-kea failures surface as
// Result.Status="error" with the err string in Result.Error,
// matching Python's PushResult contract.
func PushScope(ctx context.Context, q Querier, build KeaClientBuilder, scopeID uuid.UUID) (Result, error) {
	scope, err := q.GetDhcpScopeForPush(ctx, scopeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{ScopeID: scopeID, Status: kea.StatusError, Error: "scope not found"}, nil
		}
		return Result{}, fmt.Errorf("load scope: %w", err)
	}
	server, err := q.GetDhcpServerForPush(ctx, scope.DhcpServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{ScopeID: scopeID, Status: kea.StatusError, Error: "parent dhcp server not found"}, nil
		}
		return Result{}, fmt.Errorf("load server: %w", err)
	}
	if !server.Enabled {
		return Result{
			ScopeID: scopeID, KeaSubnetID: scope.KeaSubnetID,
			Status: kea.StatusError, Error: ErrServerDisabled.Error(),
		}, nil
	}
	tpl, err := loadTemplate(ctx, q, scope.TemplateID)
	if err != nil {
		return Result{}, fmt.Errorf("load template: %w", err)
	}
	isUpdate := scope.KeaSubnetID != nil
	operation := "update"
	if !isUpdate {
		operation = "add"
		if allocErr := claimKeaSubnetID(ctx, q, &scope, server.ID); allocErr != nil {
			return Result{}, allocErr
		}
	}
	pushStart := time.Now()
	client := build(server)
	rawResp, status, errStr := callKea(ctx, client, scope, tpl, isUpdate)
	durationMs := int32(time.Since(pushStart).Milliseconds())

	// Roll back the optimistic id claim if the push didn't make it
	// to Kea — leaves the scope in its pre-push state so a retry
	// picks the same low integer rather than fragmenting the
	// namespace. Update pushes leave kea_subnet_id alone (already
	// claimed in a prior successful push).
	if status != kea.StatusOK && !isUpdate {
		scope.KeaSubnetID = nil
		_ = q.UpdateDhcpScopeKeaSubnetID(ctx, dbq.UpdateDhcpScopeKeaSubnetIDParams{
			ID: scope.ID, KeaSubnetID: nil,
		})
	}

	if recErr := recordOutcome(ctx, q, scope, server, operation, status, errStr, durationMs); recErr != nil {
		return Result{}, fmt.Errorf("record push outcome: %w", recErr)
	}
	return Result{
		ScopeID:     scope.ID,
		KeaSubnetID: scope.KeaSubnetID,
		Status:      status,
		Error:       errStr,
		RawResponse: rawResp,
	}, nil
}

// claimKeaSubnetID allocates and writes the new kea_subnet_id on a
// first-push scope. Split out of PushScope to keep the orchestrator
// linear and the cognitive complexity under sonar's 15 ceiling.
func claimKeaSubnetID(ctx context.Context, q Querier, scope *dbq.DhcpScopeForPushRow, serverID uuid.UUID) error {
	allocated, err := AllocateKeaSubnetID(ctx, q, serverID)
	if err != nil {
		return fmt.Errorf("allocate kea_subnet_id: %w", err)
	}
	scope.KeaSubnetID = &allocated
	if err := q.UpdateDhcpScopeKeaSubnetID(ctx, dbq.UpdateDhcpScopeKeaSubnetIDParams{
		ID: scope.ID, KeaSubnetID: scope.KeaSubnetID,
	}); err != nil {
		return fmt.Errorf("write allocated kea_subnet_id: %w", err)
	}
	return nil
}

// loadTemplate fetches the template referenced by the scope, or
// returns (nil, nil) when the scope has no template_id OR the row is
// missing (e.g. deleted). Matches Python's
// merge_template_into_scope(scope, None) fallback.
func loadTemplate(ctx context.Context, q Querier, templateID *uuid.UUID) (*dbq.DhcpScopeTemplate, error) {
	if templateID == nil {
		return nil, nil
	}
	tpl, err := q.GetDhcpScopeTemplateForPush(ctx, *templateID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &tpl, nil
}

// callKea performs the family-correct add/update + config_write
// round-trip. Returns (rawResponse, status, errString). A transport
// failure (Kea unreachable, HTTP error, etc.) surfaces as
// (nil, StatusError, "transport_error: <err>"). A Kea-side error
// (result=1/2/4 or bad shape) surfaces as (raw, status, text)
// straight from kea.InterpretResponse.
func callKea(
	ctx context.Context, client KeaClient,
	scope dbq.DhcpScopeForPushRow, tpl *dbq.DhcpScopeTemplate, isUpdate bool,
) ([]byte, kea.Status, string) {
	bundleScope := bundle.FromDbqScope(asDbqScope(scope))
	var bundleTpl *bundle.Template
	if tpl != nil {
		t := bundle.FromDbqTemplate(*tpl)
		bundleTpl = &t
	}
	effective := bundle.MergeTemplateIntoScope(bundleScope, bundleTpl)

	keaID := int64(*scope.KeaSubnetID)
	var subnet map[string]any
	if scope.IPFamily == 4 {
		subnet = bundle.RenderKeaSubnet4(effective, keaID)
	} else {
		subnet = bundle.RenderKeaSubnet6(effective, keaID)
	}

	resp, err := dispatchKeaCall(ctx, client, scope.IPFamily, isUpdate, subnet)
	if err != nil {
		return nil, kea.StatusError, fmt.Sprintf("transport_error: %s", err.Error())
	}
	// config_write fires only after a successful subnet command; on
	// a Kea-side error we'd be persisting a half-broken config to
	// disk, which is worse than the volatile failure.
	status, msg := kea.InterpretResponse(resp)
	if status == kea.StatusOK {
		svc := "dhcp4"
		if scope.IPFamily == 6 {
			svc = "dhcp6"
		}
		if _, werr := client.ConfigWrite(ctx, []string{svc}); werr != nil {
			return resp, kea.StatusError, fmt.Sprintf("config_write transport error: %s", werr.Error())
		}
	}
	return resp, status, msg
}

func dispatchKeaCall(
	ctx context.Context, client KeaClient,
	ipFamily int32, isUpdate bool, subnet map[string]any,
) ([]byte, error) {
	switch {
	case ipFamily == 4 && isUpdate:
		return client.Subnet4Update(ctx, subnet)
	case ipFamily == 4:
		return client.Subnet4Add(ctx, subnet)
	case isUpdate:
		return client.Subnet6Update(ctx, subnet)
	default:
		return client.Subnet6Add(ctx, subnet)
	}
}

// asDbqScope re-projects a DhcpScopeForPushRow back to a dbq.DhcpScope
// so bundle.FromDbqScope (which takes the full row) accepts it. The
// fields the renderer actually reads are present; the rest get
// zero-values which the renderer doesn't touch.
func asDbqScope(s dbq.DhcpScopeForPushRow) dbq.DhcpScope {
	return dbq.DhcpScope{
		ID:                       s.ID,
		DhcpServerID:             s.DhcpServerID,
		IPFamily:                 s.IPFamily,
		Prefix:                   s.Prefix,
		PoolsJSON:                s.PoolsJSON,
		PdPoolsJSON:              s.PdPoolsJSON,
		OptionsJSON:              s.OptionsJSON,
		ReservationsJSON:         s.ReservationsJSON,
		ValidLifetimeSeconds:     s.ValidLifetimeSeconds,
		RenewTimerSeconds:        s.RenewTimerSeconds,
		RebindTimerSeconds:       s.RebindTimerSeconds,
		PreferredLifetimeSeconds: s.PreferredLifetimeSeconds,
		KeaSubnetID:              s.KeaSubnetID,
		TemplateID:               s.TemplateID,
		Enabled:                  s.Enabled,
	}
}

// recordOutcome writes last_push_at/status/error on the server row
// + appends a push-history row. Mirrors Python's _record_push_status
// + _record_push_history called in series.
func recordOutcome(
	ctx context.Context, q Querier,
	scope dbq.DhcpScopeForPushRow, server dbq.DhcpServerForPushRow,
	operation string, status kea.Status, errStr string, durationMs int32,
) error {
	statusStr := string(status)
	errPtr := maybeErrorPtr(errStr)
	if err := q.UpdateDhcpServerLastPush(ctx, dbq.UpdateDhcpServerLastPushParams{
		ID: server.ID, LastPushStatus: statusStr, LastPushError: errPtr,
	}); err != nil {
		return err
	}
	durationPtr := durationMs
	if err := q.InsertDhcpScopePushHistory(ctx, dbq.InsertDhcpScopePushHistoryParams{
		ScopeID:     scope.ID,
		ServerID:    server.ID,
		Operation:   operation,
		KeaSubnetID: scope.KeaSubnetID,
		Status:      statusStr,
		Error:       errPtr,
		DurationMS:  &durationPtr,
	}); err != nil {
		return err
	}
	// Successful push clears the scope's drift cache — it's now
	// in-sync with Kea by construction.
	if status == kea.StatusOK {
		if err := q.UpdateDhcpScopeAfterSuccessfulPush(ctx, scope.ID); err != nil {
			return err
		}
	}
	return nil
}

// maybeErrorPtr returns nil for an empty string and a pointer for a
// non-empty one. Postgres NULL vs '' distinction matters here — a
// successful push writes NULL into last_push_error so the UI doesn't
// show a stale message from a previous failure.
func maybeErrorPtr(s string) *string {
	if s == "" {
		return nil
	}
	if len(s) > errMaxLen {
		s = s[:errMaxLen]
	}
	return &s
}
