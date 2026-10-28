package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amangeldievkuu/llmproxy/internal/pricing"
	"github.com/amangeldievkuu/llmproxy/internal/proxy"
	"github.com/amangeldievkuu/llmproxy/internal/store"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.String("port", envOr("PORT", "8000"), "port to listen on (env PORT)")
	upstream := fs.String("upstream", envOr("LLMPROXY_UPSTREAM", "https://api.openai.com"),
		"API to forward requests to (env LLMPROXY_UPSTREAM)")
	dbPath := fs.String("db", envOr("LLMPROXY_DB", "llmproxy.db"), "SQLite file for usage records (env LLMPROXY_DB)")
	budget := fs.Float64("daily-budget", 0, "answer 429 once this many USD are spent today (0 = no limit)")
	cacheTTL := fs.Duration("cache-ttl", 0, "serve identical requests from memory for this long, e.g. 10m (0 = off)")
	pricesPath := fs.String("prices", "", "JSON file with extra or overriding model prices")
	fs.Parse(args)

	target, err := url.Parse(*upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("invalid -upstream %q", *upstream)
	}
	prices, err := pricing.Load(*pricesPath)
	if err != nil {
		return err
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	srv := &http.Server{
		Addr: ":" + *port,
		Handler: proxy.New(proxy.Config{
			Upstream:    target,
			Store:       st,
			Prices:      prices,
			DailyBudget: *budget,
			CacheTTL:    *cacheTTL,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// On Ctrl+C, stop accepting connections and let in-flight requests finish
	// (and get recorded) before the database closes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("llmproxy listening on http://localhost:%s -> %s (db %s)", *port, target, *dbPath)
	if *budget > 0 {
		log.Printf("daily budget: $%.2f", *budget)
	}
	if *cacheTTL > 0 {
		log.Printf("response cache: %s", *cacheTTL)
	}

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}
