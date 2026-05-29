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
