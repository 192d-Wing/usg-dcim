// Tests for the dhcp_sync scheduler job. The leasesync.SyncServer
// orchestrator is covered exhaustively in
// internal/dhcp/leasesync/sync_test.go; these focus on the cron
// driver's concerns:
//
//   - nil Q rejected
//   - list err wrapped
//   - empty fleet → zero aggregate
//   - per-server SyncServer fatal err logged + loop continues
//   - per-server Kea transport err (result.Error set) counted as
//     errCount, doesn't abort the sweep
//   - ctx cancel between servers → partial counts
//   - aggregate counters sum across servers
package dhcpsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dhcp/leasesync"
)

// fakeQ stands in for *dbq.Queries: returns canned reads + lets
// tests inject errors per server id.
type fakeQ struct {
	servers   []dbq.ListEnabledDhcpServersForLeaseSyncRow
	listErr   error

	// leasesync.Querier methods. Per-server SyncServer pulls subnets
	// + finds/upserts; we route everything to no-op so per-server
	// invariants are exercised at the harness level only.
	subnetsByServer map[uuid.UUID][]dbq.ListSubnetsForFabricLeaseSyncRow

	// findResult/findErr override the per-key result so a test can
	// pin "no existing row" (default) vs an existing dhcp row.
	findResults map[string]dbq.FindDhcpLeaseIPAddressRow

	// stateErr lets one test force the orchestrator into the fatal-
	// err branch by failing the final UpdateDhcpServerSyncState write.
	stateErr error
}

func (f *fakeQ) ListEnabledDhcpServersForLeaseSync(_ context.Context) ([]dbq.ListEnabledDhcpServersForLeaseSyncRow, error) {
	return f.servers, f.listErr
}
func (f *fakeQ) ListSubnetsForFabricLeaseSync(_ context.Context, fabricID uuid.UUID) ([]dbq.ListSubnetsForFabricLeaseSyncRow, error) {
	return f.subnetsByServer[fabricID], nil
}
func (f *fakeQ) FindDhcpLeaseIPAddress(_ context.Context, arg dbq.FindDhcpLeaseIPAddressParams) (dbq.FindDhcpLeaseIPAddressRow, error) {
	r, ok := f.findResults[arg.SubnetID.String()+"/"+arg.Address]
	if !ok {
		return dbq.FindDhcpLeaseIPAddressRow{}, pgx.ErrNoRows
	}
	return r, nil
}
func (f *fakeQ) UpdateDhcpLease(_ context.Context, _ dbq.UpdateDhcpLeaseParams) error { return nil }
func (f *fakeQ) InsertDhcpLease(_ context.Context, _ dbq.InsertDhcpLeaseParams) error { return nil }
func (f *fakeQ) UpdateDhcpServerSyncState(_ context.Context, _ dbq.UpdateDhcpServerSyncStateParams) error {
	return f.stateErr
}

// stubKea emits an empty pair of lease lists so the per-server
// SyncServer succeeds without writing anything. Each test that
// needs actual lease data overrides via per-test stubKea.
type stubKea struct{ lease4Err, lease6Err error }

func (s stubKea) ListLeases4(_ context.Context) ([]byte, error) { return []byte(`[]`), s.lease4Err }
func (s stubKea) ListLeases6(_ context.Context) ([]byte, error) { return []byte(`[]`), s.lease6Err }

func builder(k stubKea) leasesync.KeaClientBuilder {
	return func(_ leasesync.Server) leasesync.KeaClient { return k }
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected err for nil Q")
	}
}

func TestRun_ListErr_Wrapped(t *testing.T) {
	f := &fakeQ{listErr: errors.New("conn refused")}
	j := &Job{Q: f, KeaBuilder: builder(stubKea{})}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("want non-nil")
	}
	if !errors.Is(err, f.listErr) {
		t.Errorf("err chain doesn't wrap listErr: %v", err)
	}
}

func TestRun_EmptyFleet_ZeroAggregate(t *testing.T) {
	f := &fakeQ{}
	j := &Job{Q: f, KeaBuilder: builder(stubKea{})}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"servers", "errors", "total_upserted",
		"total_skipped_no_subnet", "total_leases_seen",
	} {
		if v, ok := out[key].(int); !ok || v != 0 {
			t.Errorf("out[%q] = %v, want 0", key, out[key])
		}
	}
}

func TestRun_PerServerKeaError_CountsAsError(t *testing.T) {
	// When ListLeases4 fails, SyncServer records last_sync_status=
	// 'error' and returns result.Error != "". The cron driver
	// counts this as errCount but does NOT abort the sweep.
	srvA := uuid.New()
	srvB := uuid.New()
	f := &fakeQ{
		servers: []dbq.ListEnabledDhcpServersForLeaseSyncRow{
			{ID: srvA, FabricID: uuid.New(), KeaURL: "http://kea-a"},
			{ID: srvB, FabricID: uuid.New(), KeaURL: "http://kea-b"},
		},
	}
	// First server fails; second succeeds (empty). Use a stub that
	// always fails — the driver should walk both and count one
	// error per server.
	j := &Job{Q: f, KeaBuilder: builder(stubKea{lease4Err: errors.New("kea unreachable")})}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["servers"].(int) != 2 {
		t.Errorf("servers = %v, want 2", out["servers"])
	}
	if out["errors"].(int) != 2 {
		t.Errorf("errors = %v, want 2 (both servers Kea-unreachable)", out["errors"])
	}
}

func TestRun_PerServerFatalErr_LoggedAndContinues(t *testing.T) {
	// A non-nil err from SyncServer (DB unreachable inside the
	// orchestrator) must NOT abort the sweep. We force this by
	// making the final UpdateDhcpServerSyncState fail.
	srvA := uuid.New()
	srvB := uuid.New()
	f := &fakeQ{
		servers: []dbq.ListEnabledDhcpServersForLeaseSyncRow{
			{ID: srvA, FabricID: uuid.New(), KeaURL: "http://kea-a"},
			{ID: srvB, FabricID: uuid.New(), KeaURL: "http://kea-b"},
		},
		stateErr: errors.New("db down"),
	}
	j := &Job{Q: f, KeaBuilder: builder(stubKea{})}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["servers"].(int) != 2 {
		t.Errorf("servers = %v, want 2", out["servers"])
	}
	if out["errors"].(int) != 2 {
		t.Errorf("errors = %v, want 2 (both walked, both wrote sync state and got DB err)", out["errors"])
	}
}

func TestRun_ContextCancel_BailsWithPartialCounts(t *testing.T) {
	f := &fakeQ{
		servers: []dbq.ListEnabledDhcpServersForLeaseSyncRow{
			{ID: uuid.New(), FabricID: uuid.New(), KeaURL: "http://a"},
			{ID: uuid.New(), FabricID: uuid.New(), KeaURL: "http://b"},
			{ID: uuid.New(), FabricID: uuid.New(), KeaURL: "http://c"},
		},
	}
	j := &Job{Q: f, KeaBuilder: builder(stubKea{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := j.Run(ctx)
	if err != nil {
		t.Errorf("ctx cancel must return nil err, got %v", err)
	}
	if got := out["servers"].(int); got != 0 {
		t.Errorf("servers = %d, want 0 on immediate cancel", got)
	}
}

// leaseStubKea returns one v4 lease per ListLeases4 call. Used to
// pin the aggregate-sum invariant — without per-server upserts the
// total_upserted etc. counters always stay at 0 even if the
// summation regresses to assignment.
type leaseStubKea struct {
	address string
	mac     string
}

func (k leaseStubKea) ListLeases4(_ context.Context) ([]byte, error) {
	body := `[{"result":0,"arguments":{"leases":[{"ip-address":"` + k.address +
		`","hw-address":"` + k.mac + `","state":0}]}}]`
	return []byte(body), nil
}
func (k leaseStubKea) ListLeases6(_ context.Context) ([]byte, error) {
	return []byte(`[]`), nil
}

func TestRun_HappyPath_TotalUpsertedSumsAcrossServers(t *testing.T) {
	// Each server gets one lease in its /24; the cron walks both and
	// the aggregate carries upserted=2. Catches a regression that
	// flips += to = (which the empty-leases happy-path test below
	// can't detect because every counter stays at 0).
	srv1FabricID := uuid.New()
	srv2FabricID := uuid.New()
	subnet1 := uuid.New()
	subnet2 := uuid.New()
	f := &fakeQ{
		servers: []dbq.ListEnabledDhcpServersForLeaseSyncRow{
			{ID: uuid.New(), FabricID: srv1FabricID, KeaURL: "http://a"},
			{ID: uuid.New(), FabricID: srv2FabricID, KeaURL: "http://b"},
		},
		subnetsByServer: map[uuid.UUID][]dbq.ListSubnetsForFabricLeaseSyncRow{
			srv1FabricID: {{ID: subnet1, Prefix: "10.0.0.0/24"}},
			srv2FabricID: {{ID: subnet2, Prefix: "10.0.1.0/24"}},
		},
	}
	// Both KeaClients return one lease — the orchestrator's matcher
	// picks the correct subnet for each server's fabric.
	j := &Job{Q: f, KeaBuilder: func(server leasesync.Server) leasesync.KeaClient {
		if server.FabricID == srv1FabricID {
			return leaseStubKea{address: "10.0.0.5", mac: "aa:bb:cc:dd:ee:01"}
		}
		return leaseStubKea{address: "10.0.1.5", mac: "aa:bb:cc:dd:ee:02"}
	}}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := out["total_upserted"].(int); got != 2 {
		t.Errorf("total_upserted = %d, want 2", got)
	}
	if got := out["total_leases_seen"].(int); got != 2 {
		t.Errorf("total_leases_seen = %d, want 2", got)
	}
}

func TestRun_HappyPath_AggregatesPerServerCounters(t *testing.T) {
	// Each server returns empty leases (no upserts), but the
	// driver still walks both and sums (0+0).
	f := &fakeQ{
		servers: []dbq.ListEnabledDhcpServersForLeaseSyncRow{
			{ID: uuid.New(), FabricID: uuid.New(), KeaURL: "http://a"},
			{ID: uuid.New(), FabricID: uuid.New(), KeaURL: "http://b"},
		},
	}
	j := &Job{Q: f, KeaBuilder: builder(stubKea{}), Now: func() time.Time { return time.Unix(1, 0) }}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["servers"].(int) != 2 || out["errors"].(int) != 0 {
		t.Errorf("out = %+v", out)
	}
}
