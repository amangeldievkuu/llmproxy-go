package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

func runStats(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	dbPath := fs.String("db", envOr("LLMPROXY_DB", "llmproxy.db"), "SQLite file for usage records (env LLMPROXY_DB)")
	since := fs.Duration("since", 24*time.Hour, "how far back to look, e.g. 1h, 24h, 168h")
	fs.Parse(args)

	if _, err := os.Stat(*dbPath); err != nil {
		return fmt.Errorf("no database at %s (run \"llmproxy serve\" first)", *dbPath)
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	from := time.Now().Add(-*since)
	byModel, err := st.Summary(ctx, from, "model")
	if err != nil {
		return err
	}
	byTag, err := st.Summary(ctx, from, "tag")
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Since %s (last %s)\n\n", from.Format("2006-01-02 15:04"), *since)
	if len(byModel) == 0 {
		fmt.Fprintln(out, "No requests recorded.")
		return nil
	}
	printTable(out, "MODEL", byModel, "(unknown)")
	fmt.Fprintln(out)
	printTable(out, "TAG", byTag, "(none)")
	return nil
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
