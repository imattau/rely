package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	rely "github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/internal/storage"
)

func BenchmarkRelayConcurrentIngest(b *testing.B) {
	for _, workers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.ReportAllocs()
			addr, cleanup := startRelayTrafficBenchmark(b, func(c rely.Client, event *nostr.Event) rely.EventResult {
				return rely.Success()
			}, func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
				_ = ctx
				_ = c
				_ = id
				_ = filters
				return nil, nil
			})
			defer cleanup()

			pub := newPublisherPool(b, addr, workers)
			defer pub.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pub.Round(b, i)
			}
		})
	}
}

func BenchmarkRelayConcurrentIngestWithStorage(b *testing.B) {
	for _, workers := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.ReportAllocs()

			store := storage.NewStore()
			addr, cleanup := startRelayTrafficBenchmark(b, func(c rely.Client, event *nostr.Event) rely.EventResult {
				store.Save(*event)
				return rely.Success()
			}, func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
				_ = ctx
				_ = c
				_ = id
				_ = filters
				return nil, nil
			})
			defer cleanup()

			pub := newPublisherPool(b, addr, workers)
			defer pub.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				pub.Round(b, i)
			}
		})
	}
}

func BenchmarkRelayBroadcastFanout(b *testing.B) {
	for _, subscribers := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			b.ReportAllocs()

			store := storage.NewStore()
			addr, cleanup := startRelayTrafficBenchmark(b, func(c rely.Client, event *nostr.Event) rely.EventResult {
				store.Save(*event)
				return rely.Success()
			}, func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
				_ = ctx
				_ = c
				_ = id
				_ = filters
				return nil, nil
			})
			defer cleanup()

			subs := newSubscriberPool(b, addr, subscribers)
			defer subs.Close()

			publisher := newBenchmarkConn(b, addr)
			defer publisher.Close()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := publisher.SendEvent(b, makeRelayEvent(i, 0)); err != nil {
					b.Fatal(err)
				}
				if err := subs.WaitBroadcastRound(b, i); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func startRelayTrafficBenchmark(
	b *testing.B,
	onEvent func(rely.Client, *nostr.Event) rely.EventResult,
	onReq func(context.Context, rely.Client, string, nostr.Filters) ([]nostr.Event, error),
) (string, func()) {
	b.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen relay benchmark: %v", err)
	}

	relay := rely.NewRelay(
		rely.WithClientResponseLimit(256),
	)
	relay.Reject.Connection.Clear()
	relay.Reject.Event.Clear()
	relay.On.Event = onEvent
	relay.On.Req = onReq

	ctx, cancel := context.WithCancel(context.Background())
	relay.Start(ctx)

	server := &http.Server{Handler: relay}
	go func() {
		_ = server.Serve(ln)
	}()

	cleanup := func() {
		cancel()
		_ = server.Close()
		_ = ln.Close()
	}

	return "ws://" + ln.Addr().String(), cleanup
}

type benchmarkConn struct {
	conn *websocket.Conn
}

func newBenchmarkConn(b *testing.B, addr string) *benchmarkConn {
	b.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		b.Fatalf("dial %s: %v", addr, err)
	}
	return &benchmarkConn{conn: conn}
}

func (c *benchmarkConn) Close() {
	_ = c.conn.Close()
}

func (c *benchmarkConn) SendEvent(b *testing.B, event nostr.Event) error {
	b.Helper()

	payload, err := json.Marshal([]any{"EVENT", event})
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	_, msg, err := c.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read event response: %w", err)
	}
	if !hasLabel(msg, "OK") {
		return fmt.Errorf("unexpected response: %s", string(msg))
	}
	return nil
}

type publisherPool struct {
	conns []*benchmarkConn
}

func newPublisherPool(b *testing.B, addr string, workers int) *publisherPool {
	b.Helper()

	pool := &publisherPool{
		conns: make([]*benchmarkConn, workers),
	}
	for i := 0; i < workers; i++ {
		pool.conns[i] = newBenchmarkConn(b, addr)
	}
	return pool
}

func (p *publisherPool) Close() {
	for _, c := range p.conns {
		c.Close()
	}
}

func (p *publisherPool) Round(b *testing.B, round int) {
	b.Helper()

	var wg sync.WaitGroup
	errCh := make(chan error, len(p.conns))
	for i, c := range p.conns {
		wg.Add(1)
		go func(worker int, conn *benchmarkConn) {
			defer wg.Done()
			if err := conn.SendEvent(b, makeRelayEvent(round, worker)); err != nil {
				errCh <- err
			}
		}(i, c)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			b.Fatal(err)
		}
	}
}

type subscriberPool struct {
	subs []*subscriberConn
}

func newSubscriberPool(b *testing.B, addr string, count int) *subscriberPool {
	b.Helper()

	pool := &subscriberPool{
		subs: make([]*subscriberConn, count),
	}
	for i := 0; i < count; i++ {
		pool.subs[i] = newSubscriberConn(b, addr, i)
	}
	return pool
}

func (p *subscriberPool) Close() {
	for _, s := range p.subs {
		s.Close()
	}
}

func (p *subscriberPool) WaitBroadcastRound(b *testing.B, round int) error {
	b.Helper()

	var wg sync.WaitGroup
	errCh := make(chan error, len(p.subs))
	for _, s := range p.subs {
		wg.Add(1)
		go func(sub *subscriberConn) {
			defer wg.Done()
			if err := sub.ReadBroadcast(round); err != nil {
				errCh <- err
			}
		}(s)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

type subscriberConn struct {
	conn *websocket.Conn
}

func newSubscriberConn(b *testing.B, addr string, idx int) *subscriberConn {
	b.Helper()

	conn := newBenchmarkConn(b, addr)
	reqID := fmt.Sprintf("sub-%d", idx)
	req := []any{"REQ", reqID, nostr.Filter{Kinds: []int{1}}}
	payload, err := json.Marshal(req)
	if err != nil {
		b.Fatalf("marshal subscription request: %v", err)
	}
	if err := conn.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		b.Fatalf("write subscription request: %v", err)
	}

	if _, msg, err := conn.conn.ReadMessage(); err != nil {
		b.Fatalf("read eose: %v", err)
	} else if !hasLabel(msg, "EOSE") {
		b.Fatalf("unexpected subscription response: %s", string(msg))
	}

	return &subscriberConn{conn: conn.conn}
}

func (s *subscriberConn) Close() {
	_ = s.conn.Close()
}

func (s *subscriberConn) ReadBroadcast(round int) error {
	_ = round

	_, msg, err := s.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read broadcast: %w", err)
	}
	if !hasLabel(msg, "EVENT") {
		return fmt.Errorf("unexpected broadcast response: %s", string(msg))
	}
	return nil
}

func makeRelayEvent(round, worker int) nostr.Event {
	return nostr.Event{
		ID:        fmt.Sprintf("bench-%d-%d", round, worker),
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    fmt.Sprintf("bench-pubkey-%d", worker),
		Content:   fmt.Sprintf("relay-bench-%d-%d", round, worker),
	}
}

func hasLabel(msg []byte, label string) bool {
	var payload []json.RawMessage
	if err := json.Unmarshal(msg, &payload); err != nil {
		return false
	}
	if len(payload) == 0 {
		return false
	}
	var got string
	if err := json.Unmarshal(payload[0], &got); err != nil {
		return false
	}
	return got == label
}
