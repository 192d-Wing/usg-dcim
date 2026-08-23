package dns

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

type viewsFakeQ struct {
	byFabric map[uuid.UUID][]dbq.DnsView
	err      error
}

func (f *viewsFakeQ) ListDnsViewsByFabric(_ context.Context, fabricID uuid.UUID) ([]dbq.DnsView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byFabric[fabricID], nil
}

func mkSplitZone(name string, fid uuid.UUID) dbq.DnsZone {
	return dbq.DnsZone{
		ID: uuid.New(), Name: name, Kind: "apex", FabricID: fid,
		SoaMname: "ns1", SoaRname: "hostmaster",
		SoaRefresh: 900, SoaRetry: 900, SoaExpire: 1800, SoaMinimum: 60,
		DefaultTTL: 60,
		UpdatedAt:  time.Unix(1700000000, 0).UTC(),
	}
}

// ===== loadSplitHorizonZoneFiles =====

func TestLoadSplitHorizon_NoViewBoundRecordsSkipsZone(t *testing.T) {
	fid := uuid.New()
	z := mkSplitZone("z.example.", fid)
	recs := map[uuid.UUID][]dbq.ListDnsRecordsByZoneIDsRow{
		z.ID: {{Name: "www", Type: "A", Data: []byte(`{"target":"10.0.0.1"}`)}},
	}
	q := &viewsFakeQ{byFabric: map[uuid.UUID][]dbq.DnsView{
		fid: {{ID: uuid.New(), Name: "internal", MatchCidrs: []byte(`["10.0.0.0/8"]`)}},
	}}
	out, err := loadSplitHorizonZoneFiles(context.Background(), q, []dbq.DnsZone{z}, recs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ZoneFiles) != 0 || len(out.ViewsByZone) != 0 {
		t.Errorf("zone with no view-bound records should not produce files; got %+v", out)
	}
}

func TestLoadSplitHorizon_EmitsDefaultAndPerViewFiles(t *testing.T) {
	fid := uuid.New()
	z := mkSplitZone("z.example.", fid)
	viewID := uuid.New()
	recs := map[uuid.UUID][]dbq.ListDnsRecordsByZoneIDsRow{
		z.ID: {
			{Name: "default-only", Type: "A", Data: []byte(`{"target":"10.0.0.1"}`)},
			{Name: "internal-override", Type: "A", Data: []byte(`{"target":"10.0.0.2"}`), ViewID: &viewID},
		},
	}
	q := &viewsFakeQ{byFabric: map[uuid.UUID][]dbq.DnsView{
		fid: {{ID: viewID, Name: "internal", FabricID: fid, MatchCidrs: []byte(`["10.0.0.0/8"]`)}},
	}}
	out, err := loadSplitHorizonZoneFiles(context.Background(), q, []dbq.DnsZone{z}, recs, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Default file: only null-view records.
	defaultFile, ok := out.ZoneFiles["z.example..zone"]
	if !ok {
		t.Fatalf("default file missing; keys=%v", filenames(out.ZoneFiles))
	}
	if !strings.Contains(defaultFile, "10.0.0.1") {
		t.Errorf("default file missing null-view record: %s", defaultFile)
	}
	if strings.Contains(defaultFile, "10.0.0.2") {
		t.Errorf("default file must NOT contain view-bound record: %s", defaultFile)
	}
	// Per-view file: view records + null-view records.
	viewFile, ok := out.ZoneFiles["z.example..view-internal.zone"]
	if !ok {
		t.Fatalf("view file missing; keys=%v", filenames(out.ZoneFiles))
	}
	if !strings.Contains(viewFile, "10.0.0.1") {
		t.Errorf("view file missing null-view (shared) record: %s", viewFile)
	}
	if !strings.Contains(viewFile, "10.0.0.2") {
		t.Errorf("view file missing view-bound record: %s", viewFile)
	}
	// ViewsByZone preserves order + carries CIDRs.
	vcs := out.ViewsByZone["z.example."]
	if len(vcs) != 1 || vcs[0].Name != "internal" || len(vcs[0].CIDRs) != 1 || vcs[0].CIDRs[0] != "10.0.0.0/8" {
		t.Errorf("ViewConfig wrong: %+v", vcs)
	}
}

func TestLoadSplitHorizon_NoViewsForFabricSkipsRender(t *testing.T) {
	fid := uuid.New()
	z := mkSplitZone("z.example.", fid)
	viewID := uuid.New()
	// Record references a view but the fabric has no views configured
	// (Python skips per-view rendering in this case).
	recs := map[uuid.UUID][]dbq.ListDnsRecordsByZoneIDsRow{
		z.ID: {{Name: "x", Type: "A", Data: []byte(`{"target":"10.0.0.1"}`), ViewID: &viewID}},
	}
	q := &viewsFakeQ{byFabric: map[uuid.UUID][]dbq.DnsView{}}
	out, err := loadSplitHorizonZoneFiles(context.Background(), q, []dbq.DnsZone{z}, recs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ZoneFiles) != 0 {
		t.Errorf("no views for fabric → no split files; got %+v", filenames(out.ZoneFiles))
	}
}

func TestLoadSplitHorizon_UnhealthyFilterApplies(t *testing.T) {
	fid := uuid.New()
	z := mkSplitZone("z.example.", fid)
	viewID := uuid.New()
	hcID := uuid.New()
	recs := map[uuid.UUID][]dbq.ListDnsRecordsByZoneIDsRow{
		z.ID: {
			{Name: "ok", Type: "A", Data: []byte(`{"target":"10.0.0.1"}`), ViewID: &viewID},
			{Name: "sick", Type: "A", Data: []byte(`{"target":"10.0.0.2"}`), ViewID: &viewID, HealthCheckID: &hcID},
		},
	}
	q := &viewsFakeQ{byFabric: map[uuid.UUID][]dbq.DnsView{
		fid: {{ID: viewID, Name: "internal", FabricID: fid, MatchCidrs: []byte(`["10.0.0.0/8"]`)}},
	}}
	unhealthy := map[uuid.UUID]struct{}{hcID: {}}
	out, _ := loadSplitHorizonZoneFiles(context.Background(), q, []dbq.DnsZone{z}, recs, unhealthy)
	view := out.ZoneFiles["z.example..view-internal.zone"]
	if !strings.Contains(view, "10.0.0.1") {
		t.Error("healthy view-bound record missing from view file")
	}
	if strings.Contains(view, "10.0.0.2") {
		t.Error("unhealthy view-bound record leaked into view file")
	}
}

// ===== filterRecordsForView =====

func TestFilterRecordsForView_NilKeepsNullViewOnly(t *testing.T) {
	vid := uuid.New()
	recs := []dbq.ListDnsRecordsByZoneIDsRow{
		{Name: "null"},
		{Name: "bound", ViewID: &vid},
	}
	got := filterRecordsForView(recs, nil)
	if len(got) != 1 || got[0].Name != "null" {
		t.Errorf("nil view should keep only null-view records; got %v", got)
	}
}

func TestFilterRecordsForView_ViewIDKeepsViewAndNull(t *testing.T) {
	vid := uuid.New()
	other := uuid.New()
	recs := []dbq.ListDnsRecordsByZoneIDsRow{
		{Name: "null"},
		{Name: "this", ViewID: &vid},
		{Name: "other", ViewID: &other},
	}
	got := filterRecordsForView(recs, &vid)
	if len(got) != 2 {
		t.Fatalf("want 2 records (null + this view); got %v", got)
	}
}

// ===== decodeViewCIDRs =====

func TestDecodeViewCIDRs_ValidJSON(t *testing.T) {
	got := decodeViewCIDRs([]byte(`["10.0.0.0/8", "2001:db8::/32"]`))
	if len(got) != 2 || got[0] != "10.0.0.0/8" || got[1] != "2001:db8::/32" {
		t.Errorf("got %v", got)
	}
}

func TestDecodeViewCIDRs_BadJSONIsEmpty(t *testing.T) {
	got := decodeViewCIDRs([]byte(`not-json`))
	if len(got) != 0 {
		t.Errorf("bad JSON should produce empty CIDRs; got %v", got)
	}
}

func TestDecodeViewCIDRs_EmptyInput(t *testing.T) {
	if got := decodeViewCIDRs(nil); got != nil {
		t.Errorf("empty input should produce nil; got %v", got)
	}
}

// ===== AssembleAuthBundle integration with split-horizon =====

// When a zone has view-bound records, the assembler must skip the
// default render for it (Python's `continue` branch) and instead
// emit the pre-rendered files from SplitHorizonZoneFiles.
func TestAssembleAuthBundle_SplitHorizonZoneSkipsDefaultRender(t *testing.T) {
	z := mkSplitZone("z.example.", uuid.New())
	in := AuthBundleInput{
		Server: dbq.DnsServer{Role: "auth"},
		Zones:  []dbq.DnsZone{z},
		// View-bound records present so the assembler must skip the
		// default render.
		RecordsByZone: map[uuid.UUID][]dbq.ListDnsRecordsByZoneIDsRow{
			z.ID: {{Name: "x", Type: "A", Data: []byte(`{"target":"10.0.0.1"}`)}},
		},
		ViewsByZone: map[string][]ViewConfig{
			"z.example.": {{Name: "internal", CIDRs: []string{"10.0.0.0/8"}}},
		},
		SplitHorizonZoneFiles: map[string]string{
			"z.example..zone":               "pre-rendered default\n",
			"z.example..view-internal.zone": "pre-rendered view\n",
		},
	}
	out, err := AssembleAuthBundle(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Zones["z.example..zone"] != "pre-rendered default\n" {
		t.Errorf("split-horizon default file must come from SplitHorizonZoneFiles, not the default-render path; got %q",
			out.Zones["z.example..zone"])
	}
	if out.Zones["z.example..view-internal.zone"] != "pre-rendered view\n" {
		t.Errorf("split-horizon view file missing: %v", filenames(out.Zones))
	}
	// Corefile must carry one view-scoped server block for the view.
	if !strings.Contains(out.Corefile, "view internal {") {
		t.Errorf("Corefile missing view block: %q", out.Corefile)
	}
}

func filenames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
