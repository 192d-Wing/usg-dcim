package dnssecrotate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/dns"
)

type fakeQ struct {
	zones       []dbq.DnsZone
	activeByZ   map[uuid.UUID][]dbq.DnsKey
	createCalls int
	retireCalls int
	touchCalls  int

	// Capture every role string ListActiveDnsKeysForZoneAndRole and
	// every CreateDnsKey saw — both must always be "zsk" for the
	// cron; a regression to KSK would be catastrophic for DNSSEC.
	rolesSeen        []string
	createAlgorithms []string

	// createErrFor scopes the error to specific zones via the
	// CreateDnsKeyParams.ZoneID — lets one zone fail while another
	// succeeds, proving real per-zone isolation in the loop.
	createErrFor map[uuid.UUID]error
	listZonesErr error
}

func (f *fakeQ) ListSignedZonesWithZskRotation(_ context.Context) ([]dbq.DnsZone, error) {
	if f.listZonesErr != nil {
		return nil, f.listZonesErr
	}
	return f.zones, nil
}
func (f *fakeQ) ListActiveDnsKeysForZoneAndRole(_ context.Context, arg dbq.ListActiveDnsKeysForZoneAndRoleParams) ([]dbq.DnsKey, error) {
	f.rolesSeen = append(f.rolesSeen, arg.Role)
	return f.activeByZ[arg.ZoneID], nil
}
func (f *fakeQ) CreateDnsKey(_ context.Context, a dbq.CreateDnsKeyParams) (dbq.DnsKey, error) {
	f.createCalls++
	f.createAlgorithms = append(f.createAlgorithms, a.Algorithm)
	if a.ZoneID != nil {
		if err, ok := f.createErrFor[*a.ZoneID]; ok {
			return dbq.DnsKey{}, err
		}
	}
	return dbq.DnsKey{ID: uuid.New()}, nil
}
func (f *fakeQ) RetireDnsKey(_ context.Context, _ uuid.UUID) (int64, error) {
	f.retireCalls++
	return 1, nil
}
func (f *fakeQ) TouchDnsZone(_ context.Context, _ uuid.UUID) (int64, error) {
	f.touchCalls++
	return 1, nil
}

func zone(t *testing.T, name string, rotationDays int32, frozen bool) dbq.DnsZone {
	t.Helper()
	siteID := uuid.New()
	return dbq.DnsZone{
		ID: uuid.New(), Name: name, Kind: "site", FabricID: uuid.New(),
		SiteID: &siteID, Signed: true, ZskRotationDays: rotationDays, Frozen: frozen,
	}
}

func TestRun_RotatesDueZones(t *testing.T) {
	nowAt := time.Date(2026, 5, 31, 3, 17, 0, 0, time.UTC)
	z1 := zone(t, "zone1.example.", 30, false) // due
	z2 := zone(t, "zone2.example.", 30, false) // fresh
	q := &fakeQ{
		zones: []dbq.DnsZone{z1, z2},
		activeByZ: map[uuid.UUID][]dbq.DnsKey{
			// z1 uses ed25519 — verifies the algorithm-inheritance path
			// at the cron level. If RotateZoneKey ever stopped reading
			// active[0].Algorithm, this test would catch a silent
			// downgrade to the ECDSAP256 default.
			z1.ID: {{ID: uuid.New(), Algorithm: "ed25519", ActiveFrom: nowAt.Add(-31 * 24 * time.Hour)}},
			z2.ID: {{ID: uuid.New(), Algorithm: "ecdsap256sha256", ActiveFrom: nowAt.Add(-5 * 24 * time.Hour)}},
		},
	}
	j := &Job{Q: q, Now: func() time.Time { return nowAt }}

	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if v, ok := out["checked"].(int); !ok || v != 2 {
		t.Errorf("checked: got %v (ok=%v), want 2", out["checked"], ok)
	}
	if v, ok := out["rotated"].(int); !ok || v != 1 {
		t.Errorf("rotated: got %v (ok=%v), want 1", out["rotated"], ok)
	}
	if q.createCalls != 1 {
		t.Errorf("CreateDnsKey: got %d, want 1 (only z1 was due)", q.createCalls)
	}
	if q.touchCalls != 1 {
		t.Errorf("TouchDnsZone: got %d, want 1", q.touchCalls)
	}
	// Every role string the cron passed to the Querier must be "zsk".
	// A regression that flipped this to "ksk" would compile + pass
	// the other assertions but destroy DNSSEC chains of trust in prod.
	for i, r := range q.rolesSeen {
		if r != "zsk" {
			t.Errorf("rolesSeen[%d]: got %q, want \"zsk\"", i, r)
		}
	}
	// New ZSK inherits z1's existing algorithm (ed25519), not the
	// ECDSAP256 default.
	if len(q.createAlgorithms) != 1 || q.createAlgorithms[0] != "ed25519" {
		t.Errorf("createAlgorithms: got %v, want [\"ed25519\"]", q.createAlgorithms)
	}
}

func TestRun_PerZoneIsolation_OneFailsOneSucceeds(t *testing.T) {
	// Verifies the package-doc invariant: one bad zone doesn't block
	// rotations on the rest of the fleet. Earlier version of this
	// test used a global createErr toggle, so BOTH zones failed and
	// only loop-continuation was proven (not isolation).
	nowAt := time.Date(2026, 5, 31, 3, 17, 0, 0, time.UTC)
	bad := zone(t, "fails.example.", 30, false)
	good := zone(t, "succeeds.example.", 30, false)
	old := nowAt.Add(-100 * 24 * time.Hour)
	q := &fakeQ{
		zones: []dbq.DnsZone{bad, good},
		activeByZ: map[uuid.UUID][]dbq.DnsKey{
			bad.ID:  {{ID: uuid.New(), Algorithm: "ecdsap256sha256", ActiveFrom: old}},
			good.ID: {{ID: uuid.New(), Algorithm: "ecdsap256sha256", ActiveFrom: old}},
		},
		createErrFor: map[uuid.UUID]error{bad.ID: errors.New("simulated bad-zone db error")},
	}
	j := &Job{Q: q, Now: func() time.Time { return nowAt }}

	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run should not propagate per-zone errors; got %v", err)
	}
	if v, _ := out["rotated"].(int); v != 1 {
		t.Errorf("rotated: got %v, want 1 (only good rotated)", v)
	}
	if q.createCalls != 2 {
		t.Errorf("CreateDnsKey: got %d, want 2 (both attempted)", q.createCalls)
	}
	// good's retire ran (it produced 1 retired key); bad's didn't
	// (its create errored before reaching retire).
	if q.retireCalls != 1 {
		t.Errorf("RetireDnsKey: got %d, want 1 (only good zone reached retire step)", q.retireCalls)
	}
}

func TestRotateZoneKey_RejectsUnsignedZone(t *testing.T) {
	// Defense-in-depth guard added when RotateZoneKey was exported.
	// Both call sites pre-check, but a future third caller might
	// forget — confirm the helper itself refuses.
	z := zone(t, "unsigned.example.", 30, false)
	z.Signed = false
	q := &fakeQ{}
	if _, err := dns.RotateZoneKey(context.Background(), q, z, "zsk"); !errors.Is(err, dns.ErrZoneNotSigned) {
		t.Errorf("expected ErrZoneNotSigned, got %v", err)
	}
	if q.createCalls != 0 {
		t.Errorf("CreateDnsKey called on unsigned zone; want 0, got %d", q.createCalls)
	}
}

func TestRotateZoneKey_RejectsFrozenZone(t *testing.T) {
	z := zone(t, "frozen-helper.example.", 30, true) // frozen
	q := &fakeQ{}
	if _, err := dns.RotateZoneKey(context.Background(), q, z, "zsk"); !errors.Is(err, dns.ErrZoneFrozen) {
		t.Errorf("expected ErrZoneFrozen, got %v", err)
	}
	if q.createCalls != 0 {
		t.Errorf("CreateDnsKey called on frozen zone; want 0, got %d", q.createCalls)
	}
}

func TestRun_SkipsFrozenZones(t *testing.T) {
	frozen := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	z := zone(t, "frozen.example.", 30, true)
	q := &fakeQ{
		zones: []dbq.DnsZone{z},
		activeByZ: map[uuid.UUID][]dbq.DnsKey{
			z.ID: {{ID: uuid.New(), Algorithm: "ecdsap256sha256", ActiveFrom: frozen.Add(-100 * 24 * time.Hour)}},
		},
	}
	j := &Job{Q: q, Now: func() time.Time { return frozen }}

	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if v, _ := out["checked"].(int); v != 1 {
		t.Errorf("checked: got %v, want 1 (frozen still counts as checked)", v)
	}
	if v, _ := out["rotated"].(int); v != 0 {
		t.Errorf("rotated: got %v, want 0 (frozen skipped)", v)
	}
	if q.createCalls != 0 {
		t.Errorf("CreateDnsKey called %d times on frozen zone; want 0", q.createCalls)
	}
}

func TestRun_SkipsZonesWithoutActiveKey(t *testing.T) {
	frozen := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	z := zone(t, "noactive.example.", 30, false)
	q := &fakeQ{
		zones: []dbq.DnsZone{z},
		// activeByZ entry intentionally missing — returns empty slice
	}
	j := &Job{Q: q, Now: func() time.Time { return frozen }}

	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if v, _ := out["rotated"].(int); v != 0 {
		t.Errorf("rotated: got %v, want 0 (no active ZSK = skip)", v)
	}
	if q.createCalls != 0 {
		t.Errorf("CreateDnsKey called on zone with no active key; want 0, got %d", q.createCalls)
	}
}

func TestRun_AllZonesFail_LoopContinues(t *testing.T) {
	nowAt := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	z1 := zone(t, "fails-1.example.", 30, false)
	z2 := zone(t, "fails-2.example.", 30, false)
	old := nowAt.Add(-100 * 24 * time.Hour)
	q := &fakeQ{
		zones: []dbq.DnsZone{z1, z2},
		activeByZ: map[uuid.UUID][]dbq.DnsKey{
			z1.ID: {{ID: uuid.New(), Algorithm: "ecdsap256sha256", ActiveFrom: old}},
			z2.ID: {{ID: uuid.New(), Algorithm: "ecdsap256sha256", ActiveFrom: old}},
		},
		createErrFor: map[uuid.UUID]error{
			z1.ID: errors.New("z1 db error"),
			z2.ID: errors.New("z2 db error"),
		},
	}
	j := &Job{Q: q, Now: func() time.Time { return nowAt }}

	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run should not propagate per-zone errors; got %v", err)
	}
	if v, _ := out["rotated"].(int); v != 0 {
		t.Errorf("rotated: got %v, want 0 (both zones failed)", v)
	}
	if q.createCalls != 2 {
		t.Errorf("CreateDnsKey: got %d, want 2 (loop should continue past failure)", q.createCalls)
	}
	if q.touchCalls != 0 {
		t.Errorf("TouchDnsZone: got %d, want 0 (no rotation should reach the touch step on failure)", q.touchCalls)
	}
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_ListZonesError_Wrapped(t *testing.T) {
	q := &fakeQ{listZonesErr: errors.New("db gone")}
	j := &Job{Q: q}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestName_Matches(t *testing.T) {
	j := &Job{}
	if j.Name() != Name {
		t.Errorf("Name(): got %q, want %q", j.Name(), Name)
	}
	if Name != "dns_rotate_zsks" {
		t.Errorf("package-level Name constant changed unexpectedly: %q", Name)
	}
}
