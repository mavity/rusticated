// Package browser implements a pure-Go Chrome DevTools Protocol (CDP) client,
// in-browser nickname-to-tab persistence, heuristically trimmed AX tree & diffing,
// biomimetic mouse/keyboard physics, dual-signal settling, and token-frugal
// network activity summarization.
package browser

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CDPError represents a JSON-RPC error returned by Chrome DevTools Protocol.
type CDPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *CDPError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("cdp error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message)
}

// Event represents an incoming CDP notification event.
type Event struct {
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	SessionID string          `json:"sessionId,omitempty"`
}

type rpcRequest struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type rpcMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *CDPError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type rpcReply struct {
	result json.RawMessage
	err    error
}

// Conn is a multiplexed JSON-RPC 2.0 client over an RFC 6455 WebSocket connection.
type Conn struct {
	conn    net.Conn
	br      *bufio.Reader
	writeMu sync.Mutex

	nextID    atomic.Int64
	pendingMu sync.Mutex
	pending   map[int64]chan rpcReply

	subMu      sync.RWMutex
	nextSubID  int
	subs       map[int]func(Event)
	closedCh   chan struct{}
	closeOnce  sync.Once
	closeError error
}

// DialWebSocket connects to a ws:// localhost URL and performs the RFC 6455 handshake.
func DialWebSocket(ctx context.Context, wsURL string) (*Conn, error) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("invalid websocket url %q: %w", wsURL, err)
	}
	if u.Scheme != "ws" && u.Scheme != "http" {
		return nil, fmt.Errorf("unsupported websocket scheme %q", u.Scheme)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}

	var d net.Dialer
	netConn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = netConn.Close()
		return nil, err
	}
	secKey := base64.StdEncoding.EncodeToString(keyBytes)

	reqPath := u.RequestURI()
	if reqPath == "" {
		reqPath = "/"
	}
	var req strings.Builder
	req.WriteString("GET " + reqPath + " HTTP/1.1\r\n")
	req.WriteString("Host: " + u.Host + "\r\n")
	req.WriteString("Upgrade: websocket\r\n")
	req.WriteString("Connection: Upgrade\r\n")
	req.WriteString("Sec-WebSocket-Key: " + secKey + "\r\n")
	req.WriteString("Sec-WebSocket-Version: 13\r\n\r\n")

	if deadline, ok := ctx.Deadline(); ok {
		_ = netConn.SetDeadline(deadline)
	} else {
		_ = netConn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	if _, err := io.WriteString(netConn, req.String()); err != nil {
		_ = netConn.Close()
		return nil, err
	}

	br := bufio.NewReaderSize(netConn, 65536)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("websocket handshake read: %w", err)
	}
	if !strings.Contains(statusLine, "101") {
		_ = netConn.Close()
		return nil, fmt.Errorf("unexpected websocket handshake status: %s", strings.TrimSpace(statusLine))
	}

	acceptExpected := computeWSKeyAccept(secKey)
	gotAccept := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = netConn.Close()
			return nil, fmt.Errorf("websocket header read: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(k), "Sec-WebSocket-Accept") {
				gotAccept = strings.TrimSpace(v)
			}
		}
	}
	if gotAccept != "" && gotAccept != acceptExpected {
		_ = netConn.Close()
		return nil, fmt.Errorf("invalid Sec-WebSocket-Accept %q", gotAccept)
	}

	_ = netConn.SetDeadline(time.Time{})

	c := &Conn{
		conn:     netConn,
		br:       br,
		pending:  make(map[int64]chan rpcReply),
		subs:     make(map[int]func(Event)),
		closedCh: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func computeWSKeyAccept(secKey string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(secKey + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}

// Subscribe registers a callback for CDP events and returns an unsubscribe function.
func (c *Conn) Subscribe(fn func(Event)) func() {
	c.subMu.Lock()
	c.nextSubID++
	id := c.nextSubID
	c.subs[id] = fn
	c.subMu.Unlock()
	return func() {
		c.subMu.Lock()
		delete(c.subs, id)
		c.subMu.Unlock()
	}
}

// Call invokes a CDP method (optionally scoped to a target sessionId) and unmarshals the result into out.
func (c *Conn) Call(ctx context.Context, sessionID string, method string, params any, out any) error {
	select {
	case <-c.closedCh:
		if c.closeError != nil {
			return c.closeError
		}
		return errors.New("cdp connection closed")
	default:
	}

	id := c.nextID.Add(1)
	replyCh := make(chan rpcReply, 1)

	c.pendingMu.Lock()
	c.pending[id] = replyCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	payload, err := json.Marshal(rpcRequest{
		ID:        id,
		Method:    method,
		Params:    params,
		SessionID: sessionID,
	})
	if err != nil {
		return err
	}

	if err := c.writeFrame(0x1, payload); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closedCh:
		if c.closeError != nil {
			return c.closeError
		}
		return errors.New("cdp connection closed")
	case rep := <-replyCh:
		if rep.err != nil {
			return rep.err
		}
		if out != nil && len(rep.result) > 0 {
			return json.Unmarshal(rep.result, out)
		}
		return nil
	}
}

// Close closes the underlying WebSocket connection.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		_ = c.writeFrame(0x8, nil)
		err = c.conn.Close()
		close(c.closedCh)
	})
	return err
}

// Closed returns a channel that is closed when the connection terminates.
func (c *Conn) Closed() <-chan struct{} {
	return c.closedCh
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var frame bytes.Buffer
	frame.WriteByte(0x80 | (opcode & 0x0f))

	n := len(payload)
	switch {
	case n <= 125:
		frame.WriteByte(0x80 | byte(n))
	case n <= 65535:
		frame.WriteByte(0x80 | 126)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(n))
		frame.Write(lenBuf[:])
	default:
		frame.WriteByte(0x80 | 127)
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(n))
		frame.Write(lenBuf[:])
	}

	var maskKey [4]byte
	_, _ = rand.Read(maskKey[:])
	frame.Write(maskKey[:])

	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = payload[i] ^ maskKey[i%4]
	}
	frame.Write(masked)

	_, err := c.conn.Write(frame.Bytes())
	return err
}

func (c *Conn) readLoop() {
	var msgBuf bytes.Buffer
	var msgOpcode byte

	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			c.failAll(err)
			return
		}

		switch opcode {
		case 0x8: // Close
			c.failAll(io.EOF)
			return
		case 0x9: // Ping
			_ = c.writeFrame(0xA, payload)
			continue
		case 0xA: // Pong
			continue
		case 0x1, 0x2: // Text or Binary
			msgOpcode = opcode
			msgBuf.Reset()
			msgBuf.Write(payload)
		case 0x0: // Continuation
			msgBuf.Write(payload)
		}

		if fin && (msgOpcode == 0x1 || msgOpcode == 0x2) {
			raw := append([]byte(nil), msgBuf.Bytes()...)
			msgBuf.Reset()
			c.dispatchMessage(raw)
		}
	}
}

func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.br, hdr[:]); err != nil {
		return false, 0, nil, err
	}
	fin = (hdr[0] & 0x80) != 0
	opcode = hdr[0] & 0x0f
	masked := (hdr[1] & 0x80) != 0
	length := uint64(hdr[1] & 0x7f)

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return false, 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (c *Conn) dispatchMessage(raw []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	if msg.ID != 0 {
		c.pendingMu.Lock()
		ch, ok := c.pending[msg.ID]
		c.pendingMu.Unlock()
		if ok {
			var rep rpcReply
			if msg.Error != nil {
				rep.err = msg.Error
			} else {
				rep.result = msg.Result
			}
			select {
			case ch <- rep:
			default:
			}
		}
		return
	}

	if msg.Method != "" {
		evt := Event{
			Method:    msg.Method,
			Params:    msg.Params,
			SessionID: msg.SessionID,
		}
		c.subMu.RLock()
		handlers := make([]func(Event), 0, len(c.subs))
		for _, fn := range c.subs {
			handlers = append(handlers, fn)
		}
		c.subMu.RUnlock()
		for _, fn := range handlers {
			fn(evt)
		}
	}
}

func (c *Conn) failAll(err error) {
	c.closeOnce.Do(func() {
		c.closeError = err
		_ = c.conn.Close()
		close(c.closedCh)
	})
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		select {
		case ch <- rpcReply{err: err}:
		default:
		}
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
}
