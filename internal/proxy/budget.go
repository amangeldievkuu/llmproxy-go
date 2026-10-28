package proxy

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/amangeldievkuu/llmproxy-go/internal/store"
)

// errOverBudget means a request does not fit in what is left of the budget.
var errOverBudget = errors.New("daily budget reached")

// recentRequests is roughly how many of a model's latest requests its
// expected cost is averaged over. Older costs are not dropped but fade out.
const recentRequests = 20

// budget enforces the daily budget. Besides today's recorded spend, it counts
// the expected cost of requests that are still running, so a burst of
// parallel requests cannot all get through before any of them is recorded.
// A request's expected cost is the average cost of its model's recent
// requests; a model with no history yet is expected to cost nothing.
type budget struct {
	limit float64
	store *store.Store
	log   *log.Logger

	mu      sync.Mutex
	held    float64          // expected cost of requests still running
	running int              // requests holding part of held
	avg     map[string]*mean // by the model name in the request
}

type mean struct {
	sum float64
	n   int
}

func newBudget(limit float64, st *store.Store, logger *log.Logger) *budget {
	return &budget{limit: limit, store: st, log: logger, avg: make(map[string]*mean)}
}

// admit lets a request for model through, or returns errOverBudget. Once the
// request has been recorded, call release with its actual cost. If today's
// spend cannot be read, the request is let through.
func (b *budget) admit(ctx context.Context, now time.Time, model string) (release func(cost float64), err error) {
	y, m, d := now.Date()
	spent, err := b.store.SpentSince(ctx, time.Date(y, m, d, 0, 0, 0, 0, now.Location()))
	checked := err == nil
	if !checked {
		b.log.Printf("budget check: %v", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	expected := b.expected(model)
	if checked && (spent+b.held >= b.limit || spent+b.held+expected > b.limit) {
		return nil, errOverBudget
	}
	b.held += expected
	b.running++
	return func(cost float64) { b.release(model, expected, cost) }, nil
}

func (b *budget) release(model string, expected, cost float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running--
	b.held -= expected
	if b.running == 0 {
		b.held = 0 // drop floating-point leftovers
	}
	// Unpriced, failed and cached requests cost nothing and say nothing about
	// what the next request will cost.
	if cost <= 0 || model == "" {
		return
	}
	a := b.avg[model]
	if a == nil {
		a = new(mean)
		b.avg[model] = a
	}
	if a.n == recentRequests {
		a.sum -= a.sum / float64(a.n)
		a.n--
	}
	a.sum += cost
	a.n++
}

func (b *budget) expected(model string) float64 {
	if a := b.avg[model]; a != nil && a.n > 0 {
		return a.sum / float64(a.n)
	}
	return 0
}
