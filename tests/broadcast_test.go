package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/tests/swarm"
	"github.com/pippellia-btc/rely/v2/utils"
)

// BroadcastConfig holds the configuration for the broadcast test.
type BroadcastConfig struct {
	Address       string
	TestDuration  time.Duration
	RelayDuration time.Duration
	SwarmDuration time.Duration
	ClientBuffer  int
	Swarm         swarm.Config
}

func defaultBroadcastConfig() BroadcastConfig {
	d := 60 * time.Second
	return BroadcastConfig{
		Address:       "localhost:3334",
		TestDuration:  d,
		RelayDuration: d - 5*time.Second,
		SwarmDuration: d - 10*time.Second,
		ClientBuffer:  50, // tiny buffer to trigger backpressure
		Swarm: swarm.Config{
			ConnectionFrequency: time.Millisecond,
			Client:              swarm.DefaultClientConfig(),
		},
	}
}

var (
	testEvent = nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
	}

	testFilter = nostr.Filter{
		Kinds: []int{1},
	}
)

// TestBroadcast runs an end-to-end test aimed at stressing the relay broadcasting system.
// To achieve maximum stress, the clients will request empty filters (i.e. matching any event),
// and will have a small response buffer.
func TestBroadcast(t *testing.T) {
	config := defaultBroadcastConfig()
	start := time.Now()
	processed := atomic.Int64{}

	ctx, cancel := context.WithTimeout(context.Background(), config.TestDuration)
	defer cancel()

	logger, err := newFileLogger("test.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	relay := rely.NewRelay(
		rely.WithLogger(logger.Logger),
		rely.WithClientResponseLimit(config.ClientBuffer),
	)

	relay.Reject.Event.Clear() // accept all events, even if the signature doesn't match

	relay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		processed.Add(1)
		return rely.Success()
	}
	relay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		processed.Add(1)
		return repeat(testEvent, config.ClientBuffer), nil // make sure the client buffers are full
	}

	addr, cleanup := startRelayOnListener(t, relay)
	defer cleanup()

	// Step 2: create the swarm with fuzzy client behaviors.
	swarm, err := swarm.New(config.Swarm, swarm.BehaviorDistribution{
		{P: 0.2, Behavior: simpleEventClient{testEvent}},
		{P: 0.8, Behavior: simpleReqClient{testFilter}},
	})
	if err != nil {
		t.Fatalf("failed to create swarm: %v", err)
	}

	// Step 3: run everything.
	go func() {
		ctx, cancel := context.WithTimeout(ctx, config.RelayDuration)
		defer cancel()
		go displayStats(ctx, "broadcast", start, &processed, relay, swarm)
	}()

	go func() {
		ctx, cancel := context.WithTimeout(ctx, config.SwarmDuration)
		defer cancel()
		swarm.Run(ctx, addr)
	}()

	go func() { http.ListenAndServe(":6060", nil) }() // pprof

	select {
	case err := <-swarm.Err():
		t.Fatalf("swarm error: %v", err)

	case <-ctx.Done():
		// test passed, print stats last time
		clearScreen()
		printStats("broadcast", start, &processed, relay, swarm)
	}
}

// simpleEventClient implements the [swarm.Behaviour] interface.
// It sends the given EVENT and validates that the responses it receives
// have the expected labels ("OK", "NOTICE").
type simpleEventClient struct {
	e nostr.Event
}

func (c simpleEventClient) NextRequest() []byte {
	event := []any{"EVENT", c.e}
	data, err := json.Marshal(event)
	if err != nil {
		panic(fmt.Errorf("simpleEventClient: failed to marshal event %v: %w", event, err))
	}
	return data
}

func (c simpleEventClient) ValidateResponse(d *json.Decoder) error {
	return validateLabel("OK", "NOTICE")(d)
}

// simpleReqClient implements the [swarm.Behaviour] interface.
// It sends the given filter in REQ requests and validates that the responses it receives
// have the expected labels ("EOSE", "CLOSED", "EVENT", "NOTICE").
type simpleReqClient struct {
	f nostr.Filter
}

func (c simpleReqClient) NextRequest() []byte {
	req := []any{"REQ", utils.RandomString(), c.f}
	data, err := json.Marshal(req)
	if err != nil {
		panic(fmt.Errorf("simpleReqClient: failed to marshal req %v: %w", req, err))
	}
	return data
}

func (c simpleReqClient) ValidateResponse(d *json.Decoder) error {
	return validateLabel("EOSE", "CLOSED", "EVENT", "NOTICE")(d)
}

func repeat[T any](e T, n int) []T {
	s := make([]T, n)
	for i := range s {
		s[i] = e
	}
	return s
}

func startRelayOnListener(t testing.TB, relay *rely.Relay) (string, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	relay.Start(ctx)

	server := &http.Server{Handler: relay}
	go func() { _ = server.Serve(ln) }()

	cleanup := func() {
		cancel()
		_ = server.Close()
		_ = ln.Close()
		relay.Wait()
	}

	return "ws://" + ln.Addr().String(), cleanup
}
