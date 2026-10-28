// Package proxy forwards requests to an LLM API and records the model, token
// usage, cost and latency of each one.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/pricing"
	"github.com/amangeldievkuu/llmproxy-go/internal/store"
	"github.com/amangeldievkuu/llmproxy-go/internal/usage"
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
	StreamUsage bool          // add stream_options.include_usage to streamed Chat Completions requests
	Logger      *log.Logger   // nil uses log.Default()
}

// Proxy is an http.Handler that forwards requests to Config.Upstream.
type Proxy struct {
	cfg      Config
	rp       *httputil.ReverseProxy
	cache    *cache
	budget   *budget // nil without a daily budget
	log      *log.Logger
	inflight sync.WaitGroup
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
		p.cache = newCache(cfg.CacheTTL, maxCacheEntries, maxCacheBytes)
	}
	if cfg.DailyBudget > 0 && cfg.Store != nil {
		p.budget = newBudget(cfg.DailyBudget, cfg.Store, cfg.Logger)
	}
	// Every request goes to the same host, so keep more than the default two
	// idle connections to it and avoid a new TLS handshake per request.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 100
	p.rp = &httputil.ReverseProxy{
		Transport: transport,
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
	p.inflight.Add(1)
	defer p.inflight.Done()

	start := time.Now()
	tag := r.Header.Get(TagHeader)
	r.Header.Del(TagHeader)

	var key, model string
	var addedUsage bool
	addUsage := p.cfg.StreamUsage && strings.HasSuffix(r.URL.Path, "/chat/completions")
	if r.Method == http.MethodPost && (p.cache != nil || addUsage || p.budget != nil) {
		body, ok, err := readBody(r, maxReadBody)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "llmproxy could not read the request body")
			return
		}
		if ok && p.budget != nil {
			model = requestModel(body)
		}
		// Cache hits cost nothing, so they are served even over budget.
		if ok && p.cache != nil {
			key = cacheKey(r, body)
			if e, ok := p.cache.get(key); ok {
				e.writeTo(w)
				u, _ := usage.Parse(e.header.Get("Content-Type"), e.body)
				p.finish(r, start, tag, e.status, u, true)
				return
			}
		}
		if ok && addUsage {
			if b, changed := addStreamUsage(r.URL.Path, body); changed {
				r.Body = io.NopCloser(bytes.NewReader(b))
				r.ContentLength = int64(len(b))
				addedUsage = true
			}
		}
	}

	release := func(float64) {}
	if p.budget != nil {
		var err error
		if release, err = p.budget.admit(r.Context(), start, model); err != nil {
			msg := fmt.Sprintf("llmproxy daily budget of $%.2f reached", p.cfg.DailyBudget)
			writeError(w, http.StatusTooManyRequests, "budget_exceeded", msg)
			p.log.Printf("%s %s 429 budget exceeded", r.Method, r.URL.Path)
			return
		}
	}

	rec := &recorder{ResponseWriter: w, status: http.StatusOK, hideUsageChunk: addedUsage}
	if key != "" {
		rec.cacheMax = maxCacheEntrySize
	}
	p.rp.ServeHTTP(rec, r)
	rec.flush()

	if key != "" && rec.status == http.StatusOK && !rec.truncated {
		p.cache.set(key, entry{status: rec.status, header: rec.Header().Clone(), body: bytes.Clone(rec.body.Bytes())})
	}
	// Release only after the cost is recorded, so it is never counted zero times.
	release(p.finish(r, start, tag, rec.status, rec.usage(), false))
}

// requestModel returns the "model" field of a JSON request body, or "".
func requestModel(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &req)
	return req.Model
}

// Wait blocks until every request being served has finished and been recorded.
// Call it after http.Server.Shutdown and before closing the store.
func (p *Proxy) Wait() {
	p.inflight.Wait()
}

// finish records and logs a completed request and returns its cost. Cached
// responses cost nothing.
func (p *Proxy) finish(r *http.Request, start time.Time, tag string, status int, u usage.Usage, cached bool) float64 {
	latency := time.Since(start)
	price, priced := p.cfg.Prices.Lookup(u.Model)
	var cost float64
	if priced && !cached {
		cost = price.Cost(u)
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
	return cost
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
