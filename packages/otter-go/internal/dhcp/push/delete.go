// Delete orchestrator — port of Python's delete_scope_from_kea at
// services/dhcp_push.py:503. Best-effort Kea-side cleanup before a
// DCIM DELETE. Returns ok if the scope was never pushed
// (kea_subnet_id IS NULL — nothing to clean up). Kept separate from
// PushScope so the DELETE endpoint can pass-or-log without
// entangling the DB write back into Kea state.
//
// Deliberate differences from PushScope:
//   - Doesn't touch last_push_* on the server row (delete is
//     destructive; conflating it with the push status would obscure
//     the operator's view of whether the live config is fresh).
//   - Doesn't clear the scope's last_diff_* (the scope is being
//     removed anyway, so the diff cache is moot).
//   - History row uses operation="delete".
package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/kea"
)

// DeleteScopeFromKea calls subnet4-del / subnet6-del + config_write
// for one scope. Returns Result with Status=ok when:
//   - the scope was never pushed (kea_subnet_id IS NULL — no-op)
//   - Kea confirmed the deletion (result=0)
//   - Kea reported the subnet wasn't there (result=3) — semantically
//     "already gone" which is the desired post-condition
//
// Returns Status=error/unsupported per kea.InterpretResponse on a
// real Kea-side failure or transport error. Records a history row
// (operation="delete") on every attempt.
//
// Symmetric to PushScope: takes scopeID and loads the scope+server
// itself so HTTP handlers don't have to do the work twice.
func DeleteScopeFromKea(ctx context.Context, q Querier, build KeaClientBuilder, scopeID uuid.UUID) (Result, error) {
	scope, err := q.GetDhcpScopeForPush(ctx, scopeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{ScopeID: scopeID, Status: kea.StatusError, Error: "scope not found"}, nil
		}
		return Result{}, fmt.Errorf("load scope: %w", err)
	}
	// No-push, no-clean-up. Mirrors the Python early return at
	// services/dhcp_push.py:517-521; an unsuspecting caller that
	// asks for delete on a scope with no Kea presence gets an OK
	// result + nil error, so the caller's "delete from DCIM"
	// transaction can proceed.
	if scope.KeaSubnetID == nil {
		return Result{ScopeID: scope.ID, Status: kea.StatusOK}, nil
	}
	server, err := q.GetDhcpServerForPush(ctx, scope.DhcpServerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{ScopeID: scopeID, Status: kea.StatusError, Error: "parent dhcp server not found"}, nil
		}
		return Result{}, fmt.Errorf("load server: %w", err)
	}
	// Disabled-server short-circuit: a disabled server can't accept
	// any RPC, so refuse with an explicit error. Python doesn't
	// gate this explicitly because the RPC would fail anyway, but
	// failing fast saves the operator a confusing transport-error
	// log line and matches PushScope's posture.
	if !server.Enabled {
		return Result{
			ScopeID: scopeID, KeaSubnetID: scope.KeaSubnetID,
			Status: kea.StatusError, Error: ErrServerDisabled.Error(),
		}, nil
	}

	deleteStart := time.Now()
	client := build(server)
	rawResp, status, errStr := callKeaDelete(ctx, client, scope)
	durationMs := int32(time.Since(deleteStart).Milliseconds())

	if err := recordDeleteHistory(ctx, q, scope, server, status, errStr, durationMs); err != nil {
		return Result{}, fmt.Errorf("record delete history: %w", err)
	}
	return Result{
		ScopeID:     scope.ID,
		KeaSubnetID: scope.KeaSubnetID,
		Status:      status,
		Error:       errStr,
		RawResponse: rawResp,
	}, nil
}

// callKeaDelete invokes the family-correct subnet-del + config_write.
// Returns (raw, status, errStr). Transport failure surfaces as
// (nil, StatusError, "transport_error: ..."); Kea-side error from
// InterpretResponse passes through verbatim. config_write fires
// only after a successful delete — persisting a half-rolled
// removal would leave Kea in a state we couldn't reason about.
func callKeaDelete(ctx context.Context, client KeaClient, scope dbq.DhcpScopeForPushRow) ([]byte, kea.Status, string) {
	keaID := int64(*scope.KeaSubnetID)
	var resp []byte
	var rpcErr error
	if scope.IPFamily == 4 {
		resp, rpcErr = client.Subnet4Del(ctx, keaID)
	} else {
		resp, rpcErr = client.Subnet6Del(ctx, keaID)
	}
	if rpcErr != nil {
		return nil, kea.StatusError, fmt.Sprintf("transport_error: %s", rpcErr.Error())
	}
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

// recordDeleteHistory writes one push-history row for the delete
// attempt. Unlike PushScope's recordOutcome, this doesn't touch
// dhcp_servers.last_push_* (operator-facing last-push status stays
// scoped to push/update operations) and doesn't clear
// dhcp_scopes.last_diff_* (the scope row is on its way out).
func recordDeleteHistory(
	ctx context.Context, q Querier,
	scope dbq.DhcpScopeForPushRow, server dbq.DhcpServerForPushRow,
	status kea.Status, errStr string, durationMs int32,
) error {
	errPtr := maybeErrorPtr(errStr)
	durationPtr := durationMs
	return q.InsertDhcpScopePushHistory(ctx, dbq.InsertDhcpScopePushHistoryParams{
		ScopeID:     scope.ID,
		ServerID:    server.ID,
		Operation:   "delete",
		KeaSubnetID: scope.KeaSubnetID,
		Status:      string(status),
		Error:       errPtr,
		DurationMS:  &durationPtr,
	})
}
