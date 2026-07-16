package identity

import (
	"sync"
	"time"
)

type attemptBucket struct {
	startedAt time.Time
	count     int
}

type attemptLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]attemptBucket
}

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return &attemptLimiter{limit: limit, window: window, buckets: make(map[string]attemptBucket)}
}

func (l *attemptLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, exists := l.buckets[key]
	if !exists || now.Sub(bucket.startedAt) >= l.window {
		l.buckets[key] = attemptBucket{startedAt: now, count: 1}
		return true
	}
	if bucket.count >= l.limit {
		return false
	}
	bucket.count++
	l.buckets[key] = bucket
	return true
}

func (l *attemptLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}
