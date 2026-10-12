package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterRefillsTokens(t *testing.T) {
	current := time.Unix(0, 0)
	limiter := New(60, 1)
	limiter.now = func() time.Time { return current }

	if !limiter.Allow("user") {
		t.Fatal("first request was denied")
	}
	if limiter.Allow("user") {
		t.Fatal("second request was allowed before refill")
	}
	current = current.Add(2 * time.Second)
	if !limiter.Allow("user") {
		t.Fatal("request was denied after refill")
	}
}
