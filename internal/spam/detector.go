package spam

import (
	"sync"
	"time"
)

type RateLimiter struct {
	clientRate int
	peerRate   int
	clients    map[string]*tokenBucket
	peers      map[string]*tokenBucket
	mu         sync.Mutex
}

func NewRateLimiter(clientEventsPerSec, peerAnnouncesPerSec int) *RateLimiter {
	return &RateLimiter{
		clientRate: clientEventsPerSec,
		peerRate:   peerAnnouncesPerSec,
		clients:    make(map[string]*tokenBucket),
		peers:      make(map[string]*tokenBucket),
	}
}

func (rl *RateLimiter) AllowClient(id string) bool {
	return rl.limiterFor(rl.clients, id, rl.clientRate).Allow()
}

func (rl *RateLimiter) AllowPeer(id string) bool {
	return rl.limiterFor(rl.peers, id, rl.peerRate).Allow()
}

func (rl *RateLimiter) limiterFor(m map[string]*tokenBucket, id string, perSec int) *tokenBucket {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if bucket, ok := m[id]; ok {
		return bucket
	}

	burst := perSec
	if burst < 1 {
		burst = 1
	}

	bucket := newTokenBucket(perSec, burst)
	m[id] = bucket
	return bucket
}

type tokenBucket struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	lastSeen time.Time
}

func newTokenBucket(perSec, burst int) *tokenBucket {
	return &tokenBucket{
		rate:     float64(perSec),
		burst:    float64(burst),
		tokens:   float64(burst),
		lastSeen: time.Now(),
	}
}

func (tb *tokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastSeen).Seconds()
	tb.lastSeen = now

	if tb.rate > 0 {
		tb.tokens += elapsed * tb.rate
		if tb.tokens > tb.burst {
			tb.tokens = tb.burst
		}
	}

	if tb.tokens < 1 {
		return false
	}

	tb.tokens--
	return true
}
