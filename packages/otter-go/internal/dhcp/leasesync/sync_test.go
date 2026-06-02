// Tests for SyncServer — the lease-ingest orchestrator. Each test
// injects a fake Querier + fake KeaClient so the unit suite stays
// pure (no DB, no HTTP). Per-decision taxonomy and the
// `static rows are operator-owned, leave alone` Python parity are
// pinned here.
package leasesync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// captureQ stands in for *dbq.Queries: returns canned reads, records
// every write so tests can assert on the per-decision SQL params.
type captureQ struct {
	subnetRows  []dbq.SubnetForLeaseSyncRow
	subnetErr   error

	// findResults maps (subnet_id, address) → row.
	findResults map[string]dbq.FindDhcpLeaseIPAddressRow

	updates  []dbq.UpdateDhcpLeaseParams
	inserts  []dbq.InsertDhcpLeaseParams
	syncStates []dbq.UpdateDhcpServerSyncStateParams

	updateErr error
	insertErr error
	stateErr  error
}

func (c *captureQ) ListSubnetsForFabricLeaseSync(_ context.Context, _ uuid.UUID) ([]dbq.SubnetForLeaseSyncRow, error) {
	return c.subnetRows, c.subnetErr
}
func (c *captureQ) FindDhcpLeaseIPAddress(_ context.Context, arg dbq.FindDhcpLeaseIPAddressParams) (dbq.FindDhcpLeaseIPAddressRow, error) {
	key := arg.SubnetID.String() + "/" + arg.Address
	r, ok := c.findResults[key]
	if !ok {
		return dbq.FindDhcpLeaseIPAddressRow{}, pgx.ErrNoRows
	}
	return r, nil
}
func (c *captureQ) UpdateDhcpLease(_ context.Context, arg dbq.UpdateDhcpLeaseParams) error {
	c.updates = append(c.updates, arg)
	return c.updateErr
}
func (c *captureQ) InsertDhcpLease(_ context.Context, arg dbq.InsertDhcpLeaseParams) error {
	c.inserts = append(c.inserts, arg)
	return c.insertErr
}
func (c *captureQ) UpdateDhcpServerSyncState(_ context.Context, arg dbq.UpdateDhcpServerSyncStateParams) error {
	c.syncStates = append(c.syncStates, arg)
	return c.stateErr
}

// stubKea returns canned lease4/lease6 responses + optional errors.
type stubKea struct {
	lease4Body []byte
	lease4Err  error
	lease6Body []byte
	lease6Err  error
}

func (s *stubKea) ListLeases4(_ context.Context) ([]byte, error) { return s.lease4Body, s.lease4Err }
func (s *stubKea) ListLeases6(_ context.Context) ([]byte, error) { return s.lease6Body, s.lease6Err }

func builderReturning(k *stubKea) KeaClientBuilder {
	return func(_ Server) KeaClient { return k }
}

// leaseEnvelope wraps lease maps in Kea's per-service response
// shape: [{"result":0, "arguments":{"leases":[...]}}].
func leaseEnvelope(t *testing.T, leases []map[string]any) []byte {
	t.Helper()
	body := []map[string]any{{
		"result":    float64(0),
		"arguments": map[string]any{"leases": leases},
	}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newServer() Server {
	return Server{
		ID: uuid.New(), FabricID: uuid.New(),
		KeaURL: "http://kea.example", AuthUsername: "u", AuthPassword: "p",
	}
}

func TestSyncServer_HappyPath_InsertsNewLease(t *testing.T) {
	srv := newServer()
	subnetID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5",
			"hw-address": "aa:bb:cc:dd:ee:01",
			"state":      float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.LeasesSeen != 1 || got.Upserted != 1 || got.SkippedNoSubnet != 0 {
		t.Errorf("result = %+v", got)
	}
	if len(q.inserts) != 1 {
		t.Fatalf("inserts = %d, want 1", len(q.inserts))
	}
	if q.inserts[0].Address != "10.0.0.5" || q.inserts[0].SubnetID != subnetID {
		t.Errorf("insert params = %+v", q.inserts[0])
	}
	if q.inserts[0].DhcpMac == nil || *q.inserts[0].DhcpMac != "aa:bb:cc:dd:ee:01" {
		t.Errorf("insert mac = %v", q.inserts[0].DhcpMac)
	}
	// Per-server sync state must land as ok.
	if len(q.syncStates) != 1 || q.syncStates[0].LastSyncStatus != "ok" {
		t.Errorf("sync states = %+v", q.syncStates)
	}
}

func TestSyncServer_ExistingDhcpLease_Updates(t *testing.T) {
	srv := newServer()
	subnetID := uuid.New()
	ipID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{
			subnetID.String() + "/10.0.0.5": {ID: ipID, Source: "dhcp"},
		},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5",
			"hw-address": "aa:bb:cc:dd:ee:01",
			"hostname":   "host-1",
			"state":      float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Upserted != 1 {
		t.Errorf("Upserted = %d, want 1", got.Upserted)
	}
	if len(q.updates) != 1 || q.updates[0].ID != ipID {
		t.Errorf("updates = %+v, want UPDATE on %s", q.updates, ipID)
	}
	if len(q.inserts) != 0 {
		t.Errorf("must not INSERT when row exists; got %d inserts", len(q.inserts))
	}
}

func TestSyncServer_StaticRowLeftAlone(t *testing.T) {
	srv := newServer()
	subnetID := uuid.New()
	ipID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{
			subnetID.String() + "/10.0.0.5": {ID: ipID, Source: "static"},
		},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5", "hw-address": "aa:bb:cc:dd:ee:01", "state": float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Lease was seen but the source=static row was NOT touched —
	// operator-owned addresses are protected.
	if got.LeasesSeen != 1 || got.Upserted != 0 {
		t.Errorf("got %+v, want seen=1 upserted=0", got)
	}
	if len(q.updates) != 0 || len(q.inserts) != 0 {
		t.Errorf("static row must not be mutated; updates=%d inserts=%d", len(q.updates), len(q.inserts))
	}
}

func TestSyncServer_ReservationRowLeftAlone(t *testing.T) {
	// Same posture as static: source=reservation is operator-owned.
	srv := newServer()
	subnetID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{
			subnetID.String() + "/10.0.0.5": {ID: uuid.New(), Source: "reservation"},
		},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5", "hw-address": "aa:bb:cc:dd:ee:01", "state": float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Upserted != 0 {
		t.Errorf("Upserted = %d, want 0", got.Upserted)
	}
	if len(q.updates) != 0 {
		t.Errorf("reservation row must not be mutated; got %d updates", len(q.updates))
	}
}

func TestSyncServer_UnmatchedLease_SkippedNoSubnet(t *testing.T) {
	srv := newServer()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: uuid.New(), Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{},
	}
	k := &stubKea{
		// 192.168.1.1 isn't in any subnet of the fabric.
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "192.168.1.1", "hw-address": "aa:bb:cc:dd:ee:01", "state": float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.SkippedNoSubnet != 1 {
		t.Errorf("SkippedNoSubnet = %d, want 1", got.SkippedNoSubnet)
	}
	if len(q.inserts) != 0 {
		t.Errorf("unmatched lease must not insert; got %d inserts", len(q.inserts))
	}
}

func TestSyncServer_KeaLease4Error_RecordsFailure(t *testing.T) {
	srv := newServer()
	q := &captureQ{}
	k := &stubKea{lease4Err: errors.New("connection refused")}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Error == "" {
		t.Errorf("Error empty; want non-empty for transport failure")
	}
	if len(q.syncStates) != 1 || q.syncStates[0].LastSyncStatus != "error" {
		t.Fatalf("sync state = %+v, want one error row", q.syncStates)
	}
	if q.syncStates[0].LastSyncError == nil || *q.syncStates[0].LastSyncError == "" {
		t.Errorf("last_sync_error must be populated")
	}
	// Failure path writes nil lease_count so operators see "we
	// don't know" instead of "0 leases, sync was fine".
	if q.syncStates[0].LastSyncLeaseCount != nil {
		t.Errorf("LastSyncLeaseCount = %v, want nil on failure", q.syncStates[0].LastSyncLeaseCount)
	}
}

func TestSyncServer_Lease6ErrorSwallowed(t *testing.T) {
	// Python parity at services/kea.py:251-253: v6 fetch errors are
	// silently dropped so a v4-only Kea fleet still syncs.
	srv := newServer()
	subnetID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5", "hw-address": "aa:bb:cc:dd:ee:01", "state": float64(0),
		}}),
		lease6Err: errors.New("dhcp6 not running"),
	}
	got, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.Upserted != 1 {
		t.Errorf("Upserted = %d, want 1 (v4 should still sync)", got.Upserted)
	}
	if q.syncStates[0].LastSyncStatus != "ok" {
		t.Errorf("status = %q, want ok (v6 error must not poison v4)", q.syncStates[0].LastSyncStatus)
	}
}

func TestSyncServer_ErrorMessageTruncated(t *testing.T) {
	srv := newServer()
	q := &captureQ{}
	long := make([]byte, 3000)
	for i := range long {
		long[i] = 'x'
	}
	k := &stubKea{lease4Err: errors.New(string(long))}
	_, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if q.syncStates[0].LastSyncError == nil {
		t.Fatal("LastSyncError nil")
	}
	if len(*q.syncStates[0].LastSyncError) > errMaxLen {
		t.Errorf("error not truncated: %d chars", len(*q.syncStates[0].LastSyncError))
	}
}

// Python parity at services/kea.py:328: a truthy parsed.hostname
// OVERWRITES the existing dns_name. Go achieves the same shape by
// passing the non-empty hostname through to the SQL's
// COALESCE($3, dns_name) — non-NULL incoming wins. Pin the
// orchestrator's contract here.
func TestSyncServer_UpdateOverwritesDnsNameWhenHostnameSet(t *testing.T) {
	srv := newServer()
	subnetID := uuid.New()
	ipID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{
			subnetID.String() + "/10.0.0.5": {ID: ipID, Source: "dhcp"},
		},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5",
			"hw-address": "aa:bb:cc:dd:ee:01",
			"hostname":   "new-host",
			"state":      float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	_, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(q.updates))
	}
	if q.updates[0].DnsName == nil || *q.updates[0].DnsName != "new-host" {
		t.Errorf("DnsName = %v, want \"new-host\" (truthy hostname must overwrite)", q.updates[0].DnsName)
	}
}

// Empty hostname must NOT overwrite the existing dns_name — Python's
// `parsed.hostname or existing.dns_name` keeps the existing value
// when the new is empty. nilIfEmpty maps "" → nil before the SQL,
// then COALESCE($3, dns_name) keeps the existing column.
func TestSyncServer_UpdateKeepsExistingDnsNameWhenHostnameEmpty(t *testing.T) {
	srv := newServer()
	subnetID := uuid.New()
	ipID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{
			subnetID.String() + "/10.0.0.5": {ID: ipID, Source: "dhcp"},
		},
	}
	k := &stubKea{
		// No hostname field; parser emits "" which nilIfEmpty
		// translates to nil → COALESCE keeps existing.
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5",
			"hw-address": "aa:bb:cc:dd:ee:01",
			"state":      float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	_, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(q.updates))
	}
	if q.updates[0].DnsName != nil {
		t.Errorf("DnsName = %v, want nil (empty hostname → keep existing via COALESCE)", q.updates[0].DnsName)
	}
}

// A non-pgx.ErrNoRows error from FindDhcpLeaseIPAddress (transient
// DB hiccup) must propagate as a non-nil SyncServer error so the
// cron driver can log it. The handler treats this as fatal because
// the per-lease DB call failed for an unknown reason; the cron
// driver in PR 16 will record it on the dhcp_server row.
func TestSyncServer_FindLeaseTransientError_Propagates(t *testing.T) {
	srv := newServer()
	subnetID := uuid.New()
	q := &transientFindErrQ{
		captureQ: captureQ{
			subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		},
		err: errors.New("conn reset"),
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5", "hw-address": "aa:bb:cc:dd:ee:01", "state": float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	_, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err == nil {
		t.Fatal("expected non-nil err on transient find failure")
	}
}

// transientFindErrQ overrides FindDhcpLeaseIPAddress to inject an
// arbitrary error (not pgx.ErrNoRows). Used to pin the per-lease
// DB-error-propagation contract.
type transientFindErrQ struct {
	captureQ
	err error
}

func (f *transientFindErrQ) FindDhcpLeaseIPAddress(_ context.Context, _ dbq.FindDhcpLeaseIPAddressParams) (dbq.FindDhcpLeaseIPAddressRow, error) {
	return dbq.FindDhcpLeaseIPAddressRow{}, f.err
}

// truncateRuneSafe must not split a multibyte rune. ASCII-only
// inputs trim at the boundary; multibyte inputs (e.g. operator
// error messages from non-ASCII Kea deployments) walk back to the
// last full rune.
func TestTruncateRuneSafe_MultibyteAware(t *testing.T) {
	// "héllo" = h(1) é(2) l(1) l(1) o(1) = 6 bytes. Truncating at
	// max=2 should yield "h" (drop the partial é), not "h\xC3".
	got := truncateRuneSafe("héllo", 2)
	for i := 0; i < len(got); i++ {
		// Every byte must be a valid leading byte or ASCII; no
		// continuation byte (10xxxxxx) at the end.
		if got[i]&0xC0 == 0x80 {
			t.Errorf("truncateRuneSafe left a continuation byte at %d: %q", i, got)
		}
	}
}

func TestSyncServer_EmptyHostnameWritesNull(t *testing.T) {
	// PR 14 contract: kea.ParseLease emits "" for missing hostname;
	// the orchestrator translates "" → SQL NULL so the column doesn't
	// store empty strings (Python had None vs "" but db column
	// always NULL on missing — keep parity).
	srv := newServer()
	subnetID := uuid.New()
	q := &captureQ{
		subnetRows: []dbq.SubnetForLeaseSyncRow{{ID: subnetID, Prefix: "10.0.0.0/24"}},
		findResults: map[string]dbq.FindDhcpLeaseIPAddressRow{},
	}
	k := &stubKea{
		lease4Body: leaseEnvelope(t, []map[string]any{{
			"ip-address": "10.0.0.5", "hw-address": "aa:bb:cc:dd:ee:01", "state": float64(0),
		}}),
		lease6Body: []byte(`[]`),
	}
	_, err := SyncServer(context.Background(), q, builderReturning(k), srv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(q.inserts) != 1 || q.inserts[0].DnsName != nil {
		t.Errorf("DnsName = %v, want nil (no hostname in lease)", q.inserts[0].DnsName)
	}
}
