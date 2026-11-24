package httpserver

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestLimiterConcurrencyCapacityAndExpiry(t *testing.T) {
	l := newRateLimiter(10, 15*time.Minute)
	now := time.Now()
	var wg sync.WaitGroup
	results := make(chan bool, 40)
	for range 40 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- l.allow("shared", now) }()
	}
	wg.Wait()
	close(results)
	allowed := 0
	for ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("allowed %d attempts", allowed)
	}
	if !l.allow("shared", now.Add(15*time.Minute)) {
		t.Fatal("limit did not expire")
	}
	for i := len(l.buckets); i < 4096; i++ {
		l.buckets[fmt.Sprint(i)] = bucket{until: now.Add(time.Hour)}
	}
	if l.allow("overflow", now) {
		t.Fatal("limiter exceeded capacity")
	}
}

func TestLimiterFullCapacityKeepsExistingBudgetsAndReclaimsExpiredKeys(t *testing.T) {
	l := newRateLimiter(2, time.Minute)
	now := time.Unix(1800000000, 0)
	for index := range 4096 {
		l.buckets[strconv.Itoa(index)] = bucket{until: now.Add(time.Minute)}
	}
	if l.allow("new", now) || !l.allow("0", now) || !l.allow("0", now) || l.allow("0", now) {
		t.Fatal("capacity changed existing budgets or admitted a new key")
	}
	if l.allow("0", now.Add(-time.Hour)) {
		t.Fatal("clock rollback reset an exhausted budget")
	}
	// An existing key can renew at capacity without exceeding the key bound.
	if !l.allow("0", now.Add(time.Minute)) || len(l.buckets) > 4096 {
		t.Fatal("existing key failed to renew within the key bound")
	}
	if !l.allow("new", now.Add(time.Minute)) || len(l.buckets) != 2 {
		t.Fatal("a new key failed to reclaim expired entries")
	}
	if !l.allow("0", now.Add(time.Minute)) || l.allow("0", now.Add(time.Minute)) {
		t.Fatal("cleanup reset an active key's remaining budget")
	}
}
