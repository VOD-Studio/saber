package violetplatform

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// sseIdleTimeout 是「流还活着」的判据：Violet 每 30 秒发一次 heartbeat 注释帧，
// 连续错过三次说明中间链路把连接悄悄掐了，此时要主动重连而不是继续傻等。
const sseIdleTimeout = 90 * time.Second

// sseActivity 记录流上最后一次读到字节的时刻，供看门狗判断是否卡死。
type sseActivity struct {
	lastNano atomic.Int64
}

// touch 在每次成功读到数据后调用。
func (a *sseActivity) touch() {
	a.lastNano.Store(time.Now().UnixNano())
}

// watchIdle 在流长时间没有字节时调用 cancel，使阻塞在 ReadString 上的读循环带着
// ctx 错误返回并进入重连。watchdog 自己必须在退出时交出 cancel 之外的资源，
// 因此由调用方负责取消连接上下文，这里只负责「什么时候该取消」。
func watchIdle(ctx context.Context, activity *sseActivity, cancel context.CancelFunc) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if activity.idle() {
				slog.Warn("violet 事件流长时间无数据，主动重连", "idle", sseIdleTimeout.String())
				cancel()
				return
			}
		}
	}
}

// idle 报告距最后一次读到字节是否已超过静默阈值；从未读过时不算静默，
// 以免连接刚建立、首帧还没到就被掐断。
func (a *sseActivity) idle() bool {
	last := a.lastNano.Load()
	return last != 0 && time.Since(time.Unix(0, last)) > sseIdleTimeout
}

// sseFrame 是一帧事件：name 取自 event: 行，data 是攒齐的 data: 行。
type sseFrame struct {
	name string
	data string
}

// readSSEFrames 按 SSE 规范逐帧回调，直到流结束或读取出错。
//
// 用 bufio.Reader 而不是 Scanner：message.created 的 data 行自带消息全文，
// 长度可以远超 Scanner 默认 token 上限，截断只会让整帧 JSON 解析失败。
// 未以空行收尾的残帧一律丢弃：事件流可重连，缺的那条消息由补拉通道补上，
// 半截 JSON 交给上层只会变成一条解不开又已经计数的事件。
func readSSEFrames(reader *bufio.Reader, activity *sseActivity, handle func(sseFrame) error) error {
	var (
		name  string
		lines []string
	)
	dispatch := func() error {
		frame := sseFrame{name: name, data: strings.Join(lines, "\n")}
		name, lines = "", nil
		if frame.data == "" {
			return nil // 只有 event: 行没有 data: 行，不是完整事件
		}
		return handle(frame)
	}
	for {
		line, err := reader.ReadString('\n')
		activity.touch()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch trimmed := strings.TrimRight(line, "\r\n"); {
		case trimmed == "":
			if err := dispatch(); err != nil {
				return err
			}
		case !strings.HasPrefix(trimmed, ":"):
			field, value, found := strings.Cut(trimmed, ":")
			if !found {
				continue // 无冒号的行不符合规范，忽略即可，不值得为它断流
			}
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				name = value
			case "data":
				lines = append(lines, value)
			case "id", "retry":
				// bot 流不写 id: 行也没有 retry 语义：显式忽略，避免误以为支持续传。
			}
		}
	}
}
