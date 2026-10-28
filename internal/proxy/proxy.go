// Package proxy forwards requests to an LLM API and records the model, token
// usage, cost and latency of each one.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/amangeldievkuu/llmproxy/internal/pricing"
	"github.com/amangeldievkuu/llmproxy/internal/store"
	"github.com/amangeldievkuu/llmproxy/internal/usage"
)

// TagHeader labels a request (by feature, user, environment...) in the usage
// records. The proxy removes it before forwarding.
const TagHeader = "X-Llmproxy-Tag"

// Config configures a Proxy.
type Config struct {
	Upstream    *url.URL      // API to forward requests to, e.g. https://api.openai.com
	Store       *store.Store  // where to record requests; nil records nothing
	Prices      pricing.Table // nil uses pricing.Default()
	DailyBudget float64       // USD per calendar day before requests get 429; 0 disables
	CacheTTL    time.Duration // how long identical requests are served from memory; 0 disables
	Logger      *log.Logger   // nil uses log.Default()
}

// Proxy is an http.Handler that forwards requests to Config.Upstream.
type Proxy struct {
	cfg   Config
	rp    *httputil.ReverseProxy
	cache *cache
	log   *log.Logger
}

// New returns a Proxy for cfg.
func New(cfg Config) *Proxy {
	if cfg.Prices == nil {
		cfg.Prices = pricing.Default()
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	p := &Proxy{cfg: cfg, log: cfg.Logger}
	if cfg.CacheTTL > 0 {
		p.cache = newCache(cfg.CacheTTL, maxCacheEntries)
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(cfg.Upstream)
			// Ask for an uncompressed response so the body can be parsed.
			pr.Out.Header.Del("Accept-Encoding")
		},
		// Pass streamed (SSE) responses through as they arrive.
		FlushInterval: -1,
		ErrorLog:      cfg.Logger,
	}
	return p
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	tag := r.Header.Get(TagHeader)
	r.Header.Del(TagHeader)

	if p.overBudget(r.Context(), start) {
		msg := fmt.Sprintf("llmproxy daily budget of $%.2f reached", p.cfg.DailyBudget)
		writeError(w, http.StatusTooManyRequests, "budget_exceeded", msg)
		p.log.Printf("%s %s 429 budget exceeded", r.Method, r.URL.Path)
		return
	}

	var key string
	if p.cache != nil && r.Method == http.MethodPost {
		k, err := cacheKey(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "llmproxy could not read the request body")
			return
		}
		key = k
		if e, ok := p.cache.get(key); ok {
			e.writeTo(w)
			p.finish(r, start, tag, e.status, e.header.Get("Content-Type"), e.body, true)
			return
		}
	}

	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	p.rp.ServeHTTP(rec, r)

	body := rec.body.Bytes()
	if key != "" && rec.status == http.StatusOK && !rec.truncated {
		p.cache.set(key, entry{status: rec.status, header: rec.Header().Clone(), body: bytes.Clone(body)})
	}
	p.finish(r, start, tag, rec.status, rec.Header().Get("Content-Type"), body, false)
}

// finish records and logs a completed request. Cached responses cost nothing.
func (p *Proxy) finish(r *http.Request, start time.Time, tag string, status int, contentType string, body []byte, cached bool) {
	latency := time.Since(start)
	u, _ := usage.Parse(contentType, body)
	price, priced := p.cfg.Prices.Lookup(u.Model)
	var cost float64
	if priced && !cached {
		cost = price.Cost(u.InputTokens, u.OutputTokens)
	}

	if p.cfg.Store != nil {
		err := p.cfg.Store.Insert(context.WithoutCancel(r.Context()), store.Record{
			Time:         start,
			Method:       r.Method,
			Path:         r.URL.Path,
			Status:       status,
			Model:        u.Model,
			Tag:          tag,
			InputTokens:  u.InputTokens,
			OutputTokens: u.OutputTokens,
			CostUSD:      cost,
			Latency:      latency,
			Cached:       cached,
		})
		if err != nil {
			p.log.Printf("store: %v", err)
		}
	}

	var line strings.Builder
	fmt.Fprintf(&line, "%s %s %d", r.Method, r.URL.Path, status)
	if u.Model != "" {
		fmt.Fprintf(&line, " %s %d in / %d out", u.Model, u.InputTokens, u.OutputTokens)
		switch {
		case cached:
			line.WriteString(" cached")
		case priced:
			fmt.Fprintf(&line, " $%.6f", cost)
		default:
			line.WriteString(" (price unknown)")
		}
	}
	if tag != "" {
		fmt.Fprintf(&line, " tag=%s", tag)
	}
	fmt.Fprintf(&line, " %s", latency.Round(time.Millisecond))
	p.log.Print(line.String())
}

// overBudget reports whether today's spend has reached the daily budget.
// If the spend cannot be read, the request is let through.
func (p *Proxy) overBudget(ctx context.Context, now time.Time) bool {
	if p.cfg.DailyBudget <= 0 || p.cfg.Store == nil {
		return false
	}
	y, m, d := now.Date()
	spent, err := p.cfg.Store.SpentSince(ctx, time.Date(y, m, d, 0, 0, 0, 0, now.Location()))
	if err != nil {
		p.log.Printf("budget check: %v", err)
		return false
	}
	return spent >= p.cfg.DailyBudget
}

// writeError sends an error in the {"error": {"type", "message"}} shape that
// OpenAI and Anthropic clients already know how to read.
func writeError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"type": typ, "message": msg},
	})
}
