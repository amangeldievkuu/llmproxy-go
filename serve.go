package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/pricing"
	"github.com/amangeldievkuu/llmproxy-go/internal/proxy"
	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

// runServe runs the proxy until Ctrl+C or SIGTERM. A second signal exits at once.
func runServe(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore the default handling, so another Ctrl+C kills the process
	}()
	return serve(ctx, args, nil)
}

// serve runs the proxy until ctx ends, then stops accepting connections and
// lets open requests finish (and get recorded) before the database closes.
// If ready is not nil, it is called with the address once the proxy is listening.
func serve(ctx context.Context, args []string, ready func(addr string)) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", envOr("LLMPROXY_HOST", "127.0.0.1"),
		"address to listen on; 0.0.0.0 exposes the proxy to your network (env LLMPROXY_HOST)")
	port := fs.String("port", envOr("LLMPROXY_PORT", "8000"), "port to listen on (env LLMPROXY_PORT)")
	upstream := fs.String("upstream", envOr("LLMPROXY_UPSTREAM", "https://api.openai.com"),
		"API to forward requests to (env LLMPROXY_UPSTREAM)")
	dbPath := fs.String("db", envOr("LLMPROXY_DB", "llmproxy.db"), "SQLite file for usage records (env LLMPROXY_DB)")
	budget := fs.Float64("daily-budget", 0, "answer 429 once this many USD are spent today (0 = no limit)")
	cacheTTL := fs.Duration("cache-ttl", 0, "serve identical requests from memory for this long, e.g. 10m (0 = off)")
	pricesPath := fs.String("prices", "", "JSON file with extra or overriding model prices")
	streamUsage := fs.Bool("stream-usage", true,
		"ask for token usage in streamed OpenAI Chat Completions requests that don't, so they can be priced")
	var retention age
	if v := os.Getenv("LLMPROXY_RETENTION"); v != "" {
		if err := retention.Set(v); err != nil {
			return fmt.Errorf("LLMPROXY_RETENTION: %w", err)
		}
	}
	fs.Var(&retention, "retention",
		"delete records older than this at startup and daily, e.g. 90d (0 = keep all; env LLMPROXY_RETENTION)")
	fs.Parse(args)

	target, err := url.Parse(*upstream)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("invalid -upstream %q", *upstream)
	}
	if target.Scheme == "http" && !isLoopback(target.Hostname()) {
		log.Printf("warning: -upstream %s is plain http, so API keys are sent unencrypted", target)
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

	var pruner sync.WaitGroup
	if keep := time.Duration(retention); keep > 0 {
		pruneOld(st, keep)
		pruner.Go(func() {
			tick := time.NewTicker(24 * time.Hour)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					pruneOld(st, keep)
				}
			}
		})
	}

	p := proxy.New(proxy.Config{
		Upstream:    target,
		Store:       st,
		Prices:      prices,
		DailyBudget: *budget,
		CacheTTL:    *cacheTTL,
		StreamUsage: *streamUsage,
	})
	ln, err := net.Listen("tcp", net.JoinHostPort(*host, *port))
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close() // cut off streams that are still open; they are recorded as they end
		}
		p.Wait()
		pruner.Wait()
	}()

	log.Printf("llmproxy listening on http://%s -> %s (db %s)", addr, target, *dbPath)
	if !isLoopback(*host) {
		log.Printf("warning: -host %s accepts connections from other machines, and the proxy has no authentication", *host)
	}
	if *budget > 0 {
		log.Printf("daily budget: $%.2f", *budget)
	}
	if *cacheTTL > 0 {
		log.Printf("response cache: %s", *cacheTTL)
	}
	if retention > 0 {
		log.Printf("keeping records for %s", retention)
	}

	if ready != nil {
		ready(addr)
	}

	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Print("shutting down, waiting for open requests (Ctrl+C again to quit now)")
	<-done
	return nil
}

// isLoopback reports whether host names this machine.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pruneOld deletes records older than keep and logs how many it deleted.
func pruneOld(st *store.Store, keep time.Duration) {
	n, err := st.Prune(context.Background(), time.Now().Add(-keep))
	switch {
	case err != nil:
		log.Printf("retention: %v", err)
	case n > 0:
		log.Printf("retention: deleted %d records older than %s", n, age(keep))
	}
}
