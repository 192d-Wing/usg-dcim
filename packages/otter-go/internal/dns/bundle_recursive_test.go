package dns

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// ===== AssembleRecursiveBundle =====

func TestAssembleRecursive_CoreDNSPathHasNoZoneFiles(t *testing.T) {
	out := AssembleRecursiveBundle(RecursiveBundleInput{
		Engine: "coredns",
		Now:    time.Unix(1700000000, 0).UTC(),
		Blocklists: []Blocklist{
			{Action: "block", Patterns: []string{"evil.example"}},
		},
	})
	if out.Engine != "coredns" {
		t.Errorf("engine: got %q", out.Engine)
	}
	if len(out.Zones) != 0 {
		t.Errorf("CoreDNS path emits blocklists inline; expected zero zone files, got %v", out.Zones)
	}
	if !strings.Contains(out.Corefile, "template ANY ANY") {
		t.Errorf("blocklist template missing from Corefile; got %q", out.Corefile)
	}
	if out.Etag == "" || len(out.Etag) != 32 {
		t.Errorf("etag length: got %d, want 32 (%q)", len(out.Etag), out.Etag)
	}
}

func TestAssembleRecursive_HickoryPathRendersRpzZoneFiles(t *testing.T) {
	out := AssembleRecursiveBundle(RecursiveBundleInput{
		Engine: "hickory",
		Now:    time.Unix(1700000000, 0).UTC(),
		Blocklists: []Blocklist{
			{Action: "block", Patterns: []string{"evil.example"}},
		},
	})
	if out.Engine != "hickory" {
		t.Errorf("engine: got %q", out.Engine)
	}
	if len(out.Zones) != 1 {
		t.Fatalf("expected 1 RPZ zone file; got %d (%v)", len(out.Zones), out.Zones)
	}
	if _, ok := out.Zones["bl-000.rpz.dcim.local.zone"]; !ok {
		t.Errorf("missing predictably-named RPZ zone file; got %v", out.Zones)
	}
	if !strings.Contains(out.Corefile, "[[response_policy]]") {
		t.Errorf("Hickory response_policy block missing; got %q", out.Corefile)
	}
}

// PR 36 dropped the gobgp wire field entirely. The recursive
// bundle no longer surfaces anycast_prefixes either — the agent's
// RIB-reconcile loop is gone (PR #257; Cilium BGP owns the session
// at the cluster level). Pin those two negatives so a future
// re-introduction of either field on the recursive path fails.
func TestAssembleRecursive_AnycastNil(t *testing.T) {
	out := AssembleRecursiveBundle(RecursiveBundleInput{
		Engine: "coredns", Now: time.Unix(1700000000, 0).UTC(),
	})
	if out.AnycastPrefixes != nil {
		t.Errorf("anycast_prefixes must be nil on recursive bundles; got %v", out.AnycastPrefixes)
	}
}

func TestAssembleRecursive_DefaultEngineIsCoreDNS(t *testing.T) {
	out := AssembleRecursiveBundle(RecursiveBundleInput{Now: time.Unix(1700000000, 0).UTC()})
	if out.Engine != "coredns" {
		t.Errorf("empty engine should default to coredns; got %q", out.Engine)
	}
}

func TestAssembleRecursive_DeterministicEtag(t *testing.T) {
	in := RecursiveBundleInput{
		Engine: "coredns", Now: time.Unix(1700000000, 0).UTC(),
		UpstreamResolvers: []string{"9.9.9.9"},
	}
	a := AssembleRecursiveBundle(in)
	b := AssembleRecursiveBundle(in)
	if a.Etag != b.Etag {
		t.Errorf("etag flapped: %s vs %s", a.Etag, b.Etag)
	}
}

// ===== loadRecursiveBundleInput =====

type recursiveLoaderFakeQ struct {
	fabric     dbq.FabricForRecursiveBundle
	fabricErr  error
	apexes     []string
	authIP     string
	authIPErr  error
	fwd        []dbq.DnsForwarderRow
	bl         []dbq.BlocklistForBundleRow
	systemRow  dbq.SystemSetting
	systemErr  error
}

func (f *recursiveLoaderFakeQ) GetFabricForRecursiveBundle(_ context.Context, _ uuid.UUID) (dbq.FabricForRecursiveBundle, error) {
	return f.fabric, f.fabricErr
}
func (f *recursiveLoaderFakeQ) ListApexZoneNamesByFabric(_ context.Context, _ uuid.UUID) ([]string, error) {
	return f.apexes, nil
}
func (f *recursiveLoaderFakeQ) GetSameSiteAuthUnicastIP(_ context.Context, _ uuid.UUID) (string, error) {
	if f.authIPErr != nil {
		return "", f.authIPErr
	}
	return f.authIP, nil
}
func (f *recursiveLoaderFakeQ) ListDnsForwardersForBundle(_ context.Context, _ uuid.UUID) ([]dbq.DnsForwarderRow, error) {
	return f.fwd, nil
}
func (f *recursiveLoaderFakeQ) ListEnabledBlocklistsWithPatternsByFabric(_ context.Context, _ uuid.UUID) ([]dbq.BlocklistForBundleRow, error) {
	return f.bl, nil
}
func (f *recursiveLoaderFakeQ) GetSystemSetting(_ context.Context, _ string) (dbq.SystemSetting, error) {
	if f.systemErr != nil {
		return dbq.SystemSetting{}, f.systemErr
	}
	return f.systemRow, nil
}

// SiteID populated → loader queries the local auth IP.
func TestLoadRecursiveBundleInput_AuthIPThreaded(t *testing.T) {
	q := &recursiveLoaderFakeQ{
		fabric:    dbq.FabricForRecursiveBundle{RecursiveEngine: "coredns"},
		authIP:    "10.0.0.1",
		systemErr: pgx.ErrNoRows,
	}
	in, err := loadRecursiveBundleInput(context.Background(), q,
		dbq.DnsServer{Role: "recursive", FabricID: uuid.New(), SiteID: uuid.New()},
		RecursiveBundleConfig{}, time.Unix(1700000000, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if in.AuthUnicastIP == nil || *in.AuthUnicastIP != "10.0.0.1" {
		t.Errorf("auth IP not threaded; got %v", in.AuthUnicastIP)
	}
}

// Server has no site → local auth lookup is skipped.
func TestLoadRecursiveBundleInput_NoSiteSkipsAuthLookup(t *testing.T) {
	q := &recursiveLoaderFakeQ{
		fabric:    dbq.FabricForRecursiveBundle{RecursiveEngine: "coredns"},
		systemErr: pgx.ErrNoRows,
	}
	in, err := loadRecursiveBundleInput(context.Background(), q,
		dbq.DnsServer{Role: "recursive", FabricID: uuid.New()}, // SiteID zero
		RecursiveBundleConfig{}, time.Unix(1700000000, 0).UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if in.AuthUnicastIP != nil {
		t.Errorf("auth IP should be nil for site-less servers; got %v", in.AuthUnicastIP)
	}
}

// Auth lookup ErrNoRows is benign (the recursive can still serve;
// it just won't forward the apex back).
func TestLoadRecursiveBundleInput_AuthLookupNoRowsBenign(t *testing.T) {
	q := &recursiveLoaderFakeQ{
		fabric:    dbq.FabricForRecursiveBundle{RecursiveEngine: "coredns"},
		authIPErr: pgx.ErrNoRows,
		systemErr: pgx.ErrNoRows,
	}
	in, err := loadRecursiveBundleInput(context.Background(), q,
		dbq.DnsServer{Role: "recursive", FabricID: uuid.New(), SiteID: uuid.New()},
		RecursiveBundleConfig{}, time.Unix(1700000000, 0).UTC(),
	)
	if err != nil {
		t.Errorf("ErrNoRows on auth lookup should not propagate; got %v", err)
	}
	if in.AuthUnicastIP != nil {
		t.Errorf("auth IP should be nil when lookup returned no rows; got %v", in.AuthUnicastIP)
	}
}

// Engine selection comes from the fabric column.
func TestLoadRecursiveBundleInput_EngineFromFabric(t *testing.T) {
	q := &recursiveLoaderFakeQ{
		fabric:    dbq.FabricForRecursiveBundle{RecursiveEngine: "hickory"},
		systemErr: pgx.ErrNoRows,
	}
	in, _ := loadRecursiveBundleInput(context.Background(), q,
		dbq.DnsServer{Role: "recursive", FabricID: uuid.New()},
		RecursiveBundleConfig{}, time.Unix(1700000000, 0).UTC(),
	)
	if in.Engine != "hickory" {
		t.Errorf("engine: got %q want hickory", in.Engine)
	}
}

// Empty engine column → coredns default.
func TestLoadRecursiveBundleInput_EngineEmptyDefaultsCoreDNS(t *testing.T) {
	q := &recursiveLoaderFakeQ{
		fabric:    dbq.FabricForRecursiveBundle{},
		systemErr: pgx.ErrNoRows,
	}
	in, _ := loadRecursiveBundleInput(context.Background(), q,
		dbq.DnsServer{Role: "recursive", FabricID: uuid.New()},
		RecursiveBundleConfig{}, time.Unix(1700000000, 0).UTC(),
	)
	if in.Engine != "coredns" {
		t.Errorf("engine: got %q want coredns", in.Engine)
	}
}

// ===== resolveRecursiveUpstreams =====

func TestResolveUpstreams_FabricOverrideWins(t *testing.T) {
	q := &recursiveLoaderFakeQ{}
	got, err := resolveRecursiveUpstreams(context.Background(), q,
		dbq.FabricForRecursiveBundle{DnsRecursiveUpstreams: []byte(`["1.2.3.4"]`)},
		[]string{"99.99.99.99"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "1.2.3.4" {
		t.Errorf("fabric override should win; got %v", got)
	}
}

func TestResolveUpstreams_SystemSettingBeatsDefault(t *testing.T) {
	q := &recursiveLoaderFakeQ{
		systemRow: dbq.SystemSetting{Value: []byte(`["8.8.8.8"]`)},
	}
	got, err := resolveRecursiveUpstreams(context.Background(), q,
		dbq.FabricForRecursiveBundle{},
		[]string{"99.99.99.99"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "8.8.8.8" {
		t.Errorf("system setting should beat caller default; got %v", got)
	}
}

func TestResolveUpstreams_DefaultsOnNoOverride(t *testing.T) {
	q := &recursiveLoaderFakeQ{systemErr: pgx.ErrNoRows}
	got, err := resolveRecursiveUpstreams(context.Background(), q,
		dbq.FabricForRecursiveBundle{},
		[]string{"99.99.99.99"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "99.99.99.99" {
		t.Errorf("caller default should win when neither override is set; got %v", got)
	}
}

// ===== recursiveEngineKnown =====

func TestRecursiveEngineKnown(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"", true},
		{"coredns", true},
		{"hickory", true},
		{"CoreDNS", true}, // case-insensitive
		{"unbound", false},
		{"bind", false},
	} {
		if got := recursiveEngineKnown(c.in); got != c.want {
			t.Errorf("recursiveEngineKnown(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
