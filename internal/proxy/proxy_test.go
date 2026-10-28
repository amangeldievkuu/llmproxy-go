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

	"github.com/amangeldievkuu/llmproxy/internal/store"
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
