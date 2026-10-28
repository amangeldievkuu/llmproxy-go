package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

const maxCacheEntries = 1000

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
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	items map[string]entry
	now   func() time.Time
}

func newCache(ttl time.Duration, max int) *cache {
	return &cache{ttl: ttl, max: max, items: make(map[string]entry), now: time.Now}
}

func (c *cache) get(key string) (entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return entry{}, false
	}
	if c.now().After(e.expires) {
		delete(c.items, key)
		return entry{}, false
	}
	return e, true
}

func (c *cache) set(key string, e entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		c.evict()
	}
	e.expires = c.now().Add(c.ttl)
	c.items[key] = e
}

// evict drops expired entries, then arbitrary ones until there is room for one more.
func (c *cache) evict() {
	now := c.now()
	for k, e := range c.items {
		if now.After(e.expires) {
			delete(c.items, k)
		}
	}
	for k := range c.items {
		if len(c.items) < c.max {
			break
		}
		delete(c.items, k)
	}
}

// cacheKey hashes the credentials, URL and body of r, then restores r.Body so
// the request can still be sent upstream. Credentials are part of the key so
// callers with different API keys never see each other's responses.
func cacheKey(r *http.Request) (string, error) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	h := sha256.New()
	for _, s := range []string{
		r.Header.Get("Authorization"),
		r.Header.Get("X-Api-Key"),
		r.Method,
		r.URL.RequestURI(),
	} {
		io.WriteString(h, s)
		h.Write([]byte{0})
	}
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)), nil
}
