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
	"fmt"
	"time"

	"github.com/google/uuid"

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
	scope, ok, errRes, fatalErr := loadScopeForPush(ctx, q, scopeID)
	if fatalErr != nil {
		return Result{}, fatalErr
	}
	if !ok {
		return errRes, nil
	}
	// No-push, no-clean-up. Mirrors the Python early return at
	// services/dhcp_push.py:517-521; an unsuspecting caller that
	// asks for delete on a scope with no Kea presence gets an OK
	// result + nil error, so the caller's "delete from DCIM"
	// transaction can proceed.
	if scope.KeaSubnetID == nil {
		return Result{ScopeID: scope.ID, Status: kea.StatusOK}, nil
	}
	// Disabled-server short-circuit: refuses fast (Python implicitly
	// fails via the RPC, but the explicit gate saves a confusing
	// transport-error log line).
	server, ok, errRes, fatalErr := loadEnabledServerForPush(ctx, q, scopeID, scope.DhcpServerID, scope.KeaSubnetID)
	if fatalErr != nil {
		return Result{}, fatalErr
	}
	if !ok {
		return errRes, nil
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
func callKeaDelete(ctx context.Context, client KeaClient, scope dbq.GetDhcpScopeForPushRow) ([]byte, kea.Status, string) {
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
	scope dbq.GetDhcpScopeForPushRow, server dbq.GetDhcpServerForPushRow,
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
