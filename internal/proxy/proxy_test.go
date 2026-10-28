package proxy

import (
	"bufio"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
	"github.com/amangeldievkuu/llmproxy-go/internal/usage"
)

const chatResponse = `{"model":"gpt-4o-mini-2024-07-18","usage":{"prompt_tokens":10,"completion_tokens":14}}`

// setup starts a fake upstream API running handler and a proxy in front of it.
func setup(t *testing.T, cfg Config, handler http.HandlerFunc) (proxyURL string, st *store.Store) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg.Upstream, _ = url.Parse(upstream.URL)
	cfg.Store = st
	cfg.Logger = log.New(io.Discard, "", 0)
	srv := httptest.NewServer(New(cfg))
	t.Cleanup(srv.Close)
	return srv.URL, st
}

func post(t *testing.T, rawURL string, header map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(`{"model":"gpt-4o-mini"}`))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// waitRequests polls the store until it holds n requests and returns the summary
// grouped by. Records are written just after the response reaches the client,
// so a test can otherwise read before the write lands.
func waitRequests(t *testing.T, st *store.Store, by string, n int) []store.Row {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err := st.Summary(context.Background(), time.Now().Add(-time.Hour), by)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, r := range rows {
			total += r.Requests
		}
		if total >= n || time.Now().After(deadline) {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestForwardsAndRecordsUsage(t *testing.T) {
	var gotTag, gotPath string
	proxyURL, st := setup(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTag = r.Header.Get(TagHeader)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatResponse)
	})

	resp, body := post(t, proxyURL+"/v1/chat/completions", map[string]string{TagHeader: "checkout"})
	if resp.StatusCode != http.StatusOK || body != chatResponse {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotTag != "" {
		t.Errorf("tag header leaked upstream: %q", gotTag)
	}

	rows := waitRequests(t, st, "tag", 1)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Key != "checkout" || r.InputTokens != 10 || r.OutputTokens != 14 || r.CostUSD <= 0 {
		t.Errorf("unexpected record %+v", r)
	}
}

func TestStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	proxyURL, st := setup(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"model\":\"gpt-4o-mini\",\"usage\":null}\n\n")
		w.(http.Flusher).Flush()
		<-release // hold the stream open until the client has seen the first event
		io.WriteString(w, "data: {\"model\":\"gpt-4o-mini\",\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
	})

	resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	first := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.HasPrefix(line, "data: ") {
			t.Errorf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event was buffered instead of streamed")
	}
	close(release)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := waitRequests(t, st, "model", 1)
	if len(rows) != 1 || rows[0].InputTokens != 3 || rows[0].OutputTokens != 2 {
		t.Errorf("stream usage not recorded correctly: %+v", rows)
	}
}

func TestDailyBudget(t *testing.T) {
	var calls atomic.Int32
	proxyURL, st := setup(t, Config{DailyBudget: 1}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, chatResponse)
	})

	if resp, _ := post(t, proxyURL+"/v1/chat/completions", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("under budget: got %d", resp.StatusCode)
	}
	waitRequests(t, st, "model", 1)

	err := st.Insert(context.Background(), store.Record{Time: time.Now(), Model: "gpt-4o", CostUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	resp, body := post(t, proxyURL+"/v1/chat/completions", nil)
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "budget_exceeded") {
		t.Errorf("over budget: got %d %q", resp.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Errorf("upstream called %d times, want 1", calls.Load())
	}
}

func TestCache(t *testing.T) {
	var calls atomic.Int32
	proxyURL, st := setup(t, Config{CacheTTL: time.Minute}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatResponse)
	})
	alice := map[string]string{"Authorization": "Bearer alice"}

	post(t, proxyURL+"/v1/chat/completions", alice)
	waitRequests(t, st, "model", 1) // the response is cached just before it is recorded
	resp, body := post(t, proxyURL+"/v1/chat/completions", alice)
	if body != chatResponse || resp.Header.Get("X-Llmproxy-Cache") != "hit" {
		t.Errorf("second request not served from cache: %q %v", body, resp.Header)
	}
	if calls.Load() != 1 {
		t.Errorf("upstream called %d times, want 1", calls.Load())
	}

	post(t, proxyURL+"/v1/chat/completions", map[string]string{"Authorization": "Bearer bob"})
	if calls.Load() != 2 {
		t.Error("a different API key was served another caller's cached response")
	}

	rows := waitRequests(t, st, "model", 3)
	if len(rows) != 1 || rows[0].Requests != 3 || rows[0].Cached != 1 {
		t.Errorf("unexpected summary %+v", rows)
	}
}

func TestCacheHitServedOverBudget(t *testing.T) {
	var calls atomic.Int32
	proxyURL, st := setup(t, Config{CacheTTL: time.Minute, DailyBudget: 1}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatResponse)
	})
	post(t, proxyURL+"/v1/chat/completions", nil)
	waitRequests(t, st, "model", 1)
	if err := st.Insert(context.Background(), store.Record{Time: time.Now(), CostUSD: 1}); err != nil {
		t.Fatal(err)
	}

	if resp, _ := post(t, proxyURL+"/v1/chat/completions", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("cache hit over budget: got %d, want 200", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodPost, proxyURL+"/v1/chat/completions", strings.NewReader(`{"other":1}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || calls.Load() != 1 {
		t.Errorf("uncached request over budget: got %d after %d upstream calls", resp.StatusCode, calls.Load())
	}
}

func TestCacheKeyedByProviderHeaders(t *testing.T) {
	var calls atomic.Int32
	proxyURL, _ := setup(t, Config{CacheTTL: time.Minute}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatResponse)
	})
	for _, h := range []map[string]string{
		{"Api-Key": "azure-alice"},
		{"Api-Key": "azure-bob"},
		{"Api-Key": "azure-bob", "Anthropic-Beta": "some-feature"},
	} {
		post(t, proxyURL+"/v1/chat/completions", h)
	}
	if calls.Load() != 3 {
		t.Errorf("upstream called %d times, want 3", calls.Load())
	}
}

func TestLargeRequestBypassesCache(t *testing.T) {
	big := `{"model":"gpt-4o-mini","pad":"` + strings.Repeat("x", maxReadBody) + `"}`
	var got atomic.Int64
	proxyURL, _ := setup(t, Config{CacheTTL: time.Minute}, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(int64(len(b)))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatResponse)
	})
	for range 2 {
		resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", strings.NewReader(big))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Header.Get("X-Llmproxy-Cache") != "" || got.Load() != int64(len(big)) {
			t.Fatalf("request body not forwarded intact (%d of %d bytes) or served from cache", got.Load(), len(big))
		}
	}
}

func TestRecordsUsageAfterLongStream(t *testing.T) {
	proxyURL, st := setup(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 1<<20) + "\"}}]}\n\n"
		for range 12 { // more than maxRecordedBody in total
			io.WriteString(w, chunk)
		}
		io.WriteString(w, "data: {\"model\":\"gpt-4o-mini\",\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
	})
	resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	rows := waitRequests(t, st, "model", 1)
	if len(rows) != 1 || rows[0].Key != "gpt-4o-mini" || rows[0].OutputTokens != 2 {
		t.Errorf("usage at the end of a long stream was lost: %+v", rows)
	}
}

func TestRecorderStopsAtLimit(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json")
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	rec.Write(make([]byte, maxRecordedBody))
	rec.Write([]byte("x")) // over the limit
	rec.Write([]byte(chatResponse))
	if !rec.truncated || rec.body.Len() != 0 || rec.usage() != (usage.Usage{}) {
		t.Errorf("truncated=%v kept=%d usage=%+v", rec.truncated, rec.body.Len(), rec.usage())
	}
}

func TestCacheEvictsBySize(t *testing.T) {
	c := newCache(time.Minute, 10, 100)
	c.set("a", entry{body: make([]byte, 60)})
	c.set("b", entry{body: make([]byte, 60)})
	if _, ok := c.get("b"); !ok {
		t.Error("newest entry missing")
	}
	if _, ok := c.get("a"); ok || c.size != 60 {
		t.Errorf("cache over its byte limit: size %d", c.size)
	}
	c.set("big", entry{body: make([]byte, 101)})
	if _, ok := c.get("big"); ok {
		t.Error("entry larger than the cache was stored")
	}
}

const (
	contentChunk = "data: {\"model\":\"gpt-4o-mini\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}],\"usage\":null}\n\n"
	usageChunk   = "data: {\"model\":\"gpt-4o-mini\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n"
	doneChunk    = "data: [DONE]\n\n"
)

// fakeChat answers like OpenAI Chat Completions: it streams when asked, and
// adds a usage chunk only when the request sets include_usage. It sends each
// request body it receives on bodies.
func fakeChat(bodies chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies <- string(b)
		if !strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, chatResponse)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, contentChunk)
		if strings.Contains(string(b), `"include_usage":true`) {
			io.WriteString(w, usageChunk)
		}
		io.WriteString(w, doneChunk)
	}
}

func postBody(t *testing.T, rawURL, body string) string {
	t.Helper()
	resp, err := http.Post(rawURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestStreamUsageAdded(t *testing.T) {
	bodies := make(chan string, 1)
	proxyURL, st := setup(t, Config{StreamUsage: true}, fakeChat(bodies))

	got := postBody(t, proxyURL+"/v1/chat/completions",
		`{"model":"gpt-4o-mini","stream":true,"stream_options":{"other":1},"messages":[{"role":"user","content":"<b>&</b>"}]}`)
	sent := <-bodies
	for _, want := range []string{`"include_usage":true`, `"other":1`, `"content":"<b>&</b>"`} {
		if !strings.Contains(sent, want) {
			t.Errorf("upstream request %s is missing %s", sent, want)
		}
	}
	if got != contentChunk+doneChunk {
		t.Errorf("client got %q, want the stream without the usage chunk", got)
	}

	rows := waitRequests(t, st, "model", 1)
	if len(rows) != 1 || rows[0].InputTokens != 3 || rows[0].OutputTokens != 2 || rows[0].CostUSD <= 0 {
		t.Errorf("stream usage not recorded: %+v", rows)
	}
}

func TestStreamUsageLeftAlone(t *testing.T) {
	tests := []struct {
		name, path, body string
		streamUsage      bool
		wantUsageChunk   bool
	}{
		{"app asks for usage", "/v1/chat/completions",
			`{"stream":true,"stream_options":{"include_usage":true}}`, true, true},
		{"app declines usage", "/v1/chat/completions",
			`{"stream":true,"stream_options":{"include_usage":false}}`, true, false},
		{"not streamed", "/v1/chat/completions", `{"stream":false}`, true, false},
		{"other endpoint", "/v1/messages", `{"stream":true}`, true, false},
		{"turned off", "/v1/chat/completions", `{"stream":true}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodies := make(chan string, 1)
			proxyURL, _ := setup(t, Config{StreamUsage: tt.streamUsage}, fakeChat(bodies))
			got := postBody(t, proxyURL+tt.path, tt.body)
			if sent := <-bodies; sent != tt.body {
				t.Errorf("request body changed: %s", sent)
			}
			if strings.Contains(got, usageChunk) != tt.wantUsageChunk {
				t.Errorf("client got %q", got)
			}
		})
	}
}

func TestEventFilter(t *testing.T) {
	crlf := strings.ReplaceAll(usageChunk, "\n", "\r\n")
	stream := contentChunk + crlf + "event: ping\ndata: {}\n\n" + usageChunk + doneChunk + "data: {\"unfinished\""
	var out strings.Builder
	f := &eventFilter{send: func(b []byte) (int, error) { return out.Write(b) }}
	for i := 0; i < len(stream); i += 5 { // events arrive split at arbitrary points
		if err := f.Write([]byte(stream[i:min(i+5, len(stream))])); err != nil {
			t.Fatal(err)
		}
	}
	f.flush()
	want := contentChunk + "event: ping\ndata: {}\n\n" + doneChunk + "data: {\"unfinished\""
	if out.String() != want {
		t.Errorf("got %q\nwant %q", out.String(), want)
	}
}
