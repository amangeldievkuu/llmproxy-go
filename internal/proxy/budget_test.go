package proxy

import (
	"context"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

func newTestBudget(t *testing.T, limit float64) (*budget, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return newBudget(limit, st, log.New(io.Discard, "", 0)), st
}

func TestBudgetHoldsRunningRequests(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	b, _ := newTestBudget(t, 1)

	// With no history, a model is expected to cost nothing.
	release, err := b.admit(ctx, now, "m")
	if err != nil {
		t.Fatal(err)
	}
	if b.held != 0 {
		t.Errorf("held %v for a model with no history", b.held)
	}
	release(0.3) // teaches the average

	var releases []func(float64)
	for range 3 {
		release, err := b.admit(ctx, now, "m")
		if err != nil {
			t.Fatalf("request %d refused with $%.2f held", len(releases)+1, b.held)
		}
		releases = append(releases, release)
	}
	if _, err := b.admit(ctx, now, "m"); err != errOverBudget {
		t.Errorf("4th running request: got %v, want errOverBudget ($%.2f held)", err, b.held)
	}
	if _, err := b.admit(ctx, now, "other"); err != nil {
		t.Errorf("a model with no history should still fit while $%.2f of $1 is held: %v", b.held, err)
	}

	releases[0](0.3)
	if _, err := b.admit(ctx, now, "m"); err != nil {
		t.Errorf("released room was not reused: %v", err)
	}
}

func TestBudgetCountsRecordedSpend(t *testing.T) {
	ctx, now := context.Background(), time.Now()
	b, st := newTestBudget(t, 1)
	release, _ := b.admit(ctx, now, "m")
	release(0.5)

	if err := st.Insert(ctx, store.Record{Time: now, CostUSD: 0.6}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.admit(ctx, now, "m"); err != errOverBudget {
		t.Errorf("$0.60 spent + $0.50 expected fit in $1: %v", err)
	}
	if _, err := b.admit(ctx, now, "unknown"); err != nil {
		t.Errorf("$0.60 spent + $0 expected should fit in $1: %v", err)
	}
	if err := st.Insert(ctx, store.Record{Time: now, CostUSD: 0.4}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.admit(ctx, now, "unknown"); err != errOverBudget {
		t.Errorf("admitted with the whole budget spent: %v", err)
	}
}

func TestBudgetAverage(t *testing.T) {
	b, _ := newTestBudget(t, 100)
	for _, cost := range []float64{1, 3, 0, -1} { // free and failed requests are ignored
		release, err := b.admit(context.Background(), time.Now(), "m")
		if err != nil {
			t.Fatal(err)
		}
		release(cost)
	}
	if got := b.expected("m"); got != 2 {
		t.Errorf("expected cost %v, want 2", got)
	}
	for range 2 * recentRequests { // old costs fade out
		release, _ := b.admit(context.Background(), time.Now(), "m")
		release(10)
	}
	if got := b.expected("m"); got < 9 || got > 10 {
		t.Errorf("expected cost %v, want between 9 and 10", got)
	}
	if b.held != 0 || b.running != 0 {
		t.Errorf("held %v by %d requests after all were released", b.held, b.running)
	}
}

func TestBudgetStopsParallelBurst(t *testing.T) {
	// gpt-4o-mini: 10 in + 14 out tokens = $0.0000099 per request.
	const cost = 0.0000099
	var calls atomic.Int32
	release := make(chan struct{})
	proxyURL, st := setup(t, Config{DailyBudget: 4.5 * cost}, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			<-release // hold every request after the first open
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatResponse)
	})
	send := func() int {
		resp, err := http.Post(proxyURL+"/v1/chat/completions", "application/json",
			strings.NewReader(`{"model":"gpt-4o-mini"}`))
		if err != nil {
			t.Error(err)
			return 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}

	if send() != http.StatusOK { // teaches the average; $1 of 4.5 now spent
		t.Fatal("first request failed")
	}
	waitRequests(t, st, "model", 1)

	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for range 10 {
		wg.Go(func() {
			switch send() {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusTooManyRequests:
				refused.Add(1)
			}
		})
	}
	// Refused requests return at once; the admitted ones wait in the upstream.
	deadline := time.Now().Add(2 * time.Second)
	for refused.Load() < 7 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	// $1 spent + 3 running at $1 each = $4 fits in $4.50; a 4th would not.
	if ok.Load() != 3 || refused.Load() != 7 {
		t.Errorf("%d admitted and %d refused, want 3 and 7", ok.Load(), refused.Load())
	}
	rows := waitRequests(t, st, "model", 4)
	if len(rows) != 1 || rows[0].CostUSD > 4.5*cost {
		t.Errorf("spent over budget: %+v", rows)
	}
}
