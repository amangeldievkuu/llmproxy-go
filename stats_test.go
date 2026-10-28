package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
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

// datedDB creates a database with records on 2026-09-30, -10-01 and -10-02,
// local time.
func datedDB(t *testing.T) string {
	t.Helper()
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.Local) }
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, r := range []store.Record{
		{Time: at(0, 12), Model: "gpt-4o", CostUSD: 8}, // Sept 30
		{Time: at(1, 10), Model: "gpt-4o", CostUSD: 1},
		{Time: at(1, 23), Model: "gpt-4o-mini", CostUSD: 2},
		{Time: at(2, 0), Model: "gpt-4o-mini", CostUSD: 4}, // midnight
	} {
		if err := st.Insert(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

func stats(t *testing.T, args ...string) string {
	t.Helper()
	var out strings.Builder
	if err := runStats(args, &out); err != nil {
		t.Fatalf("stats %v: %v", args, err)
	}
	return out.String()
}

func TestStatsByDayAndRange(t *testing.T) {
	db := datedDB(t)

	got := stats(t, "-db", db, "-since", "2026-10-01", "-until", "2026-10-03", "-by", "day")
	for _, want := range []string{"From 2026-10-01 00:00 to 2026-10-03 00:00", "DAY", "$7.000000"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if i, j := strings.Index(got, "$3.000000"), strings.Index(got, "$4.000000"); i < 0 || j < i {
		t.Errorf("want 2026-10-01 ($3) before 2026-10-02 ($4):\n%s", got)
	}

	// -until is not included, so the record at midnight on the 2nd is left out.
	got = stats(t, "-db", db, "-since", "2026-10-01", "-until", "2026-10-02", "-by", "model")
	if !strings.Contains(got, "$3.000000") || strings.Contains(got, "TAG") {
		t.Errorf("want only the model table, totalling $3:\n%s", got)
	}
}

func TestStatsJSON(t *testing.T) {
	got := stats(t, "-db", datedDB(t), "-since", "2026-10-01", "-until", "2026-10-03", "-by", "day,model", "-format", "json")
	var doc struct {
		Since  time.Time `json:"since"`
		Until  time.Time `json:"until"`
		Groups map[string][]struct {
			Key      string  `json:"key"`
			Requests int     `json:"requests"`
			CostUSD  float64 `json:"cost_usd"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("%v:\n%s", err, got)
	}
	if doc.Since.Day() != 1 || doc.Until.Day() != 3 {
		t.Errorf("since %v, until %v", doc.Since, doc.Until)
	}
	days, models := doc.Groups["day"], doc.Groups["model"]
	if len(days) != 2 || days[0].Key != "2026-10-01" || days[0].CostUSD != 3 || len(models) != 2 {
		t.Errorf("unexpected groups %+v", doc.Groups)
	}
}

func TestStatsCSV(t *testing.T) {
	got := stats(t, "-db", datedDB(t), "-since", "2026-10-01", "-until", "2026-10-03", "-by", "day,model", "-format", "csv")
	records, err := csv.NewReader(strings.NewReader(got)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 || strings.Join(records[0], ",") != "group,key,requests,cached,input_tokens,output_tokens,cost_usd" {
		t.Errorf("unexpected CSV:\n%s", got)
	}
	if strings.Join(records[1], ",") != "day,2026-10-01,2,0,0,0,3" {
		t.Errorf("first row = %v", records[1])
	}
}

func TestStatsRejectsBadArgs(t *testing.T) {
	db := datedDB(t)
	for name, args := range map[string][]string{
		"by":     {"-by", "path"},
		"format": {"-format", "xml"},
		"order":  {"-since", "2026-10-02", "-until", "2026-10-01"},
	} {
		if err := runStats(append([]string{"-db", db}, args...), &strings.Builder{}); err == nil {
			t.Errorf("bad -%s: expected an error", name)
		}
	}
}
