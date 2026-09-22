package violetplatform

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestReadSSEFrames 验证帧解析：注释与 id 行忽略、多行 data 按规范以 LF 连接、
// 只有 data 没有 event 的行也照常派发（事件名可由 data 里的 type 兜底），
// 而以 EOF 收尾的残帧必须丢弃——半截 JSON 交给上层只会变成解不开却已计数的事件。
func TestReadSSEFrames(t *testing.T) {
	t.Parallel()
	stream := strings.Join([]string{
		": connected",
		"",
		"event: message.created",
		"data: {\"a\":1,",
		"data:  \"b\":2}",
		"id: 7",
		"retry: 1000",
		"",
		"event: typing.updated",
		"data: {\"conversation_id\":\"c1\"}",
		"",
		"data: 只有正文没有事件名",
		"",
		"event: message.created",
		"data: {\"truncated\":true",
	}, "\n")
	var frames []sseFrame
	err := readSSEFrames(bufio.NewReader(strings.NewReader(stream)), &sseActivity{}, func(frame sseFrame) error {
		frames = append(frames, frame)
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("读到结尾应报告对端断开，got %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("应派发 3 帧（残帧不计），got %d: %+v", len(frames), frames)
	}
	// 规范规定多条 data 行以单个 LF 连接，而不是直接拼接。
	if frames[0].name != eventMessageCreated || frames[0].data != "{\"a\":1,\n \"b\":2}" {
		t.Fatalf("多行 data 未按规范合并: %+v", frames[0])
	}
	if frames[1].name != eventTypingUpdated {
		t.Fatalf("第二帧事件名 = %q", frames[1].name)
	}
	if frames[2].name != "" || frames[2].data != "只有正文没有事件名" {
		t.Fatalf("无事件名帧应原样派发，got %+v", frames[2])
	}
}

// TestReadSSEFrames_HandlerError 验证处理器错误会中止本次读取（断流后重连）。
func TestReadSSEFrames_HandlerError(t *testing.T) {
	t.Parallel()
	wanted := errors.New("boom")
	err := readSSEFrames(bufio.NewReader(strings.NewReader("event: message.created\ndata: x\n\n")), &sseActivity{}, func(sseFrame) error {
		return wanted
	})
	if !errors.Is(err, wanted) {
		t.Fatalf("处理器错误应向上返回，got %v", err)
	}
}

// TestSseActivity 验证看门狗判据：从未读过不算静默，超时后才判定静默。
func TestSseActivity(t *testing.T) {
	t.Parallel()
	activity := &sseActivity{}
	if activity.idle() {
		t.Fatal("尚未读过任何字节时不应判定为静默")
	}
	activity.touch()
	if activity.idle() {
		t.Fatal("刚有活动不应判定为静默")
	}
	activity.lastNano.Store(time.Now().Add(-2 * sseIdleTimeout).UnixNano())
	if !activity.idle() {
		t.Fatal("超过静默阈值后应判定为静默")
	}
}
