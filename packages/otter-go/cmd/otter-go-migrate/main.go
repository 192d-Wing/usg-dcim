// otter-go-migrate runs the schema migrations on the DCIM Postgres
// database. Replaces the Python alembic Job that ran out of the
// dcim-otter image until the final Python rip.
//
// Two-phase boot:
//
//  1. Bootstrap (idempotent): if the database has an `alembic_version`
//     table from the prior alembic-managed era, seed goose's version
//     table with all migrations marked applied and drop
//     alembic_version. This is the cutover step that runs ONCE in
//     prod; subsequent runs find no alembic_version table and skip
//     straight to phase 2.
//
//  2. Goose `up`: apply any unapplied .sql files from migrations/
//     in version order. Fresh databases run every migration; cut-over
//     databases run only migrations newer than the alembic head at
//     cutover time (which is all of them since we re-numbered the
//     chain — but the bootstrap marked them all applied so none re-run).
//
// Run with DCIM_POSTGRES_DSN set. The DSN may be either the
// postgresql:// driver scheme or the SQLAlchemy postgresql+asyncpg://
// scheme — the latter is normalized to the lib/pq-compatible form so
// the same Helm value continues to work.
package main

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// expectedAlembicHead is the last alembic revision id at cutover.
// bootstrapAlembic refuses to seed goose_db_version from any other
// alembic head — see that function for why blind-seeding is unsafe.
const expectedAlembicHead = "20260531_0068"

//go:embed migrations/*.sql
var migrationsFS embed.FS

func main() {
	cmd := flag.String("cmd", "up", "goose command: up | down | status | version | reset")
	timeout := flag.Duration("timeout", 5*time.Minute, "max time to spend running migrations")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	dsn := normalizeDSN(os.Getenv("DCIM_POSTGRES_DSN"))
	if dsn == "" {
		log.Error("missing_dsn", "msg", "DCIM_POSTGRES_DSN unset")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Error("db_open_failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		log.Error("db_ping_failed", "err", err)
		os.Exit(1)
	}

	// Bootstrap: convert alembic state → goose state if needed.
	if err := bootstrapAlembic(ctx, db, log); err != nil {
		log.Error("bootstrap_failed", "err", err)
		os.Exit(1)
	}

	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(gooseLogger{log: log})
	if err := goose.SetDialect("postgres"); err != nil {
		log.Error("dialect_set_failed", "err", err)
		os.Exit(1)
	}

	if err := goose.RunContext(ctx, *cmd, db, "migrations"); err != nil {
		log.Error("goose_run_failed", "cmd", *cmd, "err", err)
		os.Exit(1)
	}
	log.Info("migrate_complete", "cmd", *cmd)
}

// normalizeDSN converts a SQLAlchemy-shaped DSN to lib/pq-shape so
// the same Helm value works without a values.yaml edit. The Python
// chart wrote `postgresql+asyncpg://...`; pgx wants `postgresql://`.
// Strips any `+<driver>` suffix between `postgresql` and `://`, so
// `postgresql+psycopg://`, `postgresql+psycopg2://`, etc. — anything
// a SQLAlchemy dialect hint might land in the value — all normalize
// to the bare pgx-compatible scheme.
var sqlaDriverHint = regexp.MustCompile(`^postgresql\+\w+://`)

func normalizeDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	return sqlaDriverHint.ReplaceAllString(dsn, "postgresql://")
}

// bootstrapAlembic runs ONCE per database: if `alembic_version`
// exists and `goose_db_version` doesn't, validate the alembic head
// matches the cutover head, seed all goose versions as applied (the
// schema is already at that head, so re-running goose against it
// would fail), then drop alembic_version.
//
// On databases that have already cut over, both tables exist; we
// no-op. On fresh databases, neither table exists; we no-op and let
// goose handle the rest. Crash mid-bootstrap is safe — the inserts
// are inside a transaction and either commit fully or roll back.
//
// The head-value check refuses to bootstrap a database stuck at an
// older alembic revision (e.g. a snapshot taken before cutover or
// an environment that lagged behind prod). Blind-seeding such a
// database would silently mark migrations applied that were never
// run — runtime queries would then reference columns/tables that
// don't exist. The right recovery is to alembic-upgrade to the
// cutover head FIRST (using a snapshot of the deleted Python code)
// and then re-run the migrate Job.
func bootstrapAlembic(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	hasAlembic, err := tableExists(ctx, db, "alembic_version")
	if err != nil {
		return fmt.Errorf("probe alembic_version: %w", err)
	}
	if !hasAlembic {
		return nil
	}
	hasGoose, err := tableExists(ctx, db, "goose_db_version")
	if err != nil {
		return fmt.Errorf("probe goose_db_version: %w", err)
	}
	if hasGoose {
		// Cutover already happened on a prior run; alembic_version
		// might still be hanging around from a half-completed
		// bootstrap. Drop it for tidiness — schema is now goose-owned.
		log.Info("bootstrap_drop_residual_alembic")
		_, err := db.ExecContext(ctx, "DROP TABLE alembic_version")
		return err
	}

	// Validate the alembic head matches our cutover head. Refuse to
	// bootstrap from any other value — see func doc for why.
	var heads []string
	rows, err := db.QueryContext(ctx, "SELECT version_num FROM alembic_version")
	if err != nil {
		return fmt.Errorf("read alembic_version: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan alembic_version: %w", err)
		}
		heads = append(heads, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate alembic_version: %w", err)
	}
	if len(heads) != 1 || heads[0] != expectedAlembicHead {
		return fmt.Errorf(
			"alembic_version is %v; bootstrap requires the single head %q. Upgrade the schema to that revision before re-running this Job",
			heads, expectedAlembicHead,
		)
	}

	log.Info("bootstrap_seed_goose_from_alembic", "from_head", heads[0])
	versions, err := embeddedVersionIDs()
	if err != nil {
		return err
	}
	if len(versions) == 0 {
		return errors.New("no embedded migration files found")
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Create goose's version table with goose's own schema. We mirror
	// goose's CREATE here rather than calling goose.EnsureDBVersion
	// because we want to seed it ourselves before goose itself runs.
	if _, err := tx.ExecContext(ctx, gooseTableSQL); err != nil {
		return fmt.Errorf("create goose_db_version: %w", err)
	}
	// Goose convention: version_id=0 is the initial "no migrations
	// applied" marker, inserted at table create. Insert it first.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true)`,
	); err != nil {
		return fmt.Errorf("seed initial version: %w", err)
	}
	for _, v := range versions {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`,
			v,
		); err != nil {
			return fmt.Errorf("seed version %d: %w", v, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE alembic_version"); err != nil {
		return fmt.Errorf("drop alembic_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bootstrap: %w", err)
	}
	log.Info("bootstrap_complete", "seeded", len(versions))
	return nil
}

// gooseTableSQL mirrors goose v3's internal CREATE TABLE for
// goose_db_version. Kept here so the bootstrap creates the same
// shape goose expects; goose's EnsureDBVersion will then be a no-op.
const gooseTableSQL = `
CREATE TABLE goose_db_version (
    id SERIAL NOT NULL,
    version_id BIGINT NOT NULL,
    is_applied BOOLEAN NOT NULL,
    tstamp TIMESTAMP NULL DEFAULT NOW(),
    PRIMARY KEY (id)
);
`

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND table_name = $1
		)`, name).Scan(&exists)
	return exists, err
}

// embeddedVersionIDs walks the embedded migrations/ directory and
// returns the parsed leading integer of each .sql filename in
// ascending order. Filenames are NNNNN_name.sql, so 5-digit numeric
// prefix.
func embeddedVersionIDs() ([]int64, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		// Goose names are `<version>_<descr>.sql` — version is the
		// leading numeric run before the first `_`.
		i := strings.IndexByte(e.Name(), '_')
		if i <= 0 {
			continue
		}
		var v int64
		if _, err := fmt.Sscanf(e.Name()[:i], "%d", &v); err != nil {
			continue
		}
		out = append(out, v)
	}
	// embed.FS.ReadDir returns sorted entries, so out is already in
	// ascending order. Trust that without re-sorting.
	return out, nil
}

// gooseLogger adapts slog to goose's Logger interface so migration
// progress shares the same structured log stream as the rest of the
// process.
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Fatalf(fmtStr string, args ...any) {
	g.log.Error("goose_fatal", "msg", fmt.Sprintf(fmtStr, args...))
	os.Exit(1)
}

func (g gooseLogger) Printf(fmtStr string, args ...any) {
	g.log.Info("goose", "msg", strings.TrimRight(fmt.Sprintf(fmtStr, args...), "\n"))
}
