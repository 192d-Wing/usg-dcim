// Package buffer is the on-disk overflow queue. Drivers Enqueue samples
// as they poll; the forwarder loop calls Drain to pull a batch, POSTs it
// to go-ingest, and calls Ack to delete the sent rows. Anything that
// errors stays in the buffer for the next cycle — this is the durability
// guarantee that lets the collector survive a central outage.
//
// Schema is intentionally tiny: a single `samples` table with id + JSON
// payload. SQLite WAL keeps writes fast and survives a kill -9.
package buffer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

type Sample struct {
	AssetID string            `json:"asset_id"`
	Metric  string            `json:"metric"`
	Value   float64           `json:"value"`
	Unit    string            `json:"unit,omitempty"`
	Ts      string            `json:"ts"` // RFC3339
	Tags    map[string]string `json:"tags,omitempty"`
}

// Row pairs a buffered sample with its id so the forwarder can ack
// exactly what it sent (drivers may have enqueued more in the
// meantime).
type Row struct {
	ID     int64
	Sample Sample
}

type Buffer struct {
	mu sync.Mutex
	db *sql.DB
}

func Open(path string) (*Buffer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir buffer parent: %w", err)
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// Single connection — modernc sqlite is fine with one, and the
	// collector's write rate is well under that ceiling. Avoids the
	// "database is locked" surprises that come with the default pool.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS samples (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			payload TEXT    NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Buffer{db: db}, nil
}

func (b *Buffer) Close() error { return b.db.Close() }

// Enqueue writes one sample. Called from driver goroutines.
func (b *Buffer) Enqueue(ctx context.Context, s Sample) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err = b.db.ExecContext(ctx, `INSERT INTO samples (payload) VALUES (?)`, string(raw))
	return err
}

// EnqueueBatch writes many samples in one tx. Useful when a driver poll
// returns several metrics at once.
func (b *Buffer) EnqueueBatch(ctx context.Context, ss []Sample) error {
	if len(ss) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO samples (payload) VALUES (?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, s := range ss {
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Drain returns the oldest up-to-limit rows without removing them. The
// caller is responsible for Ack after a successful POST.
func (b *Buffer) Drain(ctx context.Context, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = 500
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rows, err := b.db.QueryContext(ctx,
		`SELECT id, payload FROM samples ORDER BY id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		var payload string
		if err := rows.Scan(&r.ID, &payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &r.Sample); err != nil {
			// Corrupt row — log via the caller; drop it on Ack.
			r.Sample = Sample{}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ack deletes the supplied ids. Safe to call with an empty slice.
func (b *Buffer) Ack(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM samples WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Depth reports how many samples are currently buffered. Used by the
// heartbeat payload + Prometheus gauge.
func (b *Buffer) Depth(ctx context.Context) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var n int64
	err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM samples`).Scan(&n)
	return n, err
}
