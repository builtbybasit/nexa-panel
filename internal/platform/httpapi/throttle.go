package httpapi

import (
	"sync"
	"time"
)

// IPThrottle is a fixed-window request limiter keyed on the trusted client IP.
// It is a transport-level, defense-in-depth control that survives independently
// of any per-account login limiter: it counts raw requests per source address so
// a single host cannot password-spray across unlimited usernames, and — being
// in-memory — it resets on restart just like the account limiter, so the nginx
// limit_req zone remains the durable first line of defense.
//
// State is attacker-controlled (the key is a client-supplied source address), so
// the bucket map is bounded and evicted like identity's attempt limiter rather
// than growing without limit and becoming its own denial-of-service primitive.
type IPThrottle struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	maxBuckets int
	buckets    map[string]throttleBucket
}

type throttleBucket struct {
	startedAt time.Time
	count     int
}

const defaultThrottleBucketCapacity = 8192

// NewIPThrottle builds a limiter permitting at most limit requests per window
// per key. A non-positive limit or window yields a permissive limiter that never
// rejects, so a misconfiguration fails open rather than locking operators out.
func NewIPThrottle(limit int, window time.Duration) *IPThrottle {
	return NewIPThrottleWithCapacity(limit, window, defaultThrottleBucketCapacity)
}

func NewIPThrottleWithCapacity(limit int, window time.Duration, maxBuckets int) *IPThrottle {
	if maxBuckets <= 0 {
		maxBuckets = defaultThrottleBucketCapacity
	}
	return &IPThrottle{
		limit:      limit,
		window:     window,
		maxBuckets: maxBuckets,
		buckets:    make(map[string]throttleBucket),
	}
}

// Allow records one request for key at now and reports whether it is within the
// window budget. An unlimited limiter (non-positive limit or window) always
// allows and never accumulates state.
func (t *IPThrottle) Allow(key string, now time.Time) bool {
	if t.limit <= 0 || t.window <= 0 {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	bucket, exists := t.buckets[key]
	if !exists || now.Sub(bucket.startedAt) >= t.window {
		if !exists {
			t.makeRoom(now)
		}
		t.buckets[key] = throttleBucket{startedAt: now, count: 1}
		return true
	}
	if bucket.count >= t.limit {
		return false
	}
	bucket.count++
	t.buckets[key] = bucket
	return true
}

// makeRoom bounds attacker-controlled limiter state. Expired buckets are removed
// first; if every bucket is still live, the oldest is evicted. A protective
// control must not itself grow into an unbounded-memory denial of service.
func (t *IPThrottle) makeRoom(now time.Time) {
	if len(t.buckets) < t.maxBuckets {
		return
	}
	for key, bucket := range t.buckets {
		if now.Sub(bucket.startedAt) >= t.window {
			delete(t.buckets, key)
		}
	}
	if len(t.buckets) < t.maxBuckets {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, bucket := range t.buckets {
		if oldestKey == "" || bucket.startedAt.Before(oldest) {
			oldestKey = key
			oldest = bucket.startedAt
		}
	}
	delete(t.buckets, oldestKey)
}
