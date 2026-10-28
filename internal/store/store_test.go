package store

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSpentSinceAndSummary(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()

	records := []Record{
		{Time: now.Add(-48 * time.Hour), Model: "gpt-4o", CostUSD: 5}, // too old
		{Time: now, Model: "gpt-4o-mini", Tag: "chat", InputTokens: 10, OutputTokens: 20, CostUSD: 0.01},
		{Time: now, Model: "gpt-4o-mini", Tag: "chat", InputTokens: 5, OutputTokens: 5, Cached: true},
		{Time: now, Model: "gpt-4o", Tag: "search", InputTokens: 100, OutputTokens: 50, CostUSD: 0.5},
	}
	for _, r := range records {
		r.Method, r.Path, r.Status = "POST", "/v1/chat/completions", 200
		if err := s.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	since := now.Add(-24 * time.Hour)
	spent, err := s.SpentSince(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(spent-0.51) > 1e-9 {
		t.Errorf("SpentSince() = %v, want 0.51", spent)
	}

	rows, err := s.Summary(ctx, since, "model")
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{Key: "gpt-4o", Requests: 1, InputTokens: 100, OutputTokens: 50, CostUSD: 0.5},
		{Key: "gpt-4o-mini", Requests: 2, Cached: 1, InputTokens: 15, OutputTokens: 25, CostUSD: 0.01},
	}
	if len(rows) != len(want) {
		t.Fatalf("Summary() returned %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestSummaryRejectsUnknownGrouping(t *testing.T) {
	if _, err := openTest(t).Summary(context.Background(), time.Now(), "path; DROP TABLE requests"); err == nil {
		t.Error("expected an error")
	}
}

func TestOpenSetsPragmas(t *testing.T) {
	s := openTest(t)
	for pragma, want := range map[string]string{"journal_mode": "wal", "busy_timeout": "5000", "synchronous": "1"} {
		var got string
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", pragma, got, want)
		}
	}
	var plan string
	var id, parent, notused int
	err := s.db.QueryRow(`EXPLAIN QUERY PLAN SELECT COALESCE(SUM(cost_usd), 0) FROM requests WHERE ts >= 0`).
		Scan(&id, &parent, &notused, &plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "COVERING INDEX requests_ts_cost") {
		t.Errorf("budget query does not use the covering index: %s", plan)
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now()
	for _, age := range []time.Duration{100 * 24 * time.Hour, 91 * 24 * time.Hour, 89 * 24 * time.Hour, 0} {
		if err := s.Insert(ctx, Record{Time: now.Add(-age), Model: "m", CostUSD: 1}); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := now.Add(-90 * 24 * time.Hour)

	if n, err := s.CountBefore(ctx, cutoff); err != nil || n != 2 {
		t.Fatalf("CountBefore() = %d, %v; want 2", n, err)
	}
	if n, err := s.Prune(ctx, cutoff); err != nil || n != 2 {
		t.Fatalf("Prune() = %d, %v; want 2", n, err)
	}
	if err := s.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	if spent, _ := s.SpentSince(ctx, time.Time{}); spent != 2 {
		t.Errorf("%v left after pruning, want the 2 recent requests", spent)
	}
	if n, _ := s.Prune(ctx, cutoff); n != 0 {
		t.Errorf("second Prune() deleted %d", n)
	}
}
