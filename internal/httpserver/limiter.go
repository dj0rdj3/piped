package httpserver

import (
	"sync"
	"time"
)

type bucket struct {
	attempts int
	until    time.Time
}

// Limiter caps both attempts and tracked keys. Callers resolve client IPs using
// the configured proxy trust boundary before choosing a rate-limit key.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]bucket
	maximum int
	window  time.Duration
}

func newRateLimiter(maximum int, window time.Duration) *limiter {
	return &limiter{buckets: make(map[string]bucket), maximum: maximum, window: window}
}

func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if !now.Before(b.until) {
			delete(l.buckets, k)
		}
	}
	b, exists := l.buckets[key]
	if !exists {
		if len(l.buckets) >= 4096 {
			return false
		}
		b.until = now.Add(l.window)
	}
	if b.attempts >= l.maximum {
		return false
	}
	b.attempts++
	l.buckets[key] = b
	return true
}
