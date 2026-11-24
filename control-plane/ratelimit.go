package main

import (
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"
)

// Limiter: per-API-key requests-per-minute (token bucket) and a cap on requests in flight.
// In-process; with several server instances this moves to Redis (see .planning/GAPS.md G23).
type Limiter struct {
	rpm, burst, maxInFlight float64
	mu                      sync.Mutex
	keys                    map[string]*bucket
}

type bucket struct {
	tokens   float64
	last     time.Time
	inFlight int
}

func NewLimiter(rpm float64, maxInFlight int) *Limiter {
	return &Limiter{rpm: rpm, burst: math.Max(rpm/4, 5), maxInFlight: float64(maxInFlight), keys: map[string]*bucket{}}
}

// Acquire returns a release func, or writes a 429 and returns nil.
func (l *Limiter) Acquire(w http.ResponseWriter, key string) func() {
	if l == nil || l.rpm <= 0 {
		return func() {}
	}
	l.mu.Lock()
	b := l.keys[key]
	now := time.Now()
	if b == nil {
		b = &bucket{tokens: l.burst, last: now}
		l.keys[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Minutes()*l.rpm)
	b.last = now
	if b.tokens < 1 {
		wait := int(math.Ceil((1 - b.tokens) / l.rpm * 60))
		l.mu.Unlock()
		w.Header().Set("Retry-After", fmt.Sprint(wait))
		apiError(w, 429, fmt.Sprintf("rate limit: %.0f requests a minute per key; retry in %d s", l.rpm, wait))
		return nil
	}
	if float64(b.inFlight) >= l.maxInFlight {
		l.mu.Unlock()
		w.Header().Set("Retry-After", "2")
		apiError(w, 429, fmt.Sprintf("too many requests at once: at most %.0f in flight per key", l.maxInFlight))
		return nil
	}
	b.tokens--
	b.inFlight++
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		b.inFlight--
		l.mu.Unlock()
	}
}
