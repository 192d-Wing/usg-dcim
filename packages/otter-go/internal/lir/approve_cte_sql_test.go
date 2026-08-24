// Structural pin on the ApproveLirRequest CTE shape.
//
// The CTE atomically inserts a tenant Supernet + LirAllocation and
// flips the request to 'approved'. PostgreSQL data-modifying CTEs in
// WITH always execute regardless of whether downstream consumes
// their RETURNING, so to prevent leaking orphan rows when the
// request raced out of 'pending_approval', new_supernet MUST be
// chained off updated_request via INSERT ... SELECT ... FROM
// updated_request (not INSERT ... VALUES).
//
// A real Postgres + concurrent-race integration test belongs in the
// integration suite; this unit-level pin guards against a future edit
// that silently regresses the structural property by reading the
// .sql source file and asserting on its shape.
package lir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Pin post-review fix #6: CountAllocationsForPoolSupernet must
// exclude status='returned' rows so a pool supernet stops being
// trapped once its allocations are returned. Earlier shape
// counted every allocation indefinitely.
func TestCountAllocationsForPoolSupernet_ExcludesReturned(t *testing.T) {
	sql := readSiblingSQL(t, "db/queries/lir.sql")
	q := extractQueryByName(sql, "CountAllocationsForPoolSupernet")
	if q == "" {
		t.Fatal("CountAllocationsForPoolSupernet query not found in lir.sql")
	}
	if !strings.Contains(q, "status != 'returned'") {
		t.Errorf("count must filter out returned allocations, got:\n%s", q)
	}
}

// Pin post-review fix #4: ResetArinJobForRetry must be direction-
// aware — rows with arin_net_handle set are remove-direction
// failures and must reset to 'removing', not 'pending'. Earlier
// shape always wrote 'pending' which orphaned the row.
func TestResetArinJobForRetry_DirectionAware(t *testing.T) {
	sql := readSiblingSQL(t, "db/queries/lir_arin.sql")
	q := extractQueryByName(sql, "ResetArinJobForRetry")
	if q == "" {
		t.Fatal("ResetArinJobForRetry query not found in lir_arin.sql")
	}
	// The CASE on arin_net_handle is the load-bearing direction
	// switch. Without it the SQL always wrote 'pending', which
	// stranded rows that had registered upstream.
	if !strings.Contains(q, "WHEN arin_net_handle IS NULL THEN 'pending'") {
		t.Errorf("reset must route by direction; missing CASE on net_handle:\n%s", q)
	}
	if !strings.Contains(q, "ELSE 'removing'") {
		t.Errorf("reset must route remove-direction rows to 'removing':\n%s", q)
	}
}

// Pin post-review fix #12: ApproveLirRequest's CTE returns the full
// LirRequest + LirAllocation rows so the handler doesn't need to
// re-fetch via GetLirRequest + GetLirAllocation. Earlier shape
// returned only three UUIDs and the handler issued two extra
// round-trips per approval.
func TestApproveCTE_WidenedReturnIncludesFullRows(t *testing.T) {
	sql := readSiblingSQL(t, "db/queries/lir_allocations.sql")
	approve := extractQueryByName(sql, "ApproveLirRequest")
	if approve == "" {
		t.Fatal("ApproveLirRequest query not found")
	}
	// Columns present in the wider LirRequest + LirAllocation
	// projection but absent from the earlier three-UUID one. A
	// future edit that narrows the SELECT (and re-introduces
	// the re-fetches) fails this pin.
	for _, col := range []string{
		"ur.justification", "ur.submitted_at", "ur.classification",
		"na.allocated_at", "na.arin_status", "na.arin_net_handle",
		"na.created_at",
	} {
		if !strings.Contains(approve, col) {
			t.Errorf("widened CTE must SELECT %s; query:\n%s", col, approve)
		}
	}
}

// Pin post-review fix #13: LoadConfig now uses GetSystemSettings
// (batch) instead of three sequential GetSystemSetting calls.
func TestSettings_BatchQueryExists(t *testing.T) {
	sql := readSiblingSQL(t, "db/queries/lir_arin.sql")
	q := extractQueryByName(sql, "GetSystemSettings")
	if q == "" {
		t.Fatal("GetSystemSettings batch query missing from lir_arin.sql")
	}
	// The source uses sqlc's named-arg form (sqlc.arg(keys)::text[]),
	// which sqlc rewrites to the positional $1::text[] at generation
	// time — accept either spelling; the load-bearing property is the
	// single ANY(...::text[]) batch filter.
	if !strings.Contains(q, "ANY($1::text[])") && !strings.Contains(q, "ANY(sqlc.arg(keys)::text[])") {
		t.Errorf("batch query must use ANY(<keys param>::text[]); got:\n%s", q)
	}
}

// Pin post-review fix #14: GetSupernetForMove keys on is_system
// rather than a slug literal. The earlier projection returned
// f.slug AS current_fabric_slug, which forced the landing-fabric
// name to live in three places (migration 0065, lir/handler.go,
// ipam/move.go). The is_system column is the single source of
// truth on the row itself.
func TestMove_GetSupernetUsesIsSystemFlag(t *testing.T) {
	sql := readSiblingSQL(t, "db/queries/ipam_move.sql")
	q := extractQueryByName(sql, "GetSupernetForMove")
	if q == "" {
		t.Fatal("GetSupernetForMove query not found in ipam_move.sql")
	}
	if !strings.Contains(q, "f.is_system") {
		t.Errorf("move query must read f.is_system, got:\n%s", q)
	}
	if strings.Contains(q, "f.slug") {
		t.Errorf("move query must not depend on f.slug literal anymore:\n%s", q)
	}
}

func TestApproveCTE_NewSupernetChainsOffUpdatedRequest(t *testing.T) {
	sql := readSiblingSQL(t, "db/queries/lir_allocations.sql")
	// Slice out the ApproveLirRequest query block — the file has
	// other queries, but only this one carries the race-sensitive
	// pattern we're guarding.
	approve := extractQueryByName(sql, "ApproveLirRequest")
	if approve == "" {
		t.Fatal("ApproveLirRequest query not found in lir_allocations.sql")
	}

	// 1. updated_request must precede new_supernet inside the WITH
	//    so PostgreSQL's evaluation order matches our intent.
	upd := strings.Index(approve, "updated_request AS")
	sup := strings.Index(approve, "new_supernet AS")
	if upd < 0 || sup < 0 || upd > sup {
		t.Fatalf("updated_request must appear before new_supernet in WITH:\nupd=%d sup=%d", upd, sup)
	}

	// 2. new_supernet must be INSERT ... SELECT ... FROM updated_request
	//    (NOT INSERT ... VALUES). The SELECT FROM updated_request is
	//    the load-bearing guard: when the UPDATE matches zero rows
	//    the INSERT yields zero rows, so no orphan supernet escapes.
	supBlock := sliceBetween(approve, "new_supernet AS", "new_allocation AS")
	if strings.Contains(supBlock, "VALUES (") || strings.Contains(supBlock, "VALUES(") {
		t.Errorf("new_supernet uses VALUES — orphans leak on race:\n%s", supBlock)
	}
	if !strings.Contains(supBlock, "FROM updated_request") {
		t.Errorf("new_supernet must SELECT FROM updated_request:\n%s", supBlock)
	}

	// 3. new_allocation must continue to chain off new_supernet.
	allocBlock := sliceBetween(approve, "new_allocation AS", "FROM updated_request, new_allocation")
	if !strings.Contains(allocBlock, "FROM new_supernet") {
		t.Errorf("new_allocation must SELECT FROM new_supernet:\n%s", allocBlock)
	}
}

// readSiblingSQL resolves a file path relative to the otter-go
// package root regardless of test invocation cwd.
func readSiblingSQL(t *testing.T, rel string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = .../packages/otter-go/internal/lir/approve_cte_sql_test.go
	// go up to packages/otter-go/, then resolve rel.
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	full := filepath.Join(root, rel)
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read %s: %v", full, err)
	}
	return string(b)
}

// extractQueryByName slices out a single sqlc-annotated query block.
// Returns "" when not found.
func extractQueryByName(sql, name string) string {
	marker := "-- name: " + name + " "
	start := strings.Index(sql, marker)
	if start < 0 {
		return ""
	}
	rest := sql[start:]
	// Block ends at the next "-- name:" marker or EOF.
	end := strings.Index(rest[len(marker):], "-- name:")
	if end < 0 {
		return rest
	}
	return rest[:len(marker)+end]
}

// sliceBetween returns sql between two markers (exclusive of the
// trailing marker). When either marker is missing returns "".
func sliceBetween(sql, from, to string) string {
	a := strings.Index(sql, from)
	if a < 0 {
		return ""
	}
	rest := sql[a:]
	b := strings.Index(rest, to)
	if b < 0 {
		return rest
	}
	return rest[:b]
}
