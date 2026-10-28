// Package pricing turns token counts into US dollars.
package pricing

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
)

// Price is the cost of one million tokens, in USD.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// Cost returns the price of a request with the given token counts.
func (p Price) Cost(inputTokens, outputTokens int) float64 {
	return (float64(inputTokens)*p.Input + float64(outputTokens)*p.Output) / 1_000_000
}

// Table maps a model name prefix to its price.
type Table map[string]Price

// defaults are list prices for standard (non-batch, non-cached) usage, last
// checked October 2026. Providers change prices: override them with Load.
var defaults = Table{
	// OpenAI
	"gpt-5":        {Input: 1.25, Output: 10.00},
	"gpt-5-mini":   {Input: 0.25, Output: 2.00},
	"gpt-5-nano":   {Input: 0.05, Output: 0.40},
	"gpt-4.1":      {Input: 2.00, Output: 8.00},
	"gpt-4.1-mini": {Input: 0.40, Output: 1.60},
	"gpt-4.1-nano": {Input: 0.10, Output: 0.40},
	"gpt-4o":       {Input: 2.50, Output: 10.00},
	"gpt-4o-mini":  {Input: 0.15, Output: 0.60},
	"o3":           {Input: 2.00, Output: 8.00},
	"o3-mini":      {Input: 1.10, Output: 4.40},
	"o4-mini":      {Input: 1.10, Output: 4.40},

	// Anthropic
	"claude-fable-5":   {Input: 10.00, Output: 50.00},
	"claude-opus-5-5":  {Input: 4.00, Output: 20.00},
	"claude-opus-5":    {Input: 5.00, Output: 25.00},
	"claude-opus-4-8":  {Input: 5.00, Output: 25.00},
	"claude-opus-4-7":  {Input: 5.00, Output: 25.00},
	"claude-opus-4-6":  {Input: 5.00, Output: 25.00},
	"claude-sonnet-5":  {Input: 2.00, Output: 10.00},
	"claude-sonnet-4":  {Input: 3.00, Output: 15.00},
	"claude-haiku-5-5": {Input: 0.10, Output: 0.50},
	"claude-haiku-4-5": {Input: 1.00, Output: 5.00},
}

// Default returns a copy of the built-in price table.
func Default() Table {
	return maps.Clone(defaults)
}

// Load returns the built-in table with the prices from a JSON file merged over it.
// The file maps model prefixes to prices, e.g. {"gpt-4o-mini": {"input": 0.15, "output": 0.6}}.
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
	maps.Copy(t, extra)
	return t, nil
}

// Lookup finds the price of a model by the longest matching prefix, so that
// "gpt-4o-mini-2024-07-18" gets the "gpt-4o-mini" price rather than "gpt-4o".
func (t Table) Lookup(model string) (Price, bool) {
	var best string
	for name := range t {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best = name
		}
	}
	p, ok := t[best]
	return p, ok
}
