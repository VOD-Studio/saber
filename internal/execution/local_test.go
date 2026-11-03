package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLocalExecution_PersistsTMPDIRAndClosesEnvironment(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	result, err := e.Run(ctx, "exec", map[string]any{"command": "printf hello"})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, "hello", result.Summary)
	require.NotEmpty(t, result.OutputID)
	output, err := e.Run(ctx, "read_output", map[string]any{"output_id": result.OutputID})
	require.NoError(t, err)
	require.Equal(t, "hello", dataOf(t, output)["text"])
	require.True(t, dataOf(t, output)["eof"].(bool))

	_, err = e.Run(ctx, "exec", map[string]any{"command": "echo p0 > $TMPDIR/marker"})
	require.NoError(t, err)
	result, err = e.Run(ctx, "exec", map[string]any{"command": "cat $TMPDIR/marker"})
	require.NoError(t, err)
	require.Equal(t, "p0\n", result.Summary)

	// 前台命令结束后同一任务仍可继续使用文件工具。
	_, err = e.Run(ctx, "write_file", map[string]any{"path": "after.txt", "content": "ok"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "after.txt"), []byte("ok"), 0600))

	require.NoError(t, e.CloseTask(ctx, 7))
	raw, err := os.ReadFile(e.recordPath(7))
	require.NoError(t, err)
	var record environmentRecord
	require.NoError(t, json.Unmarshal(raw, &record))
	require.Equal(t, envClosed, record.State)
}

func TestLocalExecution_LongOutputReplay(t *testing.T) {
	e, ctx, _ := fileFixture(t)
	result, err := e.Run(ctx, "exec", map[string]any{"command": "head -c 70000 /dev/zero | tr '\\0' x; printf '\\nP0_OUTPUT_END_7439\\n'"})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.NotEmpty(t, result.OutputID)
	var collected strings.Builder
	offset := 0
	for pages := 0; ; pages++ {
		require.Less(t, pages, 100)
		page, err := e.Run(ctx, "read_output", map[string]any{"output_id": result.OutputID, "offset": float64(offset), "limit": float64(4096)})
		require.NoError(t, err)
		data := page.Data.(map[string]any)
		next := data["next_offset"].(int)
		require.Greater(t, next, offset, "byte cursor must advance")
		collected.WriteString(data["text"].(string))
		offset = next
		if data["eof"].(bool) {
			break
		}
	}
	require.Contains(t, collected.String(), "P0_OUTPUT_END_7439")
	require.NoError(t, e.CloseTask(ctx, 7))
}

func TestLocalExecution_CancelKillsProcessGroup(t *testing.T) {
	e, ctx, dir := fileFixture(t)
	cancelCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	_, err := e.Run(cancelCtx, "exec", map[string]any{"command": "sleep 30 & echo $! > child.pid; wait"})
	require.Error(t, err)
	raw, readErr := os.ReadFile(filepath.Join(dir, "child.pid"))
	require.NoError(t, readErr)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, parseErr)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 5*time.Second, 20*time.Millisecond, "child process must be terminated")
	require.NoError(t, e.CloseTask(ctx, 7))
}

func TestLocalExecution_OutputLimitStopsProcess(t *testing.T) {
	e, ctx, _ := fileFixture(t)
	_, err := e.Run(ctx, "exec", map[string]any{"command": "dd if=/dev/zero bs=1048576 count=17 2>/dev/null"})
	require.ErrorContains(t, err, "16 MiB")
	require.NoError(t, e.CloseTask(ctx, 7))
}

func TestProcess_BackgroundLifecycle(t *testing.T) {
	e, ctx, _ := fileFixture(t)
	start, err := e.Run(ctx, "process", map[string]any{"action": "start", "command": "echo ready; sleep 5"})
	require.NoError(t, err)
	data := dataOf(t, start)
	id := data["id"].(string)
	require.Equal(t, "running", data["status"])
	require.NotEmpty(t, data["output_id"])

	status, err := e.Run(ctx, "process", map[string]any{"action": "status", "id": id})
	require.NoError(t, err)
	require.Equal(t, "running", dataOf(t, status)["status"])

	var logs map[string]any
	require.Eventually(t, func() bool {
		result, err := e.Run(ctx, "process", map[string]any{"action": "logs", "id": id})
		if err != nil {
			return false
		}
		logs = dataOf(t, result)
		return strings.Contains(logs["text"].(string), "ready")
	}, 5*time.Second, 20*time.Millisecond)

	// 后台作业运行期间任务仍可执行文件工具。
	_, err = e.Run(ctx, "write_file", map[string]any{"path": "during.txt", "content": "x"})
	require.NoError(t, err)

	wait, err := e.Run(ctx, "process", map[string]any{"action": "wait", "id": id, "wait_seconds": float64(1)})
	require.NoError(t, err)
	require.Equal(t, "running", dataOf(t, wait)["status"])

	kill, err := e.Run(ctx, "process", map[string]any{"action": "kill", "id": id})
	require.NoError(t, err)
	require.Equal(t, "exited", dataOf(t, kill)["status"])
	require.NoError(t, e.CloseTask(ctx, 7))
}

func TestProcess_RejectsCrossTaskIDAndInvalidActions(t *testing.T) {
	e, ctx, _ := fileFixture(t)
	_, err := e.Run(ctx, "process", map[string]any{"action": "status", "id": "p_0123456789abcdef0123456789abcdef"})
	require.Error(t, err)
	_, err = e.Run(ctx, "process", map[string]any{"action": "start"})
	require.Error(t, err)
	_, err = e.Run(ctx, "process", map[string]any{"action": "list", "id": "x"})
	require.Error(t, err)
	_, err = e.Run(ctx, "process", map[string]any{"action": "bogus"})
	require.Error(t, err)
}

func TestEnvironment_ReusesTaskStateAcrossCommands(t *testing.T) {
	e, ctx, _ := fileFixture(t)
	for range 2 {
		_, err := e.Run(ctx, "exec", map[string]any{"command": "echo value > $TMPDIR/shared"})
		require.NoError(t, err)
	}
	result, err := e.Run(ctx, "exec", map[string]any{"command": "cat $TMPDIR/shared"})
	require.NoError(t, err)
	require.Equal(t, "value\n", result.Summary)
	require.NoError(t, e.CloseTask(ctx, 7))
	_, err = e.Run(ctx, "exec", map[string]any{"command": "true"})
	require.ErrorContains(t, err, "closed")
}
