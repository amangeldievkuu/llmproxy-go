package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/amangeldievkuu/llmproxy-go/internal/usage"
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
		{"claude-fable-5-1", "claude-fable-5-1"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4"},
		{"claude-opus-4-1@20250805", "claude-opus-4"},
		{"claude-opus-4-5-20251101", "claude-opus-4-5"},
		{"o3-pro-2025-06-10", "o3-pro"},
		{"gpt-5-2025-08-07", "gpt-5"},
		{"gpt-5.5", "gpt-5.5"},
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
	// Unlisted variants must not inherit a cheaper base model's price.
	for _, model := range []string{"llama-3", "", "o3-deep-research", "gpt-5.9", "gpt-4o-realtime-preview"} {
		if _, ok := Default().Lookup(model); ok {
			t.Errorf("Lookup(%q) found a price, want none", model)
		}
	}
}

func TestCost(t *testing.T) {
	long := &Price{Input: 2, Output: 20, CacheRead: 0.2}
	p := Price{Input: 1, Output: 10, CacheRead: 0.1, CacheWrite: 1.25, LongContextTokens: 1000, LongContext: long}
	tests := []struct {
		name  string
		price Price
		u     usage.Usage
		want  float64
	}{
		{"plain", p, usage.Usage{InputTokens: 100, OutputTokens: 10}, (100*1 + 10*10) / 1e6},
		{"cache", p, usage.Usage{InputTokens: 100, CacheReadTokens: 60, CacheWriteTokens: 20, OutputTokens: 10},
			(20*1 + 60*0.1 + 20*1.25 + 10*10) / 1e6},
		{"long context", p, usage.Usage{InputTokens: 2000, CacheReadTokens: 1000, OutputTokens: 10},
			(1000*2 + 1000*0.2 + 10*20) / 1e6},
		{"unset cache price uses input price", Price{Input: 2}, usage.Usage{InputTokens: 100, CacheReadTokens: 100},
			100 * 2 / 1e6},
	}
	for _, tt := range tests {
		if got := tt.price.Cost(tt.u); math.Abs(got-tt.want) > 1e-12 {
			t.Errorf("%s: Cost() = %g, want %g", tt.name, got, tt.want)
		}
	}
}

func TestLookupWildcard(t *testing.T) {
	table := Table{"llama-3.3-70b*": {Input: 1}, "llama-3.3-70b": {Input: 2}}
	if p, _ := table.Lookup("llama-3.3-70b-versatile"); p.Input != 1 {
		t.Errorf("wildcard not matched: %+v", p)
	}
	if p, _ := table.Lookup("llama-3.3-70b"); p.Input != 1 {
		t.Errorf("longest name should win, even over an exact match: %+v", p)
	}
}

func TestLoadRejectsNegativePrices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{"m": {"input": -1, "output": 1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected an error")
	}
}

func TestLoadMergesOverDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	data := `{"gpt-4o-mini": {"input": 1, "output": 2}, "llama-3*": {"input": 0.5, "output": 0.5}}`
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
