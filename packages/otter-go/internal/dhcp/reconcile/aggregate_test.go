// Unit tests for the pure reservation ↔ IPAddress reconciler. The
// HTTP wrapper is tested separately in internal/ipam; these cover
// the classification logic, the helper normalizers, and the counts.
package reconcile

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

func reservation(mac, duid, ip string) map[string]any {
	m := map[string]any{"ip": ip}
	if mac != "" {
		m["mac"] = mac
	}
	if duid != "" {
		m["duid"] = duid
	}
	return m
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReconcile_NoSubnet_AllUnbacked(t *testing.T) {
	scopeID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5")}
	got := Reconcile(scopeID, nil, mustJSON(t, res), nil)
	if got.SubnetID != nil {
		t.Errorf("SubnetID = %v, want nil", got.SubnetID)
	}
	if got.Total != 1 || got.Counts[string(StatusUnbacked)] != 1 {
		t.Errorf("counts = %+v, want unbacked=1", got.Counts)
	}
	if got.Entries[0].Note == nil || *got.Entries[0].Note != "scope has no subnet_id — IPAM cross-check skipped" {
		t.Errorf("note = %v", got.Entries[0].Note)
	}
}

func TestReconcile_NoReservations_EmptyReport(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	got := Reconcile(scopeID, &subnetID, nil, nil)
	if got.Total != 0 || len(got.Entries) != 0 {
		t.Errorf("Total/Entries = %d/%d", got.Total, len(got.Entries))
	}
	// Counts pre-fills with every status at 0 even on empty input
	// so dashboards reading counts.clean see a value, not undefined.
	for _, s := range statusList {
		if got.Counts[string(s)] != 0 {
			t.Errorf("counts[%s] = %d, want 0", s, got.Counts[string(s)])
		}
	}
}

func TestReconcile_MatchingReservationSource_Clean(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	ipID := uuid.New()
	mac := "aa:bb:cc:dd:ee:01"
	res := []map[string]any{reservation(mac, "", "10.0.0.5")}
	rows := []dbq.DhcpReconcileIPRow{
		{ID: ipID, Address: "10.0.0.5/24", Source: "reservation", DhcpMac: &mac},
	}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusClean)] != 1 {
		t.Errorf("clean = %d, want 1", got.Counts[string(StatusClean)])
	}
	if got.Entries[0].IPAddressID == nil || *got.Entries[0].IPAddressID != ipID.String() {
		t.Errorf("ip_address_id = %v, want %s", got.Entries[0].IPAddressID, ipID)
	}
}

func TestReconcile_StaticSource_Collision(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5")}
	rows := []dbq.DhcpReconcileIPRow{{ID: uuid.New(), Address: "10.0.0.5", Source: "static"}}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusCollision)] != 1 {
		t.Errorf("collision = %d, want 1", got.Counts[string(StatusCollision)])
	}
	if got.Entries[0].IPSource == nil || *got.Entries[0].IPSource != "static" {
		t.Errorf("ip_source = %v, want static", got.Entries[0].IPSource)
	}
}

func TestReconcile_NoMatchingIP_Unbacked(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.99")}
	rows := []dbq.DhcpReconcileIPRow{
		{ID: uuid.New(), Address: "10.0.0.5", Source: "reservation"},
	}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusUnbacked)] != 1 {
		t.Errorf("unbacked = %d, want 1", got.Counts[string(StatusUnbacked)])
	}
}

func TestReconcile_MacMismatch(t *testing.T) {
	// PR 88: reservation declares one MAC, the lease's dhcp_mac
	// holds another. Both sides non-nil → mismatch fires.
	scopeID := uuid.New()
	subnetID := uuid.New()
	leaseMac := "11:22:33:44:55:66"
	rows := []dbq.DhcpReconcileIPRow{
		{ID: uuid.New(), Address: "10.0.0.5", Source: "dhcp", DhcpMac: &leaseMac},
	}
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5")}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusMacMismatch)] != 1 {
		t.Errorf("mac_mismatch = %d, want 1", got.Counts[string(StatusMacMismatch)])
	}
}

func TestReconcile_DuidMismatch(t *testing.T) {
	// PR 94: parallel of MAC for v6. Separate bucket so operators
	// can distinguish v4 mac drift from v6 duid drift.
	scopeID := uuid.New()
	subnetID := uuid.New()
	leaseDuid := "00:01:00:01:abcd:ef00:0001"
	rows := []dbq.DhcpReconcileIPRow{
		{ID: uuid.New(), Address: "2001:db8::1", Source: "dhcp", DhcpDuid: &leaseDuid},
	}
	res := []map[string]any{reservation("", "00:01:00:01:dead:beef:0001", "2001:db8::1")}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusDuidMismatch)] != 1 {
		t.Errorf("duid_mismatch = %d, want 1", got.Counts[string(StatusDuidMismatch)])
	}
}

func TestReconcile_MissingBindingSideSkipsCheck(t *testing.T) {
	// PR 88 default: either side nil = skip the binding check (no
	// false alarm). dhcp_mac=nil on the row → clean, not mismatch.
	scopeID := uuid.New()
	subnetID := uuid.New()
	rows := []dbq.DhcpReconcileIPRow{
		{ID: uuid.New(), Address: "10.0.0.5", Source: "dhcp", DhcpMac: nil},
	}
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "10.0.0.5")}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusClean)] != 1 {
		t.Errorf("counts = %+v, want clean=1 (nil dhcp_mac must not false-alarm)", got.Counts)
	}
}

func TestReconcile_UnparseableIP_Unbacked(t *testing.T) {
	scopeID := uuid.New()
	subnetID := uuid.New()
	res := []map[string]any{reservation("aa:bb:cc:dd:ee:01", "", "not-an-ip")}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), nil)
	if got.Counts[string(StatusUnbacked)] != 1 {
		t.Errorf("unbacked = %d, want 1", got.Counts[string(StatusUnbacked)])
	}
	if got.Entries[0].Note == nil || *got.Entries[0].Note != "reservation IP is not parseable" {
		t.Errorf("note = %v", got.Entries[0].Note)
	}
}

func TestReconcile_IPCanonicalization(t *testing.T) {
	// Postgres inet returns "10.0.0.05" reduced to "10.0.0.5" via
	// the inet → text cast already; this guards against a future
	// regression that drops the netip.ParseAddr canonicalization
	// step. "2001:db8::0001" → "2001:db8::1".
	scopeID := uuid.New()
	subnetID := uuid.New()
	rows := []dbq.DhcpReconcileIPRow{
		{ID: uuid.New(), Address: "2001:db8::1", Source: "reservation"},
	}
	res := []map[string]any{reservation("", "00:01:abcd", "2001:db8::0001")}
	got := Reconcile(scopeID, &subnetID, mustJSON(t, res), rows)
	if got.Counts[string(StatusClean)] != 1 {
		t.Errorf("counts = %+v, want clean=1 (canonicalized IP must match)", got.Counts)
	}
}

// ---- normalizer unit tests ----

func TestNormalizeMac(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"AA:BB:CC:DD:EE:01", "aa:bb:cc:dd:ee:01"},
		{"aa-bb-cc-dd-ee-01", "aa:bb:cc:dd:ee:01"},
		{"aabb.ccdd.ee01", "aa:bb:cc:dd:ee:01"},
		{"aabbccddee01", "aa:bb:cc:dd:ee:01"},
		{"too-short", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeMac(c.in); got != c.want {
			t.Errorf("normalizeMac(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeDuid(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"00:01:00:01", "00:01:00:01"},
		{"0001-0001", "00:01:00:01"},
		{"00010001", "00:01:00:01"},
		{"AB", "ab"},
		{"A", ""},     // odd hex count
		{"", ""},
		// Non-hex chars are silently dropped (Python parity at
		// services/dhcp_reconcile.py:109); zz is stripped, leaving
		// aabbccddee01 which is a valid 12-hex DUID.
		{"aabbccddee01zz", "aa:bb:cc:dd:ee:01"},
	}
	for _, c := range cases {
		if got := normalizeDuid(c.in); got != c.want {
			t.Errorf("normalizeDuid(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
