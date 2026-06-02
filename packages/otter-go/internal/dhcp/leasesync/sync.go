// SyncServer orchestrator — Go port of Python's
// services/kea.py:sync_dhcp_server (line 234). Pulls every active
// lease from one Kea CA and upserts the matching IPAddress rows.
//
// Per-lease decisions (matches Python at services/kea.py:307-340):
//
//   - existing source=dhcp        → UPDATE mac, status=active,
//                                   expires; backfill dns_name if
//                                   the row's column is NULL.
//   - existing source != dhcp     → SKIP (operator-owned —
//                                   static / reservation).
//   - no existing row             → INSERT source=dhcp + the lease
//                                   fields.
//   - unmatched lease address     → counted as skipped_no_subnet;
//                                   no DB write.
//
// Per-server failures are recorded on dhcp_servers (last_sync_status
// = "error", last_sync_error = truncated message). The sweep cron
// (PR 16) walks every enabled server and treats per-server errors
// as non-fatal — one broken Kea doesn't stop the sweep.

package leasesync

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

// errMaxLen mirrors Python's `error[:2000]` truncation at
// services/kea.py:297. The last_sync_error column is VARCHAR(2000).
const errMaxLen = 2000

// Querier is the slim DB surface SyncServer reads + writes. *dbq.
// Queries satisfies it. The cron driver (PR 16) composes this with
// the scheduler harness's Querier.
type Querier interface {
	ListSubnetsForFabricLeaseSync(ctx context.Context, fabricID uuid.UUID) ([]dbq.SubnetForLeaseSyncRow, error)
	FindDhcpLeaseIPAddress(ctx context.Context, arg dbq.FindDhcpLeaseIPAddressParams) (dbq.FindDhcpLeaseIPAddressRow, error)
	UpdateDhcpLease(ctx context.Context, arg dbq.UpdateDhcpLeaseParams) error
	InsertDhcpLease(ctx context.Context, arg dbq.InsertDhcpLeaseParams) error
	UpdateDhcpServerSyncState(ctx context.Context, arg dbq.UpdateDhcpServerSyncStateParams) error
}

// KeaClient is the slim view of *kea.Client SyncServer uses — just
// the two lease-list methods. Interface (not the concrete client)
// so tests can inject a stub without standing up an httptest.Server.
type KeaClient interface {
	ListLeases4(ctx context.Context) ([]byte, error)
	ListLeases6(ctx context.Context) ([]byte, error)
}

// KeaClientBuilder constructs a KeaClient for a given server. Same
// pattern as push.KeaClientBuilder + diff.KeaClientBuilder.
type KeaClientBuilder func(server Server) KeaClient

// DefaultKeaClientBuilder wires production *kea.Client instances.
func DefaultKeaClientBuilder(server Server) KeaClient {
	return kea.New(server.KeaURL, server.AuthUsername, server.AuthPassword)
}

// Server is the narrow input shape SyncServer reads. The caller
// projects whatever DhcpServer row it has into this struct so the
// orchestrator stays decoupled from the dbq row layout.
type Server struct {
	ID           uuid.UUID
	FabricID     uuid.UUID
	KeaURL       string
	AuthUsername string
	AuthPassword string
}

// Result mirrors Python's SyncResult dataclass at
// services/kea.py:225. ServerID is stringified for parity.
//
// Upserted semantic divergence from Python: Python increments its
// `upserted` counter (services/kea.py:273) for EVERY lease that
// matched a subnet, including the operator-owned skip path inside
// _upsert_dhcp_lease (line 325 returns without setattr). Go counts
// only the leases that actually wrote a row (INSERT or UPDATE).
// The Go semantic is more useful operationally — "rows we
// ingested" — but the value differs from Python's
// dhcp_servers.last_sync_lease_count for fleets with many
// static / reservation rows. Documented here so the cutover
// observability gap is explicit.
type Result struct {
	ServerID        string
	Upserted        int
	SkippedNoSubnet int
	LeasesSeen      int
	Error           string
}

// SyncServer is the orchestrator. Returns Result with the per-
// outcome counters; non-nil error only for unexpected internal
// failures (DB unreachable on the final UpdateDhcpServerSyncState
// write). A Kea transport failure surfaces as
// Result.Error="<message>" + last_sync_status="error" on the
// dhcp_servers row.
//
// Lease iteration is deterministic-ish: v4 leases first, then v6.
// The matcher's longest-prefix semantics make the iteration order
// irrelevant for correctness, but stable order keeps the
// per-server logs grep-able.
func SyncServer(
	ctx context.Context,
	q Querier,
	build KeaClientBuilder,
	server Server,
	now time.Time,
) (Result, error) {
	startedAt := now.UTC()
	client := build(server)
	leases4Raw, err := client.ListLeases4(ctx)
	if err != nil {
		return recordSyncFailure(ctx, q, server.ID, startedAt, fmt.Sprintf("lease4-get-all: %v", err))
	}
	// Python at line 251-253: v6 errors are swallowed (v4 alone is a
	// valid sync; some Kea fleets don't run dhcp6). Match the posture.
	leases6Raw, _ := client.ListLeases6(ctx)

	subnetRows, err := q.ListSubnetsForFabricLeaseSync(ctx, server.FabricID)
	if err != nil {
		return Result{}, fmt.Errorf("list subnets for fabric %s: %w", server.FabricID, err)
	}
	subnets := make([]Subnet, len(subnetRows))
	for i, s := range subnetRows {
		subnets[i] = Subnet{ID: s.ID, Prefix: s.Prefix}
	}

	upserted, skipped, seen := 0, 0, 0
	for _, raw := range append(kea.ExtractLeases(leases4Raw), kea.ExtractLeases(leases6Raw)...) {
		seen++
		parsed := kea.ParseLease(raw)
		if parsed == nil {
			continue
		}
		subnet := MatchLeaseToSubnet(parsed.Address, subnets)
		if subnet == nil {
			skipped++
			continue
		}
		didWrite, err := upsertLease(ctx, q, *subnet, parsed)
		if err != nil {
			return Result{}, fmt.Errorf("upsert lease %s: %w", parsed.Address, err)
		}
		if didWrite {
			upserted++
		}
	}

	count := int32(upserted)
	if err := q.UpdateDhcpServerSyncState(ctx, dbq.UpdateDhcpServerSyncStateParams{
		ID: server.ID, LastSyncAt: startedAt,
		LastSyncStatus: "ok", LastSyncError: nil,
		LastSyncLeaseCount: &count,
	}); err != nil {
		return Result{}, fmt.Errorf("write sync state: %w", err)
	}
	return Result{
		ServerID: server.ID.String(),
		Upserted: upserted, SkippedNoSubnet: skipped, LeasesSeen: seen,
	}, nil
}

// upsertLease applies the per-lease decision. Returns didWrite=true
// for INSERT or UPDATE, false for "skipped because operator-owned"
// (Python at services/kea.py:325 returns without setattr).
func upsertLease(ctx context.Context, q Querier, subnet Subnet, parsed *kea.ParsedLease) (bool, error) {
	existing, err := q.FindDhcpLeaseIPAddress(ctx, dbq.FindDhcpLeaseIPAddressParams{
		SubnetID: subnet.ID, Address: parsed.Address,
	})
	if err != nil && !isNoRows(err) {
		return false, err
	}
	mac := nilIfEmpty(parsed.MAC)
	hostname := nilIfEmpty(parsed.Hostname)
	expires := parsed.ValidUntil

	if isNoRows(err) {
		// No existing row — INSERT.
		return true, q.InsertDhcpLease(ctx, dbq.InsertDhcpLeaseParams{
			SubnetID: subnet.ID, Address: parsed.Address,
			DnsName: hostname, DhcpMac: mac, DhcpLeaseExpiresAt: expires,
		})
	}
	if existing.Source != "dhcp" {
		// Operator-owned (static or reservation) — leave alone.
		return false, nil
	}
	return true, q.UpdateDhcpLease(ctx, dbq.UpdateDhcpLeaseParams{
		ID: existing.ID, DhcpMac: mac, DnsName: hostname,
		DhcpLeaseExpiresAt: expires,
	})
}

// recordSyncFailure is the early-exit path for a Kea transport
// failure. Writes the truncated error to dhcp_servers and returns a
// Result with Error set (no per-lease counters because we never got
// the leases).
func recordSyncFailure(
	ctx context.Context, q Querier, serverID uuid.UUID,
	startedAt time.Time, message string,
) (Result, error) {
	msg := truncateRuneSafe(message, errMaxLen)
	if err := q.UpdateDhcpServerSyncState(ctx, dbq.UpdateDhcpServerSyncStateParams{
		ID: serverID, LastSyncAt: startedAt,
		LastSyncStatus: "error", LastSyncError: &msg,
		LastSyncLeaseCount: nil,
	}); err != nil {
		return Result{}, fmt.Errorf("write sync failure state: %w", err)
	}
	return Result{ServerID: serverID.String(), Error: message}, nil
}

// nilIfEmpty preserves the empty-string → SQL NULL contract the
// IPAddress columns need. The kea parser already trimmed whitespace
// off hostnames; here we map "" to nil before the SQL boundary.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// truncateRuneSafe clips a string to ≤max bytes without splitting a
// multibyte rune. Postgres VARCHAR enforces UTF-8 on insert; a
// byte-wise s[:max] that lands mid-rune would fail with SQLSTATE
// 22021 and the orchestrator's UpdateDhcpServerSyncState write
// would error out. Walking back to the last full rune is cheap and
// preserves Python's `error[:N]` posture for ASCII inputs (which is
// the common case for Kea error messages).
func truncateRuneSafe(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// isNoRows is the local "row not found" check. Tests inject
// pgx.ErrNoRows the same way the production *dbq.Queries returns it.
// errors.Is unwraps any future fmt.Errorf("%w") chains the Querier
// adapter might introduce.
func isNoRows(err error) bool {
	return err != nil && errors.Is(err, pgx.ErrNoRows)
}
