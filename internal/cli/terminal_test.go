package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestRunTerminal_InputAndErrors(t *testing.T) {
	for _, ending := range []string{"/exit\nignored\n", "/quit\n", ""} {
		t.Run(ending, func(t *testing.T) {
			var out bytes.Buffer
			var messages []chat.Message
			handle := func(ctx context.Context, msg chat.Message, reply chat.Adapter) (agent.Result, error) {
				messages = append(messages, msg)
				if msg.Text == "fail" {
					return agent.Result{}, errors.New("upstream failure")
				}
				_, err := reply.Send(ctx, chat.Reply{Session: msg.Session, Text: "ok: " + msg.Text})
				return agent.Result{Status: agent.Completed}, err
			}
			require.NoError(t, RunTerminal(context.Background(), io.NopCloser(strings.NewReader("\nfail\n hello \n"+ending)), &out, handle, "test-model"))
			require.Len(t, messages, 2)
			require.Equal(t, "terminal", messages[0].Session.Platform)
			require.Equal(t, messages[0].Session, messages[1].Session)
			require.NotEqual(t, messages[0].ID, messages[1].ID)
			require.Contains(t, out.String(), "错误：upstream failure")
			require.Contains(t, out.String(), "Saber> ok: hello")
		})
	}
}

func TestRunTerminal_CancelIdleInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { require.NoError(t, writer.Close()) }()
	done := make(chan error, 1)
	started := make(chan struct{})
	input := &waitingInput{ReadCloser: reader, started: started}
	go func() {
		done <- RunTerminal(ctx, input, io.Discard, func(context.Context, chat.Message, chat.Adapter) (agent.Result, error) {
			return agent.Result{}, errors.New("unexpected input")
		}, "test")
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not unblock terminal input")
	}
	require.NoError(t, reader.Close())
}

func TestRunTerminal_CancelDuringRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	require.NoError(t, RunTerminal(ctx, io.NopCloser(strings.NewReader("hello\nnever send\n")), io.Discard, func(ctx context.Context, msg chat.Message, reply chat.Adapter) (agent.Result, error) {
		calls++
		cancel()
		return agent.Result{}, ctx.Err()
	}, "test"))
	require.Equal(t, 1, calls)
}

// waitingInput 让取消测试精确等待 Scanner 已进入阻塞读取。
type waitingInput struct {
	io.ReadCloser
	started chan struct{}
}

func (r *waitingInput) Read(p []byte) (int, error) {
	close(r.started)
	return r.ReadCloser.Read(p)
}

// TestRunTerminal_CancelConsoleRead 覆盖 Close 无法解除真实终端 Read 阻塞的情况。
func TestRunTerminal_CancelConsoleRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := &consoleInput{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	defer func() { close(input.release); <-input.finished }()
	done := make(chan error, 1)
	go func() { done <- RunTerminal(ctx, input, io.Discard, nil, "test") }()
	<-input.started
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("console Read must not block cancellation")
	}
}

type consoleInput struct{ started, release, finished chan struct{} }

func (r *consoleInput) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	close(r.finished)
	return 0, io.EOF
}
func (r *consoleInput) Close() error { return nil }
