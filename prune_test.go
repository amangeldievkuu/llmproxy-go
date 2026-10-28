package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

// oldAndNewDB creates a database with one 100-day-old and one current record.
func oldAndNewDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, ts := range []time.Time{time.Now().Add(-100 * 24 * time.Hour), time.Now()} {
		if err := st.Insert(context.Background(), store.Record{Time: ts, Model: "gpt-4o"}); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

func countRecords(t *testing.T, dbPath string) int {
	t.Helper()
	rows := summary(t, dbPath, 365*24*time.Hour)
	n := 0
	for _, r := range rows {
		n += r.Requests
	}
	return n
}

func TestPrune(t *testing.T) {
	dbPath := oldAndNewDB(t)

	var out strings.Builder
	if err := runPrune([]string{"-db", dbPath, "-older-than", "90d", "-dry-run"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would delete 1 records") || countRecords(t, dbPath) != 2 {
		t.Errorf("dry run: %q, %d records left", out.String(), countRecords(t, dbPath))
	}

	out.Reset()
	if err := runPrune([]string{"-db", dbPath, "-older-than", "90d"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deleted 1 records") || countRecords(t, dbPath) != 1 {
		t.Errorf("prune: %q, %d records left", out.String(), countRecords(t, dbPath))
	}
}

func TestPruneRejectsBadArgs(t *testing.T) {
	dbPath := oldAndNewDB(t)
	for name, args := range map[string][]string{
		"no -older-than": {"-db", dbPath},
		"zero":           {"-db", dbPath, "-older-than", "0d"},
		"missing db":     {"-db", filepath.Join(t.TempDir(), "missing.db"), "-older-than", "90d"},
	} {
		if err := runPrune(args, &strings.Builder{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if countRecords(t, dbPath) != 2 {
		t.Error("records were deleted")
	}
}
