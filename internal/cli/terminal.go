package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"rua.plus/saber/internal/chat"
)

// RunTerminal 逐行接收本机用户输入，通过共享聊天入口保留会话历史。
// 普通终端输出完整回答；EOF、/exit 和取消均正常返回，取消时关闭输入解除阻塞。
func RunTerminal(ctx context.Context, input io.ReadCloser, output io.Writer, handle chat.Handler, model string) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	stop := context.AfterFunc(ctx, func() {
		if err := input.Close(); err != nil {
			slog.Debug("关闭终端输入失败", "error", err)
		}
	})
	defer stop()
	if _, err := fmt.Fprintf(output, "Saber 终端对话 · %s\n每行发送一条消息；/exit 或 Ctrl+D 退出，Ctrl+C 取消并退出。\n", model); err != nil {
		return err
	}
	adapter := terminalReply{output: output}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	session := chat.Session{Platform: "terminal", Account: "saber", Conversation: "local"}
	for index := 1; ; {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := fmt.Fprint(output, "你> "); err != nil {
			return err
		}
		// 部分终端设备的 Close 不能解除 Read 阻塞；退出不能依赖读取先完成。
		scanned := make(chan bool, 1)
		go func() { scanned <- scanner.Scan() }()
		select {
		case <-ctx.Done():
			return nil
		case ok := <-scanned:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return scanner.Err()
			}
		}
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		if text == "/exit" || text == "/quit" {
			return nil
		}
		message := chat.Message{Session: session, ID: strconv.Itoa(index), SenderID: strconv.Itoa(os.Getuid()), Text: text}
		index++
		if _, err := handle(ctx, message, &adapter); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if _, writeErr := fmt.Fprintf(output, "错误：%v\n", err); writeErr != nil {
				return writeErr
			}
		}
	}
}

// terminalReply 使用普通文本，不依赖终端控制序列，也支持管道重定向。
type terminalReply struct{ output io.Writer }

// Capabilities 表示只输出最终文本。
func (r *terminalReply) Capabilities() chat.Capabilities { return chat.Capabilities{} }

// Send 写出一轮完整回答。
func (r *terminalReply) Send(ctx context.Context, reply chat.Reply) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, err := fmt.Fprintf(r.output, "Saber> %s\n", reply.Text)
	return "", err
}

// Edit 明确拒绝不支持的消息编辑。
func (r *terminalReply) Edit(context.Context, string, chat.Reply) error {
	return errors.New("terminal does not support message editing")
}

// SetTyping 明确拒绝不支持的输入状态。
func (r *terminalReply) SetTyping(context.Context, chat.Session, bool) error {
	return errors.New("terminal does not support typing status")
}
