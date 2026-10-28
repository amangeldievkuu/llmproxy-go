// Package pricing turns token counts into US dollars.
package pricing

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"

	"github.com/amangeldievkuu/llmproxy-go/internal/usage"
)

// Price is the cost of one million tokens, in USD.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`  // cached input; 0 means the Input price
	CacheWrite float64 `json:"cache_write,omitempty"` // input written to the cache; 0 means the Input price

	// Requests with more than LongContextTokens input tokens are billed at
	// LongContext instead.
	LongContextTokens int    `json:"long_context_tokens,omitempty"`
	LongContext       *Price `json:"long_context,omitempty"`
}

// Cost returns the price of a request with the given usage.
func (p Price) Cost(u usage.Usage) float64 {
	if p.LongContext != nil && p.LongContextTokens > 0 && u.InputTokens > p.LongContextTokens {
		p = *p.LongContext
	}
	uncached := max(0, u.InputTokens-u.CacheReadTokens-u.CacheWriteTokens)
	return (float64(uncached)*p.Input +
		float64(u.CacheReadTokens)*cmp.Or(p.CacheRead, p.Input) +
		float64(u.CacheWriteTokens)*cmp.Or(p.CacheWrite, p.Input) +
		float64(u.OutputTokens)*p.Output) / 1_000_000
}

// Table maps a model name to its price. See Lookup for how names match.
type Table map[string]Price

// defaults are list prices for standard (non-batch) usage, last checked
// 2026-10-28 against the OpenAI and Anthropic pricing pages. Anthropic cache
// writes use the 5-minute cache price. Providers change prices: override them
// with Load.
var defaults = Table{
	// OpenAI
	"gpt-6-astra":   {Input: 10.00, Output: 50.00, CacheRead: 1.00, LongContextTokens: 272_000, LongContext: &Price{Input: 20.00, Output: 75.00, CacheRead: 2.00}},
	"gpt-6-sol":     {Input: 2.00, Output: 10.00, CacheRead: 0.20, LongContextTokens: 272_000, LongContext: &Price{Input: 4.00, Output: 15.00, CacheRead: 0.40}},
	"gpt-6-luna":    {Input: 0.10, Output: 0.50, CacheRead: 0.01, LongContextTokens: 272_000, LongContext: &Price{Input: 0.20, Output: 0.75, CacheRead: 0.02}},
	"gpt-6.1-sol":   {Input: 2.00, Output: 10.00, CacheRead: 0.10, LongContextTokens: 272_000, LongContext: &Price{Input: 4.00, Output: 15.00, CacheRead: 0.20}},
	"gpt-6.1-terra": {Input: 1.00, Output: 5.00, CacheRead: 0.05, LongContextTokens: 272_000, LongContext: &Price{Input: 2.00, Output: 7.50, CacheRead: 0.10}},
	"gpt-5.6-sol":   {Input: 4.00, Output: 20.00, CacheRead: 0.40, LongContextTokens: 272_000, LongContext: &Price{Input: 8.00, Output: 30.00, CacheRead: 0.80}},
	"gpt-5.6-terra": {Input: 2.00, Output: 12.00, CacheRead: 0.20, LongContextTokens: 272_000, LongContext: &Price{Input: 4.00, Output: 18.00, CacheRead: 0.40}},
	"gpt-5.6-luna":  {Input: 0.20, Output: 1.20, CacheRead: 0.02, LongContextTokens: 272_000, LongContext: &Price{Input: 0.40, Output: 1.80, CacheRead: 0.04}},
	"gpt-5.5":       {Input: 5.00, Output: 30.00, CacheRead: 0.50, LongContextTokens: 272_000, LongContext: &Price{Input: 10.00, Output: 45.00, CacheRead: 1.00}},
	"gpt-5.5-pro":   {Input: 30.00, Output: 180.00, LongContextTokens: 272_000, LongContext: &Price{Input: 60.00, Output: 270.00}},
	"gpt-5.4":       {Input: 2.50, Output: 15.00, CacheRead: 0.25, LongContextTokens: 272_000, LongContext: &Price{Input: 5.00, Output: 22.50, CacheRead: 0.50}},
	"gpt-5.4-pro":   {Input: 30.00, Output: 180.00, LongContextTokens: 272_000, LongContext: &Price{Input: 60.00, Output: 270.00}},
	"gpt-5.4-mini":  {Input: 0.75, Output: 4.50, CacheRead: 0.075},
	"gpt-5.4-nano":  {Input: 0.20, Output: 1.25, CacheRead: 0.02},
	"gpt-5.3-codex": {Input: 1.75, Output: 14.00, CacheRead: 0.175},
	"gpt-5.2":       {Input: 1.75, Output: 14.00, CacheRead: 0.175},
	"gpt-5.1":       {Input: 1.25, Output: 10.00, CacheRead: 0.125},
	"gpt-5":         {Input: 1.25, Output: 10.00, CacheRead: 0.125},
	"gpt-5-pro":     {Input: 15.00, Output: 120.00},
	"gpt-5-mini":    {Input: 0.25, Output: 2.00, CacheRead: 0.025},
	"gpt-5-nano":    {Input: 0.05, Output: 0.40, CacheRead: 0.005},
	"gpt-4.1":       {Input: 2.00, Output: 8.00, CacheRead: 0.50},
	"gpt-4.1-mini":  {Input: 0.40, Output: 1.60, CacheRead: 0.10},
	"gpt-4.1-nano":  {Input: 0.10, Output: 0.40, CacheRead: 0.025},
	"gpt-4o":        {Input: 2.50, Output: 10.00, CacheRead: 1.25},
	"gpt-4o-mini":   {Input: 0.15, Output: 0.60, CacheRead: 0.075},
	"o3":            {Input: 2.00, Output: 8.00, CacheRead: 0.50},
	"o3-pro":        {Input: 20.00, Output: 80.00},
	"o3-mini":       {Input: 1.10, Output: 4.40, CacheRead: 0.55},
	"o4-mini":       {Input: 1.10, Output: 4.40, CacheRead: 0.275},

	// Anthropic
	"claude-fable-5-1":  {Input: 10.00, Output: 50.00, CacheRead: 0.25, CacheWrite: 12.50},
	"claude-fable-5":    {Input: 10.00, Output: 50.00, CacheRead: 1.00, CacheWrite: 12.50},
	"claude-mythos-5-1": {Input: 10.00, Output: 50.00, CacheRead: 0.25, CacheWrite: 12.50},
	"claude-mythos-5":   {Input: 10.00, Output: 50.00, CacheRead: 1.00, CacheWrite: 12.50},
	"claude-opus-5-5":   {Input: 4.00, Output: 20.00, CacheRead: 0.20, CacheWrite: 5.00},
	"claude-opus-5":     {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-8":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-7":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-6":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4-5":   {Input: 5.00, Output: 25.00, CacheRead: 0.50, CacheWrite: 6.25},
	"claude-opus-4":     {Input: 15.00, Output: 75.00, CacheRead: 1.50, CacheWrite: 18.75}, // also Opus 4.1
	"claude-sonnet-5-5": {Input: 2.00, Output: 10.00, CacheRead: 0.10, CacheWrite: 2.50},
	"claude-sonnet-5":   {Input: 2.00, Output: 10.00, CacheRead: 0.20, CacheWrite: 2.50},
	"claude-sonnet-4":   {Input: 3.00, Output: 15.00, CacheRead: 0.30, CacheWrite: 3.75}, // also 4.5 and 4.6
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125, LongContextTokens: 100_000, LongContext: &Price{Input: 0.50, Output: 2.50, CacheRead: 0.05, CacheWrite: 0.625}},
	"claude-haiku-4-5":  {Input: 1.00, Output: 5.00, CacheRead: 0.10, CacheWrite: 1.25},
}

// Default returns a copy of the built-in price table.
func Default() Table {
	return maps.Clone(defaults)
}

// Load returns the built-in table with the prices from a JSON file merged over it.
// The file maps model names to prices, e.g. {"gpt-4o-mini": {"input": 0.15, "output": 0.6}}.
// An empty path returns the built-in table.
func Load(path string) (Table, error) {
	t := Default()
	if path == "" {
		return t, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var extra Table
	if err := json.Unmarshal(data, &extra); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for name, p := range extra {
		if p.Input < 0 || p.Output < 0 || p.CacheRead < 0 || p.CacheWrite < 0 {
			return nil, fmt.Errorf("%s: negative price for %q", path, name)
		}
	}
	maps.Copy(t, extra)
	return t, nil
}

// Lookup finds the price of a model. A name matches the model exactly, or as
// a prefix followed by a dated or numbered suffix: "gpt-4o-mini" matches
// "gpt-4o-mini-2024-07-18", but "o3" does not match the pricier "o3-pro" and
// "gpt-5" does not match "gpt-5.5". A name ending in "*" matches any suffix.
// The longest matching name wins.
func (t Table) Lookup(model string) (Price, bool) {
	var best string
	found := false
	for name := range t {
		if matches(name, model) && (!found || len(name) > len(best)) {
			best, found = name, true
		}
	}
	return t[best], found
}

func matches(name, model string) bool {
	if prefix, ok := strings.CutSuffix(name, "*"); ok {
		return strings.HasPrefix(model, prefix)
	}
	rest, ok := strings.CutPrefix(model, name)
	if !ok {
		return false
	}
	// "-2024-08-06", "-1-20250805" (minor version), "@20250805" (Vertex AI)
	return rest == "" || len(rest) >= 2 && (rest[0] == '-' || rest[0] == '@') && '0' <= rest[1] && rest[1] <= '9'
}
