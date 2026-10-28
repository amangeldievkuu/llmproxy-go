package main

import (
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

// groupings are the values -by accepts, with the label for an empty key.
var groupings = map[string]string{"model": "(unknown)", "tag": "(none)", "day": ""}

// openExisting opens the database at path, which "llmproxy serve" must have created.
func openExisting(path string) (*store.Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("no database at %s (run \"llmproxy serve\" first)", path)
	}
	return store.Open(path)
}

func runStats(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	dbPath := fs.String("db", envOr("LLMPROXY_DB", "llmproxy.db"), "SQLite file for usage records (env LLMPROXY_DB)")
	since := moment{ago: 24 * time.Hour}
	fs.Var(&since, "since", "start: a date like 2026-09-01, or how long ago, e.g. 24h or 7d")
	var until moment
	fs.Var(&until, "until", "end, not included: same forms as -since (default now)")
	by := fs.String("by", "model,tag", "groupings to show, comma-separated: model, tag, day")
	format := fs.String("format", "table", "output format: table, json or csv")
	fs.Parse(args)

	groups := strings.Split(*by, ",")
	for i, g := range groups {
		groups[i] = strings.TrimSpace(g)
		if _, ok := groupings[groups[i]]; !ok {
			return fmt.Errorf("invalid -by %q (use model, tag or day)", g)
		}
	}
	if !slices.Contains([]string{"table", "json", "csv"}, *format) {
		return fmt.Errorf("invalid -format %q (use table, json or csv)", *format)
	}
	now := time.Now()
	from, to := since.at(now), until.at(now)
	if !to.After(from) {
		return fmt.Errorf("-until (%s) must be after -since (%s)", to.Format("2006-01-02 15:04"), from.Format("2006-01-02 15:04"))
	}

	st, err := openExisting(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	results := make(map[string][]store.Row, len(groups))
	for _, g := range groups {
		rows, err := st.Summary(context.Background(), from, to, g)
		if err != nil {
			return err
		}
		results[g] = rows
	}

	switch *format {
	case "json":
		return writeJSON(out, from, to, groups, results)
	case "csv":
		return writeCSV(out, groups, results)
	}

	const layout = "2006-01-02 15:04"
	switch {
	case until.set:
		fmt.Fprintf(out, "From %s to %s\n\n", from.Format(layout), to.Format(layout))
	case since.date.IsZero():
		fmt.Fprintf(out, "Since %s (last %s)\n\n", from.Format(layout), since)
	default:
		fmt.Fprintf(out, "Since %s\n\n", from.Format(layout))
	}
	if len(results[groups[0]]) == 0 {
		fmt.Fprintln(out, "No requests recorded.")
		return nil
	}
	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(out)
		}
		printTable(out, strings.ToUpper(g), results[g], groupings[g])
	}
	return nil
}

func writeJSON(out io.Writer, from, to time.Time, groups []string, results map[string][]store.Row) error {
	type row struct {
		Key          string  `json:"key"`
		Requests     int     `json:"requests"`
		Cached       int     `json:"cached"`
		InputTokens  int64   `json:"input_tokens"`
		OutputTokens int64   `json:"output_tokens"`
		CostUSD      float64 `json:"cost_usd"`
	}
	doc := struct {
		Since  time.Time        `json:"since"`
		Until  time.Time        `json:"until"`
		Groups map[string][]row `json:"groups"`
	}{Since: from, Until: to, Groups: make(map[string][]row, len(groups))}
	for _, g := range groups {
		rows := make([]row, 0, len(results[g]))
		for _, r := range results[g] {
			rows = append(rows, row(r))
		}
		doc.Groups[g] = rows
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

func writeCSV(out io.Writer, groups []string, results map[string][]store.Row) error {
	w := csv.NewWriter(out)
	w.Write([]string{"group", "key", "requests", "cached", "input_tokens", "output_tokens", "cost_usd"})
	for _, g := range groups {
		for _, r := range results[g] {
			w.Write([]string{g, r.Key, strconv.Itoa(r.Requests), strconv.Itoa(r.Cached),
				strconv.FormatInt(r.InputTokens, 10), strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatFloat(r.CostUSD, 'f', -1, 64)})
		}
	}
	w.Flush()
	return w.Error()
}

// printTable writes rows as an aligned table with a total line. Rows with an
// empty key are shown as blank.
func printTable(out io.Writer, title string, rows []store.Row, blank string) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\tREQUESTS\tCACHED\tINPUT\tOUTPUT\tCOST\n", title)
	var total store.Row
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t$%.6f\n",
			cmp.Or(r.Key, blank), r.Requests, r.Cached, r.InputTokens, r.OutputTokens, r.CostUSD)
		total.Requests += r.Requests
		total.Cached += r.Cached
		total.InputTokens += r.InputTokens
		total.OutputTokens += r.OutputTokens
		total.CostUSD += r.CostUSD
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%d\t%d\t$%.6f\n",
		total.Requests, total.Cached, total.InputTokens, total.OutputTokens, total.CostUSD)
	tw.Flush()
}
