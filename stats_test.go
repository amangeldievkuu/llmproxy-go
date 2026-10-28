package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amangeldievkuu/llmproxy/internal/store"
)

func TestStats(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.Record{
		{Time: time.Now(), Model: "gpt-4o-mini", Tag: "chat", InputTokens: 10, OutputTokens: 14, CostUSD: 0.0000099},
		{Time: time.Now(), Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, CostUSD: 0.00075},
	} {
		if err := st.Insert(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	var out strings.Builder
	if err := runStats([]string{"-db", dbPath}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"MODEL", "gpt-4o-mini", "(none)", "chat", "TOTAL", "$0.000760"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestStatsMissingDatabase(t *testing.T) {
	err := runStats([]string{"-db", filepath.Join(t.TempDir(), "missing.db")}, &strings.Builder{})
	if err == nil {
		t.Error("expected an error for a missing database")
	}
}
