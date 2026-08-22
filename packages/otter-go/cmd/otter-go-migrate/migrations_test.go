package main

import (
	"strings"
	"testing"
)

// TestMigrationsHaveGooseMarkers walks every embedded .sql file and
// asserts it has the `-- +goose Up` and `-- +goose Down` markers in
// the correct order. Catches mechanical-translation drops where a
// section header is missing or misspelled — without needing a live
// Postgres to actually run the SQL.
func TestMigrationsHaveGooseMarkers(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 60 {
		// Sanity floor: 62 mechanical-translation + 5 hand-ported = 67.
		// Anything under 60 means the embed regressed or files were
		// deleted by accident.
		t.Fatalf("expected ≥60 migrations; got %d", len(entries))
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatalf("%s: read: %v", e.Name(), err)
		}
		s := string(b)
		up := strings.Index(s, "-- +goose Up")
		dn := strings.Index(s, "-- +goose Down")
		if up < 0 {
			t.Errorf("%s: missing `-- +goose Up` marker", e.Name())
		}
		if dn < 0 {
			t.Errorf("%s: missing `-- +goose Down` marker", e.Name())
		}
		if up >= 0 && dn >= 0 && dn <= up {
			t.Errorf("%s: Down marker must come after Up marker (up=%d, down=%d)", e.Name(), up, dn)
		}
	}
}

// TestEmbeddedVersionIDsAreUnique ensures the version-number prefix of
// every embedded migration is distinct. A duplicate would mean goose
// silently re-applies one file as another's version — the kind of
// bug that survives a clean build but corrupts goose_db_version.
func TestEmbeddedVersionIDsAreUnique(t *testing.T) {
	versions, err := embeddedVersionIDs()
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[int64]int)
	for _, v := range versions {
		seen[v]++
		if seen[v] > 1 {
			t.Errorf("duplicate version id %d", v)
		}
	}
}

// TestNormalizeDSN pins the SQLAlchemy → pgx scheme rewrite so a
// future change can't accidentally break the same Helm value that
// the prior Python alembic Job read. Covers every SQLAlchemy driver
// hint the team has wired through this DSN env var (asyncpg in
// prod, psycopg2 in some legacy migration scripts, psycopg for
// SQLAlchemy 2.0).
func TestNormalizeDSN(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"postgresql://user:pass@host:5432/db", "postgresql://user:pass@host:5432/db"},
		{"postgresql+asyncpg://user:pass@host:5432/db", "postgresql://user:pass@host:5432/db"},
		{"postgresql+psycopg2://user:pass@host:5432/db", "postgresql://user:pass@host:5432/db"},
		{"postgresql+psycopg://user:pass@host:5432/db", "postgresql://user:pass@host:5432/db"},
		// `postgres://` (no driver hint, alternate scheme) passes through.
		{"postgres://user:pass@host/db", "postgres://user:pass@host/db"},
	}
	for _, c := range cases {
		if got := normalizeDSN(c.in); got != c.want {
			t.Errorf("normalizeDSN(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

// TestExpectedAlembicHeadIsLatest pins the bootstrap head to the
// numerically-largest version among the embedded migrations. If a
// future PR adds a new migration but forgets to bump
// expectedAlembicHead, this fires so the bootstrap doesn't silently
// keep accepting an old alembic state on cutover databases that
// were upgraded past the recorded head.
func TestExpectedAlembicHeadIsLatest(t *testing.T) {
	versions, err := embeddedVersionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) == 0 {
		t.Fatal("no embedded versions")
	}
	last := versions[len(versions)-1]
	// Highest embedded goose version_id. Bump this when a migration is
	// added so the guard keeps catching an accidental/forgotten add.
	// 69 = 00069_rack_grid_placement.sql (floor-plan tile placement).
	if last != 69 {
		t.Errorf("embedded migrations changed (last version_id=%d); bump the expected version in this test", last)
	}
	if expectedAlembicHead == "" {
		t.Error("expectedAlembicHead must not be empty")
	}
}
