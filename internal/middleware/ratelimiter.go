package middleware

import (
	"net/http"
	"sync"
	"time"
)

type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

func newTokenBucket(maxTokens float64, refillRate float64) *tokenBucket {
	return &tokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens = min(tb.maxTokens, tb.tokens+elapsed*tb.refillRate)
	tb.lastRefill = now

	if tb.tokens >= 1 {
		tb.tokens--
		return true
	}
	return false
}

type RateLimiter struct {
	buckets    map[string]*tokenBucket
	mu         sync.Mutex
	maxTokens  float64
	refillRate float64
	cleanup    time.Duration
}

func NewRateLimiter(requestsPerMinute float64) *RateLimiter {
	rl := &RateLimiter{
		buckets:    make(map[string]*tokenBucket),
		maxTokens:  requestsPerMinute,
		refillRate: requestsPerMinute / 60.0, // tokens per second
		cleanup:    5 * time.Minute,
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) getOrCreate(ip string) *tokenBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if tb, ok := rl.buckets[ip]; ok {
		return tb
	}
	tb := newTokenBucket(rl.maxTokens, rl.refillRate)
	rl.buckets[ip] = tb
	return tb
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		for ip, tb := range rl.buckets {
			tb.mu.Lock()
			// remove buckets that are full (idle IPs)
			if tb.tokens >= tb.maxTokens {
				delete(rl.buckets, ip)
			}
			tb.mu.Unlock()
		}
		rl.mu.Unlock()
	}
}

func getIP(r *http.Request) string {
	// check X-Forwarded-For for proxied requests
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getIP(r)
		bucket := rl.getOrCreate(ip)

		if !bucket.allow() {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded","retry_after":60}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
