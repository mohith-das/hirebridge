package middleware

import (
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateEntry
}

type rateEntry struct {
	count    int
	resetAt  time.Time
}

func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{entries: make(map[string]*rateEntry)}
	go rl.reapLoop(5 * time.Minute)
	return rl
}

func (rl *RateLimiter) Allow(key string, maxReqs int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	e, ok := rl.entries[key]
	if !ok || now.After(e.resetAt) {
		rl.entries[key] = &rateEntry{count: 1, resetAt: now.Add(window)}
		return true
	}
	if e.count >= maxReqs {
		return false
	}
	e.count++
	return true
}

func (rl *RateLimiter) reapLoop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		rl.mu.Lock()
		now := time.Now()
		for k, e := range rl.entries {
			if now.After(e.resetAt) {
				delete(rl.entries, k)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) Handler(maxReqs int, window time.Duration, keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := keyFn(r)
			if !rl.Allow(key, maxReqs, window) {
				http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
