package tests

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pippellia-btc/rely/v2/internal/spam"
)

func TestSpamManagementStress(t *testing.T) {
	if os.Getenv("RUN_SPAM_STRESS") == "" {
		t.Skip("set RUN_SPAM_STRESS=1 to run the spam management stress test")
	}

	clientWorkers := envInt(t, "SPAM_CLIENT_WORKERS", 64)
	clientBurst := envInt(t, "SPAM_CLIENT_BURST", 3)
	clientRefill := envDuration(t, "SPAM_CLIENT_REFILL", 1200*time.Millisecond)
	peerWorkers := envInt(t, "SPAM_PEER_WORKERS", 64)
	peerBurst := envInt(t, "SPAM_PEER_BURST", 3)
	peerRefill := envDuration(t, "SPAM_PEER_REFILL", 1200*time.Millisecond)

	t.Run("client-buckets", func(t *testing.T) {
		limiter := spam.NewRateLimiter(1, 1)
		accepted, rejected := stressLimiterBuckets(t, limiter.AllowClient, "client", clientWorkers, clientBurst, clientRefill)

		expectedAccepted := int64(clientWorkers * 2)
		expectedRejected := int64(clientWorkers * (clientBurst - 1))
		if got := accepted.Load(); got != expectedAccepted {
			t.Fatalf("client accepted = %d, want %d", got, expectedAccepted)
		}
		if got := rejected.Load(); got != expectedRejected {
			t.Fatalf("client rejected = %d, want %d", got, expectedRejected)
		}
	})

	t.Run("peer-buckets", func(t *testing.T) {
		limiter := spam.NewRateLimiter(1, 1)
		accepted, rejected := stressLimiterBuckets(t, limiter.AllowPeer, "peer", peerWorkers, peerBurst, peerRefill)

		expectedAccepted := int64(peerWorkers * 2)
		expectedRejected := int64(peerWorkers * (peerBurst - 1))
		if got := accepted.Load(); got != expectedAccepted {
			t.Fatalf("peer accepted = %d, want %d", got, expectedAccepted)
		}
		if got := rejected.Load(); got != expectedRejected {
			t.Fatalf("peer rejected = %d, want %d", got, expectedRejected)
		}
	})
}

func stressLimiterBuckets(
	t *testing.T,
	allow func(string) bool,
	prefix string,
	workers int,
	burst int,
	refill time.Duration,
) (*atomic.Int64, *atomic.Int64) {
	t.Helper()

	var accepted atomic.Int64
	var rejected atomic.Int64

	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		workerID := fmt.Sprintf("%s-%03d", prefix, i)
		go func(id string) {
			defer wg.Done()
			if err := stressLimiterWorker(allow, id, burst, refill, &accepted, &rejected); err != nil {
				errCh <- err
			}
		}(workerID)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	return &accepted, &rejected
}

func stressLimiterWorker(
	allow func(string) bool,
	id string,
	burst int,
	refill time.Duration,
	accepted *atomic.Int64,
	rejected *atomic.Int64,
) error {
	for i := 0; i < burst; i++ {
		if allow(id) {
			accepted.Add(1)
		} else {
			rejected.Add(1)
		}
	}

	time.Sleep(refill)

	if allow(id) {
		accepted.Add(1)
	} else {
		rejected.Add(1)
	}

	return nil
}
