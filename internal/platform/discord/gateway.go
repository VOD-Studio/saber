package discordplatform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// gatewayOpcodeIdentify 是客户端上线时发送的 op 2。
const gatewayOpcodeIdentify = 2

// errGatewayReconnect 表示服务端要求重建连接（op 7 RECONNECT 或 op 9 INVALID_SESSION）。
var errGatewayReconnect = errors.New("discord gateway 要求重连")

// gatewayConn 串行化 Gateway 的写入：Identify 与心跳来自不同 goroutine，
// 同一时刻只能有一帧在写，否则会拼坏 WebSocket 帧。
type gatewayConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	seq     atomic.Int64
}

// send 发送一个 JSON 帧。
func (g *gatewayConn) send(payload any) error {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	return websocket.JSON.Send(g.conn, payload)
}

// sequence 返回最近收到的事件序号，心跳要原样带上；还没有事件时为 null。
func (g *gatewayConn) sequence() *int64 {
	value := g.seq.Load()
	if value == 0 {
		return nil
	}
	return &value
}

// runGateway 建立一次 Gateway 会话并读到断开为止。
//
// 返回 nil 或错误都表示本次会话结束，由 supervise 决定退避重连。
func (p *Platform) runGateway(ctx context.Context) error {
	endpoint, err := p.api.gatewayURL(ctx)
	if err != nil {
		return fmt.Errorf("获取 discord gateway 地址失败: %w", err)
	}
	wsURL, err := gatewayConnectURL(endpoint)
	if err != nil {
		return err
	}
	origin := "https://discord.com"
	config, err := websocket.NewConfig(wsURL, origin)
	if err != nil {
		return fmt.Errorf("构造 discord gateway 连接配置失败: %w", err)
	}
	// x/net 的默认拨号没有超时；DNS 或 TCP 挂起会卡住整个重连循环。
	config.Dialer = &net.Dialer{Timeout: 15 * time.Second}
	conn, err := websocket.DialConfig(config)
	if err != nil {
		return fmt.Errorf("连接 discord gateway 失败: %w", err)
	}
	gateway := &gatewayConn{conn: conn}
	// 每次连接单独派生 ctx：连接断开重连时这个监听 goroutine 能及时退出，
	// 而不是一直挂到平台整体停止。
	connCtx, closeConn := context.WithCancel(ctx)
	defer closeConn()
	// ctx 结束时主动关闭连接，让阻塞中的 Receive 立刻返回。
	go func() {
		<-connCtx.Done()
		if closeErr := conn.Close(); closeErr != nil {
			slog.Debug("关闭 discord gateway 连接失败", "error", closeErr)
		}
	}()
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			slog.Debug("关闭 discord gateway 连接失败", "error", closeErr)
		}
	}()

	var hello gatewayPayload
	if err := websocket.JSON.Receive(conn, &hello); err != nil {
		return fmt.Errorf("读取 discord gateway hello 失败: %w", err)
	}
	if hello.Op != 10 {
		return fmt.Errorf("discord gateway 首帧不是 hello（op %d）", hello.Op)
	}
	var greeting gatewayHello
	if err := json.Unmarshal(hello.D, &greeting); err != nil {
		return fmt.Errorf("解析 discord gateway hello 失败: %w", err)
	}
	interval := time.Duration(greeting.HeartbeatInterval) * time.Millisecond
	if interval <= 0 {
		interval = 41 * time.Second
	}

	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go p.heartbeat(heartbeatCtx, gateway, interval)

	if err := gateway.send(map[string]any{
		"op": gatewayOpcodeIdentify,
		"d": map[string]any{
			"token":   p.api.token,
			"intents": gatewayIntents,
			"properties": map[string]string{
				"os":      "linux",
				"browser": "saber",
				"device":  "saber",
			},
		},
	}); err != nil {
		return fmt.Errorf("发送 discord identify 失败: %w", err)
	}

	for {
		var frame gatewayPayload
		if err := websocket.JSON.Receive(conn, &frame); err != nil {
			return fmt.Errorf("读取 discord gateway 事件失败: %w", err)
		}
		if frame.S != nil {
			gateway.seq.Store(*frame.S)
		}
		switch frame.Op {
		case 0:
			p.dispatchGatewayEvent(ctx, frame.T, frame.D)
		case 1:
			// 服务端要求立刻心跳，不等下一个 tick。
			if err := gateway.send(map[string]any{"op": 1, "d": gateway.sequence()}); err != nil {
				return fmt.Errorf("发送 discord 心跳失败: %w", err)
			}
		case 7, 9:
			return errGatewayReconnect
		case 10, 11:
			// 重复 hello 与心跳确认不影响事件处理。
		default:
			slog.Debug("忽略未知 discord gateway opcode", "op", frame.Op)
		}
	}
}

// heartbeat 按 Hello 给的间隔发送 op 1，直到 ctx 结束或写失败。
func (p *Platform) heartbeat(ctx context.Context, gateway *gatewayConn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := gateway.send(map[string]any{"op": 1, "d": gateway.sequence()}); err != nil {
				slog.Debug("discord 心跳发送失败，等待重连", "error", err)
				return
			}
		}
	}
}

// dispatchGatewayEvent 处理 op 0 的分发事件。
func (p *Platform) dispatchGatewayEvent(ctx context.Context, event string, data json.RawMessage) {
	switch event {
	case "READY":
		var ready gatewayReady
		if err := json.Unmarshal(data, &ready); err != nil {
			slog.Warn("解析 discord READY 失败", "error", err)
			return
		}
		if ready.User.ID == "" {
			slog.Warn("discord READY 缺少 bot 用户 ID")
			return
		}
		p.setBotUser(ready.User.ID, ready.User.Username)
		slog.Info("discord gateway 已就绪", "bot", ready.User.Username, "session", ready.SessionID)
	case "MESSAGE_CREATE":
		var message gatewayMessage
		if err := json.Unmarshal(data, &message); err != nil {
			slog.Warn("解析 discord MESSAGE_CREATE 失败", "error", err)
			return
		}
		select {
		case p.queue <- message:
		case <-ctx.Done():
		}
	default:
		slog.Debug("忽略 discord 事件", "event", event)
	}
}

// gatewayConnectURL 补上 Gateway 版本与 JSON 编码参数。
func gatewayConnectURL(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("discord gateway 地址不合法: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return "", fmt.Errorf("discord gateway 地址协议 %q 不受支持", parsed.Scheme)
	}
	query := parsed.Query()
	query.Set("v", "10")
	query.Set("encoding", "json")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
