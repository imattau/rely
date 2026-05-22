package swarm

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/goccy/go-json"
	ws "github.com/gorilla/websocket"
)

// ClientConfig holds the configuration for a websocket client.
type ClientConfig struct {
	WriteWait             time.Duration
	WriteInterval         time.Duration
	PongWait              time.Duration
	PingPeriod            time.Duration
	MaxMessageSize        int64
	DisconnectProbability float32
}

func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		WriteWait:             10 * time.Second,
		WriteInterval:         500 * time.Millisecond,
		PongWait:              60 * time.Second,
		PingPeriod:            (60 * time.Second) / 2,
		MaxMessageSize:        500_000,
		DisconnectProbability: 0.001,
	}
}

func (c ClientConfig) Validate() error {
	if c.PingPeriod < 1*time.Second {
		return errors.New("ping period must be greater than 1s to function reliably")
	}
	if c.PongWait <= c.PingPeriod {
		return errors.New("pong wait must be greater than ping period to function reliably")
	}
	if c.WriteWait < 1*time.Second {
		return errors.New("write wait must be greater than 1s to function reliably")
	}
	if c.MaxMessageSize < 512 {
		return errors.New("max message size must be greater than 512 bytes to accept nostr events")
	}
	if c.DisconnectProbability < 0 || c.DisconnectProbability > 1 {
		return errors.New("disconnect probability must be between 0 and 1")
	}
	return nil
}

// client is a single websocket client that belongs to a swarm.
type client struct {
	conn *ws.Conn
	Behavior

	swarm  *T // pointer to the parent swarm
	config ClientConfig
}

func (c *client) write(ctx context.Context, cancel context.CancelFunc) {
	ping := time.NewTicker(c.config.PingPeriod)
	write := time.NewTicker(c.config.WriteInterval)

	defer func() {
		cancel()
		c.conn.Close()
		ping.Stop()
		write.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case <-write.C:
			if rand.Float32() < c.config.DisconnectProbability {
				// randomly disconnect
				return
			}

			data := c.NextRequest()
			size := int64(len(data))

			c.conn.SetWriteDeadline(time.Now().Add(c.config.WriteWait))
			err := c.conn.WriteMessage(ws.TextMessage, data)
			if err != nil {
				return
			}

			c.swarm.totalRequests.Add(1)
			c.swarm.dataSent.Add(size)

		case <-ping.C:
			c.conn.SetWriteDeadline(time.Now().Add(c.config.WriteWait))
			err := c.conn.WriteMessage(ws.PingMessage, nil)
			if err != nil {
				return
			}
		}
	}
}

func (c *client) read(ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		cancel()
		c.conn.Close()
	}()

	c.conn.SetReadLimit(c.config.MaxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(c.config.PongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(c.config.PongWait))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return

		default:
			messageType, data, err := c.conn.ReadMessage()
			if err != nil {
				return
			}
			if messageType != ws.TextMessage || len(data) == 0 || data[0] != '[' {
				continue
			}

			decoder := json.NewDecoder(bytes.NewReader(data))
			if err := c.ValidateResponse(decoder); err != nil {
				c.swarm.report(err)
				return
			}
		}
	}
}

func IsBadError(err error) bool {
	return ws.IsUnexpectedCloseError(err,
		ws.CloseNormalClosure,
		ws.CloseTryAgainLater,
		ws.CloseAbnormalClosure)
}
