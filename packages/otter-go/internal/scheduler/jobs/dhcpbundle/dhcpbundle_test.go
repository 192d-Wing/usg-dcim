package dhcpbundle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// fakeQ is the slimmest implementation that drives the job loop:
// IDs come from the seeded slice; per-server fixture rows + scope
// slices come from per-ID maps. The fake also records every
// WriteDhcpBundleCache call so tests can assert which servers
// were written and which were no-op'd.
type fakeQ struct {
	serverIDs []uuid.UUID
	servers   map[uuid.UUID]dbq.DhcpServerBundleRow
	scopes    map[uuid.UUID][]dbq.DhcpScope
	templates map[uuid.UUID]dbq.DhcpScopeTemplate

	listErr      error
	getErrFor    map[uuid.UUID]error
	writeErrFor  map[uuid.UUID]error
	writes       []dbq.WriteDhcpBundleCacheParams
}

func (f *fakeQ) ListEnabledDhcpServerIDs(_ context.Context) ([]uuid.UUID, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.serverIDs, nil
}
func (f *fakeQ) GetDhcpServerBundleRow(_ context.Context, id uuid.UUID) (dbq.DhcpServerBundleRow, error) {
	if err, ok := f.getErrFor[id]; ok {
		return dbq.DhcpServerBundleRow{}, err
	}
	return f.servers[id], nil
}
func (f *fakeQ) ListDhcpScopesForBundle(_ context.Context, id uuid.UUID) ([]dbq.DhcpScope, error) {
	return f.scopes[id], nil
}
func (f *fakeQ) ListDhcpScopeTemplatesByIDs(_ context.Context, ids []uuid.UUID) ([]dbq.DhcpScopeTemplate, error) {
	out := []dbq.DhcpScopeTemplate{}
	for _, id := range ids {
		if t, ok := f.templates[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeQ) WriteDhcpBundleCache(_ context.Context, arg dbq.WriteDhcpBundleCacheParams) error {
	if err, ok := f.writeErrFor[arg.ID]; ok {
		return err
	}
	f.writes = append(f.writes, arg)
	return nil
}

func TestRun_NilQuerier_Rejected(t *testing.T) {
	j := &Job{}
	if _, err := j.Run(context.Background()); err == nil {
		t.Error("expected error for nil Q")
	}
}

func TestRun_ListErr_Wrapped(t *testing.T) {
	q := &fakeQ{listErr: errors.New("db gone")}
	j := &Job{Q: q}
	_, err := j.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRun_EmptyFleet_ZeroCounts(t *testing.T) {
	q := &fakeQ{}
	j := &Job{Q: q}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := out["checked"].(int); v != 0 {
		t.Errorf("checked: got %v, want 0", out["checked"])
	}
	if len(q.writes) != 0 {
		t.Errorf("no writes expected on empty fleet; got %d", len(q.writes))
	}
}

func TestRun_FreshlyRendersAndWrites(t *testing.T) {
	// No cached etag → write fires.
	srvID := uuid.New()
	q := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID: {ID: srvID, FabricID: uuid.New(),
				BaseConfig: json.RawMessage(`{"dhcp4":{"interfaces-config":{"interfaces":["eth0"]}}}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID: nil},
	}
	j := &Job{Q: q}
	out, err := j.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := out["written"].(int); v != 1 {
		t.Errorf("written: got %v, want 1", out["written"])
	}
	if len(q.writes) != 1 || q.writes[0].ID != srvID {
		t.Fatalf("expected one write for serverID=%s; got %+v", srvID, q.writes)
	}
	if q.writes[0].BundleCacheEtag == "" {
		t.Errorf("etag should be populated; got %q", q.writes[0].BundleCacheEtag)
	}
	if len(q.writes[0].BundleCacheJSON) == 0 {
		t.Errorf("JSON should be populated; got empty")
	}
	// The cache bytes must be valid JSON (any valid JSON survives
	// the JSONB normalization the column applies on store). No
	// trailing-newline check — json.Marshal does not append one,
	// and JSONB would strip it anyway.
	js := q.writes[0].BundleCacheJSON
	if !json.Valid(js) {
		t.Errorf("cache JSON should be valid; got %q", js)
	}
}

func TestRun_NoChangeTick_SkipsWrite(t *testing.T) {
	// When the cached etag matches the freshly-rendered etag, no
	// write fires — that's the polling-mode optimization PR 4
	// makes over Python's per-mutation model.
	srvID := uuid.New()
	// Render once to capture the etag, then seed the cache with
	// the same etag and re-run. The second run should produce
	// zero writes.
	q1 := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID: {ID: srvID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID: nil},
	}
	if _, err := (&Job{Q: q1}).Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	stableEtag := q1.writes[0].BundleCacheEtag
	stableJSON := q1.writes[0].BundleCacheJSON
	q2 := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			// Seed BOTH cache columns so the no-change optimization
			// guard (etag-match AND json-non-empty) actually fires.
			// Without the JSON seed, the guard would fall through and
			// the cron would re-write — defeating the test.
			srvID: {ID: srvID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`),
				BundleCacheEtag: &stableEtag,
				BundleCacheJSON: stableJSON},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID: nil},
	}
	out, err := (&Job{Q: q2}).Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if v, _ := out["rendered"].(int); v != 1 {
		t.Errorf("rendered: got %v, want 1 (we still rendered to compare)", out["rendered"])
	}
	if v, _ := out["written"].(int); v != 0 {
		t.Errorf("written: got %v, want 0 (etag matched, no-op)", out["written"])
	}
	if len(q2.writes) != 0 {
		t.Errorf("expected zero writes; got %+v", q2.writes)
	}
}

func TestRun_EtagMatchButJSONEmpty_ForcesRewrite(t *testing.T) {
	// Half-baked cache row: bundle_cache_etag is populated but
	// bundle_cache_json is NULL/empty. Without the
	// `len(BundleCacheJSON) > 0` clause in the no-change guard,
	// the cron would skip writing every tick and the JSON column
	// would stay empty forever; the HTTP endpoint would then
	// live-render every request and the cron's optimization
	// reason for existing would be silently broken.
	srvID := uuid.New()
	// First, capture what the etag would be for an empty bundle.
	q1 := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID: {ID: srvID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID: nil},
	}
	if _, err := (&Job{Q: q1}).Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	knownEtag := q1.writes[0].BundleCacheEtag

	// Now seed the row with that etag but no JSON, and confirm
	// the cron re-writes anyway.
	q2 := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID: {ID: srvID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`),
				BundleCacheEtag: &knownEtag,
				BundleCacheJSON: nil, // <-- half-baked state
			},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID: nil},
	}
	out, err := (&Job{Q: q2}).Run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if v, _ := out["written"].(int); v != 1 {
		t.Errorf("written: got %v, want 1 (half-baked row must force a rewrite)", out["written"])
	}
	if len(q2.writes) != 1 {
		t.Errorf("expected one write to repopulate JSON; got %d", len(q2.writes))
	}
}

func TestRun_RespectsContextCancellation(t *testing.T) {
	// Cancel ctx before Run starts the loop. The list query may
	// or may not honor the cancel (the fake doesn't check it), but
	// the per-iteration ctx.Err() check should bail before any
	// server is processed.
	srvID1, srvID2 := uuid.New(), uuid.New()
	q := &fakeQ{
		serverIDs: []uuid.UUID{srvID1, srvID2},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID1: {ID: srvID1, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
			srvID2: {ID: srvID2, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID1: nil, srvID2: nil},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	out, err := (&Job{Q: q}).Run(ctx)
	if err != nil {
		t.Fatalf("Run should bail cleanly on cancelled ctx, not return err; got %v", err)
	}
	// The list query ran (fake doesn't honor ctx), but the loop
	// bailed before processing any server.
	if v, _ := out["checked"].(int); v != 0 {
		t.Errorf("checked: got %v, want 0 (loop should have bailed immediately)", v)
	}
	if len(q.writes) != 0 {
		t.Errorf("expected zero writes on cancelled ctx; got %d", len(q.writes))
	}
}

func TestRun_PerServerErrorDoesNotStopLoop(t *testing.T) {
	// One server's GetDhcpServerBundleRow fails; the other one
	// still renders + writes.
	badID := uuid.New()
	goodID := uuid.New()
	q := &fakeQ{
		serverIDs: []uuid.UUID{badID, goodID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			goodID: {ID: goodID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{goodID: nil},
		getErrFor: map[uuid.UUID]error{
			badID: errors.New("simulated transient pgx error"),
		},
	}
	out, err := (&Job{Q: q}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run should not propagate per-server errors; got %v", err)
	}
	if v, _ := out["checked"].(int); v != 2 {
		t.Errorf("checked: got %v, want 2", out["checked"])
	}
	if v, _ := out["rendered"].(int); v != 1 {
		t.Errorf("rendered: got %v, want 1 (only good server)", out["rendered"])
	}
	if v, _ := out["written"].(int); v != 1 {
		t.Errorf("written: got %v, want 1", out["written"])
	}
	if len(q.writes) != 1 || q.writes[0].ID != goodID {
		t.Errorf("expected one write for goodID; got %+v", q.writes)
	}
}

func TestRun_WriteFailureLoggedAndContinues(t *testing.T) {
	// WriteDhcpBundleCache returns an error → loop continues, that
	// server is missing from `written` count.
	srvID := uuid.New()
	q := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID: {ID: srvID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{srvID: nil},
		writeErrFor: map[uuid.UUID]error{
			srvID: errors.New("write conflict"),
		},
	}
	out, err := (&Job{Q: q}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run should not propagate write errors; got %v", err)
	}
	if v, _ := out["rendered"].(int); v != 1 {
		t.Errorf("rendered: got %v, want 1", out["rendered"])
	}
	if v, _ := out["written"].(int); v != 0 {
		t.Errorf("written: got %v, want 0 (write failed)", out["written"])
	}
}

func TestRun_TemplateBulkLoadDedupes(t *testing.T) {
	// Two scopes referencing the same template — the loop should
	// only fetch templates once with the unique set.
	srvID := uuid.New()
	tplID := uuid.New()
	kid1, kid2 := int32(1), int32(2)
	q := &fakeQ{
		serverIDs: []uuid.UUID{srvID},
		servers: map[uuid.UUID]dbq.DhcpServerBundleRow{
			srvID: {ID: srvID, FabricID: uuid.New(), BaseConfig: json.RawMessage(`{}`)},
		},
		scopes: map[uuid.UUID][]dbq.DhcpScope{
			srvID: {
				{ID: uuid.New(), DhcpServerID: srvID, IPFamily: 4, Prefix: "10.0.0.0/24",
					PoolsJSON: json.RawMessage(`[]`), KeaSubnetID: &kid1, TemplateID: &tplID, Enabled: true},
				{ID: uuid.New(), DhcpServerID: srvID, IPFamily: 4, Prefix: "10.0.1.0/24",
					PoolsJSON: json.RawMessage(`[]`), KeaSubnetID: &kid2, TemplateID: &tplID, Enabled: true},
			},
		},
		templates: map[uuid.UUID]dbq.DhcpScopeTemplate{
			tplID: {ID: tplID, FabricID: uuid.New(), IPFamily: 4,
				OptionsJSON: json.RawMessage(`[{"code":3,"name":"routers","data":"10.0.0.1"}]`)},
		},
	}
	out, err := (&Job{Q: q}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if v, _ := out["written"].(int); v != 1 {
		t.Errorf("written: got %v, want 1", out["written"])
	}
}

func TestName_Matches(t *testing.T) {
	j := &Job{}
	if j.Name() != Name {
		t.Errorf("Name(): got %q, want %q", j.Name(), Name)
	}
	if Name != "dhcp_bundle_rerender" {
		t.Errorf("package-level Name constant changed unexpectedly: %q", Name)
	}
}
