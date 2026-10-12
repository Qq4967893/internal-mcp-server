package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	mu        sync.Mutex
	requests  int
	burst     int
	perSecond float64
	buckets   map[string]*bucket
	now       func() time.Time
}

type bucket struct {
	tokens  float64
	updated time.Time
}

func New(requestsPerMinute, burst int) *Limiter {
	return &Limiter{
		requests:  requestsPerMinute,
		burst:     burst,
		perSecond: float64(requestsPerMinute) / 60,
		buckets:   make(map[string]*bucket),
		now:       time.Now,
	}
}

func (l *Limiter) Allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	current, ok := l.buckets[key]
	if !ok {
		current = &bucket{tokens: float64(l.burst), updated: now}
		l.buckets[key] = current
	} else {
		elapsed := now.Sub(current.updated).Seconds()
		if elapsed > 0 {
			current.tokens += elapsed * l.perSecond
		}
	}
	if current.tokens > float64(l.burst) {
		current.tokens = float64(l.burst)
	}
	current.updated = now

	if current.tokens < 1 {
		return false
	}
	current.tokens--
	return true
}
