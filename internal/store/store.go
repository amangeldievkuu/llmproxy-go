// Package store keeps a record of every proxied request in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, keeps the binary cgo-free
)

const schema = `
CREATE TABLE IF NOT EXISTS requests (
	id            INTEGER PRIMARY KEY,
	ts            INTEGER NOT NULL, -- unix milliseconds
	method        TEXT    NOT NULL,
	path          TEXT    NOT NULL,
	status        INTEGER NOT NULL,
	model         TEXT    NOT NULL,
	tag           TEXT    NOT NULL,
	input_tokens  INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	cost_usd      REAL    NOT NULL,
	latency_ms    INTEGER NOT NULL,
	cached        INTEGER NOT NULL
);
-- Covers the daily budget check, which runs on every request.
CREATE INDEX IF NOT EXISTS requests_ts_cost ON requests (ts, cost_usd);
DROP INDEX IF EXISTS requests_ts;
`

// pragmas are set in the DSN so that every connection the pool opens gets them.
// synchronous=NORMAL is safe in WAL mode and avoids an fsync per insert.
const pragmas = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

// Record is one proxied request.
type Record struct {
	Time         time.Time
	Method       string
	Path         string
	Status       int
	Model        string
	Tag          string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Latency      time.Duration
	Cached       bool
}

// Row is one line of a usage summary.
type Row struct {
	Key          string
	Requests     int
	Cached       int
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// Store is a SQLite database of request records.
type Store struct {
	db *sql.DB
}

// Open opens the database at path, creating it if needed.
func Open(path string) (*Store, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("open %s: database path must not contain '?'", path)
	}
	db, err := sql.Open("sqlite", path+"?"+pragmas)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection avoids "database is locked".
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert saves one record.
func (s *Store) Insert(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO requests (ts, method, path, status, model, tag, input_tokens, output_tokens, cost_usd, latency_ms, cached)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Time.UnixMilli(), r.Method, r.Path, r.Status, r.Model, r.Tag,
		r.InputTokens, r.OutputTokens, r.CostUSD, r.Latency.Milliseconds(), r.Cached)
	return err
}

// SpentSince returns the total cost of requests made at or after t.
func (s *Store) SpentSince(ctx context.Context, t time.Time) (float64, error) {
	var spent float64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0) FROM requests WHERE ts >= ?`, t.UnixMilli()).Scan(&spent)
	return spent, err
}

// CountBefore returns the number of requests made before t.
func (s *Store) CountBefore(ctx context.Context, t time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE ts < ?`, t.UnixMilli()).Scan(&n)
	return n, err
}

// Prune deletes the requests made before t and returns how many it deleted.
func (s *Store) Prune(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE ts < ?`, t.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Vacuum rebuilds the database file so that space freed by Prune is given
// back to the disk.
func (s *Store) Vacuum(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `VACUUM`)
	return err
}

// Summary groups the requests made at or after from, and before to unless to
// is zero, by "model", "tag" or "day". Models and tags come most expensive
// first. Days come oldest first, as dates in the time zone of from.
func (s *Store) Summary(ctx context.Context, from, to time.Time, by string) ([]Row, error) {
	var key string
	switch by {
	case "model", "tag":
		key = by
	case "day":
		// SQLite's idea of local time may not match Go's, so group by
		// 15-minute buckets and turn them into dates in Go. Every time zone
		// offset is a whole number of quarter hours, so no bucket spans midnight.
		key = "ts / 900000"
	default:
		return nil, fmt.Errorf("cannot group by %q", by)
	}
	where, args := "ts >= ?", []any{from.UnixMilli()}
	if !to.IsZero() {
		where += " AND ts < ?"
		args = append(args, to.UnixMilli())
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+key+`, COUNT(*), SUM(cached), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd)
		FROM requests
		WHERE `+where+`
		GROUP BY `+key+`
		ORDER BY SUM(cost_usd) DESC, COUNT(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Key, &r.Requests, &r.Cached, &r.InputTokens, &r.OutputTokens, &r.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if by == "day" {
		return byDay(out, from.Location())
	}
	return out, nil
}

// byDay merges rows keyed by 15-minute bucket into rows keyed by date in loc,
// oldest first.
func byDay(buckets []Row, loc *time.Location) ([]Row, error) {
	days := make(map[string]*Row)
	for _, b := range buckets {
		n, err := strconv.ParseInt(b.Key, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad time bucket %q: %w", b.Key, err)
		}
		date := time.UnixMilli(n * 900_000).In(loc).Format("2006-01-02")
		d := days[date]
		if d == nil {
			d = &Row{Key: date}
			days[date] = d
		}
		d.Requests += b.Requests
		d.Cached += b.Cached
		d.InputTokens += b.InputTokens
		d.OutputTokens += b.OutputTokens
		d.CostUSD += b.CostUSD
	}
	out := make([]Row, 0, len(days))
	for _, d := range days {
		out = append(out, *d)
	}
	slices.SortFunc(out, func(a, b Row) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}
