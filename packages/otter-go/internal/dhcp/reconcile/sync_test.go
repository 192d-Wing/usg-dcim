// Tests for the mutating reconcile sync. Each test injects a fake
// Writer to capture the SQL params Sync emits; the unit suite
// stays pure (no DB). Per-decision taxonomy and the
// `only-backfill-never-overwrite` Python parity are pinned here.
package reconcile

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// captureWriter records every Insert/Promote params + lets tests
// inject errors. Mirrors the reconcile.Writer interface.
type captureWriter struct {
	inserts []dbq.InsertReservationIPAddressParams
	promotes []dbq.PromoteDhcpLeaseToReservationParams

	nextInsertID uuid.UUID
	insertErr    error
	promoteErr   error
}

func (c *captureWriter) InsertReservationIPAddress(_ context.Context, arg dbq.InsertReservationIPAddressParams) (uuid.UUID, error) {
	c.inserts = append(c.inserts, arg)
	if c.insertErr != nil {
		return uuid.Nil, c.insertErr
	}
	id := c.nextInsertID
	if id == uuid.Nil {
		id = uuid.New()
	}
	return id, nil
}
func (c *captureWriter) PromoteDhcpLeaseToReservation(_ context.Context, arg dbq.PromoteDhcpLeaseToReservationParams) error {
	c.promotes = append(c.promotes, arg)
	return c.promoteErr
}

func TestSync_NoSubnet_AllSkipped(t *testing.T) {
	scopeID := uuid.New()
	res := []map[string]any{
		reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5"),
		reservation("aa:bb:cc:dd:ee:02", "", "10.0.0.6"),
	}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, scopeID, nil, mustJSON(t, res), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SkippedNoSubnet != 2 {
		t.Errorf("SkippedNoSubnet = %d, want 2", got.SkippedNoSubnet)
	}
	if len(w.inserts) != 0 || len(w.promotes) != 0 {
		t.Errorf("must not insert/promote on no-subnet scope")
	}
	if got.Entries[0]["decision"] != "skipped_no_subnet" {
		t.Errorf("entry decision = %v", got.Entries[0]["decision"])
	}
}

func TestSync_UnbackedReservation_Inserts(t *testing.T) {
	subnetID := uuid.New()
	mac := "aa:bb:cc:dd:ee:01"
	res := []map[string]any{reservation(mac, "", "10.0.0.5")}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Upserted != 1 {
		t.Errorf("Upserted = %d, want 1", got.Upserted)
	}
	if len(w.inserts) != 1 {
		t.Fatalf("inserts = %d, want 1", len(w.inserts))
	}
	if w.inserts[0].Address != "10.0.0.5" {
		t.Errorf("insert address = %q", w.inserts[0].Address)
	}
	if w.inserts[0].DhcpMac == nil || *w.inserts[0].DhcpMac != mac {
		t.Errorf("insert dhcp_mac = %v, want %q", w.inserts[0].DhcpMac, mac)
	}
	if w.inserts[0].SubnetID != subnetID {
		t.Errorf("insert subnet_id = %v, want %v", w.inserts[0].SubnetID, subnetID)
	}
}

func TestSync_DhcpSourcePromoted(t *testing.T) {
	subnetID := uuid.New()
	ipID := uuid.New()
	mac := "aa:bb:cc:dd:ee:01"
	res := []map[string]any{reservation(mac, "", "10.0.0.5")}
	rows := []dbq.ListIPAddressesInSubnetForReconcileRow{
		{ID: ipID, Address: "10.0.0.5", Source: "dhcp", DhcpMac: &mac},
	}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), rows)
	if err != nil {
		t.Fatal(err)
	}
	if got.Promoted != 1 {
		t.Errorf("Promoted = %d, want 1", got.Promoted)
	}
	if len(w.promotes) != 1 || w.promotes[0].ID != ipID {
		t.Errorf("promote params = %+v", w.promotes)
	}
}

func TestSync_StaticSourceSkippedCollision(t *testing.T) {
	subnetID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5")}
	rows := []dbq.ListIPAddressesInSubnetForReconcileRow{{ID: uuid.New(), Address: "10.0.0.5", Source: "static"}}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), rows)
	if err != nil {
		t.Fatal(err)
	}
	if got.SkippedCollision != 1 {
		t.Errorf("SkippedCollision = %d", got.SkippedCollision)
	}
	if len(w.inserts) != 0 || len(w.promotes) != 0 {
		t.Errorf("static rows must not be mutated, got inserts=%d promotes=%d", len(w.inserts), len(w.promotes))
	}
}

func TestSync_AlreadyReservation_SkippedClean(t *testing.T) {
	subnetID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5")}
	rows := []dbq.ListIPAddressesInSubnetForReconcileRow{{ID: uuid.New(), Address: "10.0.0.5", Source: "reservation"}}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), rows)
	if err != nil {
		t.Fatal(err)
	}
	if got.SkippedClean != 1 {
		t.Errorf("SkippedClean = %d", got.SkippedClean)
	}
	if len(w.promotes) != 0 {
		t.Errorf("clean rows must not re-promote")
	}
}

func TestSync_MacMismatch_RefusesToPromote(t *testing.T) {
	subnetID := uuid.New()
	leaseMac := "11:22:33:44:55:66"
	resMac := "aa:bb:cc:dd:ee:01"
	rows := []dbq.ListIPAddressesInSubnetForReconcileRow{
		{ID: uuid.New(), Address: "10.0.0.5", Source: "dhcp", DhcpMac: &leaseMac},
	}
	res := []map[string]any{reservation(resMac, "", "10.0.0.5")}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), rows)
	if err != nil {
		t.Fatal(err)
	}
	if got.SkippedMacMismatch != 1 {
		t.Errorf("SkippedMacMismatch = %d", got.SkippedMacMismatch)
	}
	if len(w.promotes) != 0 {
		t.Errorf("mismatched MAC must not promote; got %d promotes", len(w.promotes))
	}
	// Entry carries both MACs so the operator can see the conflict
	// in the UI without grepping the lease table.
	e := got.Entries[0]
	if e["reservation_mac"] != resMac || e["row_mac"] != leaseMac {
		t.Errorf("entry MAC fields = %+v", e)
	}
}

func TestSync_DuidMismatch_RefusesToPromote(t *testing.T) {
	subnetID := uuid.New()
	leaseDuid := "00:01:00:01:abcd:ef00"
	resDuid := "00:01:00:01:dead:beef"
	rows := []dbq.ListIPAddressesInSubnetForReconcileRow{
		{ID: uuid.New(), Address: "2001:db8::1", Source: "dhcp", DhcpDuid: &leaseDuid},
	}
	res := []map[string]any{reservation("", resDuid, "2001:db8::1")}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), rows)
	if err != nil {
		t.Fatal(err)
	}
	if got.SkippedDuidMismatch != 1 {
		t.Errorf("SkippedDuidMismatch = %d", got.SkippedDuidMismatch)
	}
}

func TestSync_UnparseableIPSkipped(t *testing.T) {
	subnetID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "not-an-ip")}
	w := &captureWriter{}
	got, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries[0]["decision"] != "skipped_unparseable" {
		t.Errorf("entry decision = %v", got.Entries[0]["decision"])
	}
	if len(w.inserts) != 0 {
		t.Errorf("unparseable IP must not insert")
	}
}

func TestSync_PromoteParamsCarryReservationFields(t *testing.T) {
	// Backfill semantics: the reservation knows mac+hostname; the
	// lease row's columns are NULL. The promote params should
	// carry both so the SQL's COALESCE backfills.
	subnetID := uuid.New()
	ipID := uuid.New()
	mac := "aa:bb:cc:dd:ee:01"
	rows := []dbq.ListIPAddressesInSubnetForReconcileRow{
		{ID: ipID, Address: "10.0.0.5", Source: "dhcp", DhcpMac: nil},
	}
	res := []map[string]any{{"ip": "10.0.0.5", "mac": mac, "hostname": "host-1"}}
	w := &captureWriter{}
	_, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.promotes) != 1 {
		t.Fatalf("promotes = %d, want 1", len(w.promotes))
	}
	p := w.promotes[0]
	if p.DhcpMac == nil || *p.DhcpMac != mac {
		t.Errorf("promote dhcp_mac = %v, want %q", p.DhcpMac, mac)
	}
	if p.DnsName == nil || *p.DnsName != "host-1" {
		t.Errorf("promote dns_name = %v, want \"host-1\"", p.DnsName)
	}
}

func TestSync_InsertError_AbortsBatch(t *testing.T) {
	subnetID := uuid.New()
	res := []map[string]any{
		reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5"),
		reservation("aa:bb:cc:dd:ee:02", "", "10.0.0.6"),
	}
	w := &captureWriter{insertErr: errSentinel("db down")}
	_, err := Sync(context.Background(), w, uuid.New(), &subnetID, mustJSON(t, res), nil)
	if err == nil {
		t.Fatal("expected err on insert failure")
	}
}

// Pure aggregator helpers re-used: reservation, mustJSON. Sentinel
// error type so the test reads without pulling stdlib errors.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// Round-trip a SyncReport through JSON to pin the wire shape.
func TestSyncReport_WireShapeMatchesPython(t *testing.T) {
	subnetID := uuid.New().String()
	r := SyncReport{
		ScopeID: uuid.New().String(), SubnetID: &subnetID,
		Upserted: 1, Promoted: 0,
		SkippedCollision: 0, SkippedClean: 0,
		SkippedMacMismatch: 0, SkippedDuidMismatch: 0,
		SkippedNoSubnet: 0,
		Entries: []map[string]any{
			{"reservation_ip": "10.0.0.5", "decision": "upserted"},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	// Spot-check fixed-key fields Python emits at api/ipam.py:2358-
	// 2368 — operators paging the audit log filter on these names.
	for _, want := range []string{
		`"upserted":1`,
		`"skipped_mac_mismatch":0`,
		`"skipped_no_subnet":0`,
		`"entries":[`,
	} {
		if !bytesContains(b, want) {
			t.Errorf("wire shape missing %q, got %s", want, string(b))
		}
	}
}

func bytesContains(haystack []byte, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
