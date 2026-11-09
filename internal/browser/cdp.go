// Package browser 实现原生 CDP 浏览器工具，不执行模型提供的宿主脚本。
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type cdpConn struct {
	ws      *websocket.Conn
	next    atomic.Int64
	mu      sync.Mutex
	writeMu sync.Mutex
	pending map[int64]chan cdpMessage
	done    chan struct{}
	onEvent func(cdpMessage)
	once    sync.Once
	err     error
}

func dialCDP(ctx context.Context, endpoint string, onEvent func(cdpMessage)) (*cdpConn, error) {
	if strings.HasPrefix(endpoint, "http:") || strings.HasPrefix(endpoint, "https:") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/json/version"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("CDP endpoint unavailable: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("CDP discovery returned HTTP %d", resp.StatusCode)
		}
		var version struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&version); err != nil {
			return nil, err
		}
		endpoint = version.WebSocketDebuggerURL
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, errors.New("CDP discovery did not return a WebSocket endpoint")
	}
	origin := "http://" + u.Host
	if u.Scheme == "wss" {
		origin = "https://" + u.Host
	}
	cfg, err := websocket.NewConfig(endpoint, origin)
	if err != nil {
		return nil, err
	}
	ws, err := cfg.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("CDP websocket unavailable: %w", err)
	}
	ws.MaxPayloadBytes = 32 << 20
	c := &cdpConn{ws: ws, pending: make(map[int64]chan cdpMessage), done: make(chan struct{}), onEvent: onEvent}
	go c.receive()
	return c, nil
}

func (c *cdpConn) receive() {
	for {
		var msg cdpMessage
		if err := websocket.JSON.Receive(c.ws, &msg); err != nil {
			c.fail(err)
			return
		}
		if msg.ID != 0 {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		} else if c.onEvent != nil {
			c.onEvent(msg)
		}
	}
}

func (c *cdpConn) fail(err error) {
	c.once.Do(func() { c.mu.Lock(); c.err = err; close(c.done); c.mu.Unlock(); _ = c.ws.Close() })
}

func (c *cdpConn) call(ctx context.Context, session, method string, args any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.next.Add(1)
	ch := make(chan cdpMessage, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("CDP transport closed: %w", err)
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	params, err := json.Marshal(args)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	err = websocket.JSON.Send(c.ws, cdpMessage{ID: id, Method: method, SessionID: session, Params: params})
	c.writeMu.Unlock()
	if err != nil {
		c.fail(err)
		return fmt.Errorf("CDP dispatch outcome unknown; capture fresh state before another action: %w", err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("CDP dispatched action outcome unknown; not replayed: %w", ctx.Err())
	case <-c.done:
		return fmt.Errorf("CDP dispatched action outcome unknown; not replayed: %w", c.err)
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("CDP %s (%d): %s", method, msg.Error.Code, msg.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(msg.Result, out)
		}
		return nil
	}
}

func (c *cdpConn) close() error { c.fail(errors.New("closed by owner")); return nil }
