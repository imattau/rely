package websocket

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	TextMessage   = 1
	BinaryMessage = 2
	CloseMessage  = 8
	PingMessage   = 9
	PongMessage   = 10

	CloseNormalClosure    = 1000
	CloseGoingAway        = 1001
	CloseNoStatusReceived = 1005
	CloseAbnormalClosure  = 1006
	CloseTryAgainLater    = 1013
)

var ErrBadHandshake = errors.New("websocket: bad handshake")

type Conn struct {
	conn        net.Conn
	reader      *bufio.Reader
	writer      *bufio.Writer
	isClient    bool
	readLimit   int64
	pongHandler func(string) error
	writeMu     sync.Mutex
}

func NewConn(conn net.Conn, isClient bool) *Conn {
	return &Conn{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   bufio.NewWriter(conn),
		isClient: isClient,
	}
}

func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) SetReadLimit(n int64)                { c.readLimit = n }
func (c *Conn) SetReadDeadline(t time.Time) error   { return c.conn.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error  { return c.conn.SetWriteDeadline(t) }
func (c *Conn) SetPongHandler(h func(string) error) { c.pongHandler = h }

func (c *Conn) NextReader() (int, io.Reader, error) {
	mt, payload, err := c.ReadMessage()
	if err != nil {
		return 0, nil, err
	}
	return mt, bytes.NewReader(payload), nil
}

func (c *Conn) NextWriter(messageType int) (io.WriteCloser, error) {
	return &messageWriter{c: c, messageType: messageType}, nil
}

func (c *Conn) ReadMessage() (int, []byte, error) {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}

		switch opcode {
		case PingMessage:
			if c.pongHandler != nil {
				_ = c.pongHandler(string(payload))
			}
			continue
		default:
			return opcode, payload, nil
		}
	}
}

func (c *Conn) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrame(messageType, data)
}

func (c *Conn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return c.WriteMessage(messageType, data)
}

type messageWriter struct {
	c           *Conn
	messageType int
	buf         bytes.Buffer
}

func (w *messageWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *messageWriter) Close() error {
	return w.c.WriteMessage(w.messageType, w.buf.Bytes())
}

type Upgrader struct {
	CheckOrigin     func(*http.Request) bool
	ReadBufferSize  int
	WriteBufferSize int
}

func (u Upgrader) Upgrade(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*Conn, error) {
	if u.CheckOrigin != nil && !u.CheckOrigin(r) {
		return nil, errors.New("websocket: origin not allowed")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("websocket: hijacking not supported")
	}

	conn, buf, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		return nil, errors.New("websocket: missing key")
	}

	accept := computeAcceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := buf.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := buf.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return &Conn{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   bufio.NewWriter(conn),
		isClient: false,
	}, nil
}

type Dialer struct{}

var DefaultDialer = Dialer{}

func (d Dialer) Dial(rawurl string, requestHeader http.Header) (*Conn, *http.Response, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, nil, err
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, nil, err
	}

	key := randomKey()
	req := &http.Request{
		Method: "GET",
		URL:    u,
		Host:   u.Host,
		Header: make(http.Header),
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	for k, vals := range requestHeader {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	var b bytes.Buffer
	if err := req.Write(&b); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if _, err := conn.Write(b.Bytes()); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, resp, ErrBadHandshake
	}

	return &Conn{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		writer:   bufio.NewWriter(conn),
		isClient: true,
	}, resp, nil
}

func FormatCloseMessage(code int, text string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint16(code))
	b.WriteString(text)
	return b.Bytes()
}

func (c *Conn) readFrame() (int, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return 0, nil, err
	}

	opcode := int(header[0] & 0x0f)
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.reader, ext); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.reader, ext); err != nil {
			return 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext))
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

func (c *Conn) writeFrame(messageType int, payload []byte) error {
	var b bytes.Buffer
	b.WriteByte(0x80 | byte(messageType&0x0f))

	maskBit := byte(0)
	if c.isClient {
		maskBit = 0x80
	}
	length := len(payload)
	switch {
	case length < 126:
		b.WriteByte(maskBit | byte(length))
	case length <= 65535:
		b.WriteByte(maskBit | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(length))
		b.Write(ext[:])
	default:
		b.WriteByte(maskBit | 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		b.Write(ext[:])
	}

	if c.isClient {
		var key [4]byte
		rand.Read(key[:])
		b.Write(key[:])
		for i := range payload {
			payload[i] ^= key[i%4]
		}
		defer func() {
			for i := range payload {
				payload[i] ^= key[i%4]
			}
		}()
	}

	b.Write(payload)
	_, err := c.writer.Write(b.Bytes())
	if err != nil {
		return err
	}
	return c.writer.Flush()
}

func computeAcceptKey(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func IsUnexpectedCloseError(err error, expectedCodes ...int) bool {
	if err == nil {
		return false
	}
	// The shim treats all write/read errors as unexpected unless the caller
	// explicitly requested one of the known close codes and the error string
	// clearly indicates a normal close condition.
	msg := err.Error()
	if strings.Contains(msg, "closed") || strings.Contains(msg, "EOF") {
		for _, code := range expectedCodes {
			if code == CloseNormalClosure {
				return false
			}
		}
	}
	return true
}

func randomKey() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	return base64.StdEncoding.EncodeToString(raw[:])
}
