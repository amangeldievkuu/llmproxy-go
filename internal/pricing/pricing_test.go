package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupPicksLongestPrefix(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"gpt-4o-mini-2024-07-18", "gpt-4o-mini"},
		{"gpt-4o-2024-08-06", "gpt-4o"},
		{"o3-mini-2025-01-31", "o3-mini"},
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"claude-opus-5", "claude-opus-5"},
		{"claude-fable-5-1", "claude-fable-5"},
	}
	table := Default()
	for _, tt := range tests {
		got, ok := table.Lookup(tt.model)
		if !ok || got != table[tt.want] {
			t.Errorf("Lookup(%q) = %+v, %v; want %s price %+v", tt.model, got, ok, tt.want, table[tt.want])
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	for _, model := range []string{"llama-3", ""} {
		if _, ok := Default().Lookup(model); ok {
			t.Errorf("Lookup(%q) found a price, want none", model)
		}
	}
}

func TestCost(t *testing.T) {
	p := Price{Input: 0.15, Output: 0.60}
	got := p.Cost(10, 14)
	if want := 0.0000099; math.Abs(got-want) > 1e-12 {
		t.Errorf("Cost() = %g, want %g", got, want)
	}
}

func TestLoadMergesOverDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	data := `{"gpt-4o-mini": {"input": 1, "output": 2}, "llama-3": {"input": 0.5, "output": 0.5}}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	table, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := table["gpt-4o-mini"]; got != (Price{Input: 1, Output: 2}) {
		t.Errorf("override not applied: %+v", got)
	}
	if _, ok := table.Lookup("llama-3-70b"); !ok {
		t.Error("new model not added")
	}
	if _, ok := table.Lookup("gpt-4o"); !ok {
		t.Error("defaults lost")
	}
	if Default()["gpt-4o-mini"] == table["gpt-4o-mini"] {
		t.Error("Load modified the built-in table")
	}
}
