package swarm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	ws "github.com/gorilla/websocket"
)

// Config holds the configuration for a swarm of clients.
type Config struct {
	// the frequency at which new clients are spawned
	ConnectionFrequency time.Duration

	// the configuration for the clients
	Client ClientConfig
}

func DefaultConfig() Config {
	return Config{
		ConnectionFrequency: 10 * time.Millisecond,
		Client:              DefaultClientConfig(),
	}
}

func (c Config) Validate() error {
	if c.ConnectionFrequency <= 0 {
		return errors.New("connection frequency must be positive")
	}
	if err := c.Client.Validate(); err != nil {
		return err
	}
	return nil
}

// T is a swarm, a collection of clients that share the same configuration and error channel.
// As soon as a client finds an error, it reports it to the error channel.
type T struct {
	behaviors BehaviorDistribution
	errs      chan error

	connectionAttempts    atomic.Int64
	connectionEstablished atomic.Int64
	totalRequests         atomic.Int64
	dataSent              atomic.Int64
	config                Config
}

// New creates a new swarm with the given configuration and behavior distribution.
func New(c Config, b BehaviorDistribution) (*T, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &T{
		config:    c,
		behaviors: b,
		errs:      make(chan error, 10),
	}, nil
}

// Err returns the error channel of the swarm.
// Errors happen when the relay is not behaving as expected.
func (s *T) Err() <-chan error {
	return s.errs
}

// Report sends an error to the error channel of the swarm in a non-blocking way.
func (s *T) report(err error) {
	select {
	case s.errs <- err:
	default:
	}
}

// ConnectionAttempts returns the number of connection attempts made by the swarm.
func (s *T) ConnectionAttempts() int {
	return int(s.connectionAttempts.Load())
}

// ConnectionEstablished returns the number of connections established by the swarm.
func (s *T) ConnectionEstablished() int {
	return int(s.connectionEstablished.Load())
}

// TotalRequests returns the number of requests made by the swarm to the relay.
func (s *T) TotalRequests() int {
	return int(s.totalRequests.Load())
}

// DataSent returns the number of bytes sent by the swarm.
func (s *T) DataSent() int {
	return int(s.dataSent.Load())
}

// Run starts the swarm, connecting to the relay and sending requests at the configured frequencies.
// It's a blocking operation that returns when the context is done.
func (s *T) Run(ctx context.Context, addr string) {
	if addr == "" {
		addr = "ws://localhost:3334"
	}
	if !strings.HasPrefix(addr, "ws://") {
		addr = "ws://" + addr
	}

	ticker := time.NewTicker(s.config.ConnectionFrequency)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			s.connectionAttempts.Add(1)

			conn, resp, err := ws.DefaultDialer.Dial(addr, nil)
			if err != nil {
				if resp != nil {
					body, _ := io.ReadAll(resp.Body)
					resp.Body.Close()

					msg := strings.TrimSpace(string(body))
					if msg == "the relay is overloaded, please try again later" {
						// the server is rejecting http requests because it's overloaded
						// which is an acceptable behavior, not an error.
						continue
					}
				}

				s.report(fmt.Errorf("failed to connect with websocket: %w", err))
				continue
			}

			s.connectionEstablished.Add(1)

			client := client{
				conn:     conn,
				Behavior: s.behaviors.Sample(),
				swarm:    s,
				config:   s.config.Client,
			}

			clientCtx, cancel := context.WithCancel(ctx)
			go client.write(clientCtx, cancel)
			go client.read(clientCtx, cancel)
		}
	}
}
