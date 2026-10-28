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
	"syscall"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/pricing"
	"github.com/amangeldievkuu/llmproxy-go/internal/proxy"
	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

func runServe(args []string) error {
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

	p := proxy.New(proxy.Config{
		Upstream:    target,
		Store:       st,
		Prices:      prices,
		DailyBudget: *budget,
		CacheTTL:    *cacheTTL,
	})
	addr := net.JoinHostPort(*host, *port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// On Ctrl+C, stop accepting connections and let in-flight requests finish
	// (and get recorded) before the database closes. A second Ctrl+C exits at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			srv.Close() // cut off streams that are still open; they are recorded as they end
		}
		p.Wait()
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

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
