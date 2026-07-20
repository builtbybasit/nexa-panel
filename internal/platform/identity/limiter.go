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
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxBuckets int
	buckets    map[string]attemptBucket
}

const defaultAttemptBucketCapacity = 4096

func newAttemptLimiter(limit int, window time.Duration) *attemptLimiter {
	return newAttemptLimiterWithCapacity(limit, window, defaultAttemptBucketCapacity)
}

func newAttemptLimiterWithCapacity(limit int, window time.Duration, maxBuckets int) *attemptLimiter {
	return &attemptLimiter{
		limit: limit, window: window, maxBuckets: maxBuckets,
		buckets: make(map[string]attemptBucket),
	}
}

func (l *attemptLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket, exists := l.buckets[key]
	if !exists || now.Sub(bucket.startedAt) >= l.window {
		if !exists {
			l.makeRoom(now)
		}
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

// makeRoom bounds attacker-controlled limiter state. Expired buckets are
// removed first; if every bucket is still live, the oldest one is evicted.
// Rate limiting is a protective control, so bounded memory is preferable to an
// unbounded map that can itself become a denial-of-service primitive.
func (l *attemptLimiter) makeRoom(now time.Time) {
	if len(l.buckets) < l.maxBuckets {
		return
	}
	for key, bucket := range l.buckets {
		if now.Sub(bucket.startedAt) >= l.window {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) < l.maxBuckets {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, bucket := range l.buckets {
		if oldestKey == "" || bucket.startedAt.Before(oldest) {
			oldestKey = key
			oldest = bucket.startedAt
		}
	}
	delete(l.buckets, oldestKey)
}
