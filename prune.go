package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"
)

func runPrune(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	dbPath := fs.String("db", envOr("LLMPROXY_DB", "llmproxy.db"), "SQLite file for usage records (env LLMPROXY_DB)")
	var olderThan age
	fs.Var(&olderThan, "older-than", "delete records older than this, e.g. 90d (required)")
	dryRun := fs.Bool("dry-run", false, "only report how many records would be deleted")
	fs.Parse(args)

	if olderThan <= 0 {
		return errors.New("prune needs -older-than, e.g. -older-than 90d")
	}
	st, err := openExisting(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	before := time.Now().Add(-time.Duration(olderThan))
	when := before.Format("2006-01-02 15:04")
	if *dryRun {
		n, err := st.CountBefore(ctx, before)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "would delete %d records older than %s\n", n, when)
		return nil
	}
	n, err := st.Prune(ctx, before)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "deleted %d records older than %s\n", n, when)
	if n > 0 {
		return st.Vacuum(ctx)
	}
	return nil
}
