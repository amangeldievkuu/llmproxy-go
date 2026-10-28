package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	maxCacheEntries   = 1000
	maxCacheBytes     = 256 << 20 // total size of cached bodies
	maxCacheEntrySize = 2 << 20   // larger responses are not cached
	maxCachedRequest  = 4 << 20   // larger requests bypass the cache
)

// keyHeaders are the request headers that are part of the cache key: the
// credentials of each provider, so callers with different API keys never see
// each other's responses, and headers that change what the API returns.
var keyHeaders = []string{
	"Authorization",
	"X-Api-Key",      // Anthropic
	"Api-Key",        // Azure OpenAI
	"X-Goog-Api-Key", // Google
	"Anthropic-Version",
	"Anthropic-Beta",
	"OpenAI-Organization",
	"OpenAI-Project",
}

// entry is a cached upstream response.
type entry struct {
	status  int
	header  http.Header
	body    []byte
	expires time.Time
}

func (e entry) writeTo(w http.ResponseWriter) {
	for k, v := range e.header {
		w.Header()[k] = slices.Clone(v)
	}
	w.Header().Set("X-Llmproxy-Cache", "hit")
	w.WriteHeader(e.status)
	w.Write(e.body)
}

// cache is an in-memory response cache with a fixed time to live.
type cache struct {
	mu       sync.Mutex
	ttl      time.Duration
	max      int // entries
	maxBytes int // total body bytes
	size     int // current total body bytes
	items    map[string]entry
	now      func() time.Time
}

func newCache(ttl time.Duration, max, maxBytes int) *cache {
	return &cache{ttl: ttl, max: max, maxBytes: maxBytes, items: make(map[string]entry), now: time.Now}
}

func (c *cache) get(key string) (entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return entry{}, false
	}
	if c.now().After(e.expires) {
		c.remove(key)
		return entry{}, false
	}
	return e, true
}

func (c *cache) set(key string, e entry) {
	if len(e.body) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.remove(key)
	c.evict(len(e.body))
	e.expires = c.now().Add(c.ttl)
	c.items[key] = e
	c.size += len(e.body)
}

func (c *cache) remove(key string) {
	if e, ok := c.items[key]; ok {
		c.size -= len(e.body)
		delete(c.items, key)
	}
}

// evict drops expired entries, then arbitrary ones until there is room for
// one more entry of n bytes.
func (c *cache) evict(n int) {
	full := func() bool { return len(c.items) >= c.max || c.size+n > c.maxBytes }
	if !full() {
		return
	}
	now := c.now()
	for k, e := range c.items {
		if now.After(e.expires) {
			c.remove(k)
		}
	}
	for k := range c.items {
		if !full() {
			break
		}
		c.remove(k)
	}
}

// cacheKey hashes the key headers, URL and body of r, then restores r.Body so
// the request can still be sent upstream. It reports false, leaving r able to
// be forwarded, when the body is too large to cache.
func cacheKey(r *http.Request) (string, bool, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCachedRequest+1))
	if err != nil {
		r.Body.Close()
		return "", false, err
	}
	if len(body) > maxCachedRequest {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), r.Body), r.Body}
		return "", false, nil
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	h := sha256.New()
	for _, name := range keyHeaders {
		io.WriteString(h, strings.Join(r.Header.Values(name), "\x00"))
		h.Write([]byte{0})
	}
	io.WriteString(h, r.Method)
	h.Write([]byte{0})
	io.WriteString(h, r.URL.RequestURI())
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), true, nil
}
