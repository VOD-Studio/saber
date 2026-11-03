package execution

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

// outputFixture 准备带 read_output 授权的执行器和任务上下文。
func outputFixture(t *testing.T) (*Executor, context.Context, int64) {
	t.Helper()
	cfg, identity := fixture(t)
	cfg.Grants[0].Tools = append(cfg.Grants[0].Tools, "read_output")
	e, err := New(cfg, nil, nil)
	require.NoError(t, err)
	dir, err := e.Workspace(identity)
	require.NoError(t, err)
	ctx := WithTask(chat.WithIdentity(context.Background(), identity), 42, dir)
	e.SetOutputAuthorizer(func(_ context.Context, sourceID int64) error {
		if sourceID != 42 {
			return os.ErrPermission
		}
		return nil
	})
	return e, ctx, 42
}

func writeArchive(t *testing.T, e *Executor, taskID int64, text string) string {
	t.Helper()
	archive, err := e.openOutput(taskID)
	require.NoError(t, err)
	_, err = archive.file.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, archive.close(e))
	return archive.ID
}

func TestReadOutput_PaginatesAndReportsEOF(t *testing.T) {
	e, ctx, taskID := outputFixture(t)
	// 多字节字符跨页边界时不能切碎，游标必须持续前进。
	text := strings.Repeat("héllo 世界\n", 400)
	id := writeArchive(t, e, taskID, text)
	var collected strings.Builder
	offset := 0
	for pages := 0; ; pages++ {
		require.Less(t, pages, 400, "pagination must terminate")
		result, err := e.Run(ctx, "read_output", map[string]any{"output_id": id, "offset": float64(offset), "limit": float64(37)})
		require.NoError(t, err)
		require.Equal(t, 0, result.ExitCode)
		data := result.Data.(map[string]any)
		next := data["next_offset"].(int)
		require.Greater(t, next, offset)
		collected.WriteString(data["text"].(string))
		offset = next
		if data["eof"].(bool) {
			break
		}
	}
	require.Equal(t, text, collected.String())
}

func TestReadOutput_ActiveArchiveReportsCaughtUp(t *testing.T) {
	e, ctx, taskID := outputFixture(t)
	archive, err := e.openOutput(taskID)
	require.NoError(t, err)
	_, err = archive.file.WriteString("streaming")
	require.NoError(t, err)
	result, err := e.Run(ctx, "read_output", map[string]any{"output_id": archive.ID})
	require.NoError(t, err)
	data := result.Data.(map[string]any)
	require.False(t, data["eof"].(bool))
	require.True(t, data["caught_up"].(bool))
	require.Equal(t, "streaming", data["text"].(string))
	require.NoError(t, archive.close(e))
	result, err = e.Run(ctx, "read_output", map[string]any{"output_id": archive.ID})
	require.NoError(t, err)
	require.True(t, result.Data.(map[string]any)["eof"].(bool))
}

func TestReadOutput_RejectsMidRuneOffsetAndForeignHandles(t *testing.T) {
	e, ctx, taskID := outputFixture(t)
	id := writeArchive(t, e, taskID, "é")
	_, err := e.Run(ctx, "read_output", map[string]any{"output_id": id, "offset": float64(1)})
	require.ErrorContains(t, err, "UTF-8 boundary")
	_, err = e.Run(ctx, "read_output", map[string]any{"output_id": "t43-0123456789abcdef0123456789abcdef"})
	require.Error(t, err)
	_, err = e.Run(ctx, "read_output", map[string]any{"output_id": "../../etc/passwd"})
	require.Error(t, err)
	_, err = e.Run(ctx, "read_output", map[string]any{"output_id": id, "limit": float64(1)})
	require.Error(t, err)
	// 归档文件不存在时必须显式失败，不能返回空页伪装成功。
	_, err = e.Run(ctx, "read_output", map[string]any{"output_id": "t42-0123456789abcdef0123456789abcdef"})
	require.ErrorContains(t, err, "does not exist")
}

func TestReadOutput_RequiresAuthorizer(t *testing.T) {
	e, ctx, taskID := outputFixture(t)
	id := writeArchive(t, e, taskID, "secret")
	e.SetOutputAuthorizer(nil)
	_, err := e.Run(ctx, "read_output", map[string]any{"output_id": id})
	require.ErrorContains(t, err, "not configured")
}

func TestReadOutput_InvalidUTF8AdvancesBySourceBytes(t *testing.T) {
	e, ctx, taskID := outputFixture(t)
	id := writeArchive(t, e, taskID, "a\xffb")
	result, err := e.Run(ctx, "read_output", map[string]any{"output_id": id, "limit": float64(4)})
	require.NoError(t, err)
	data := result.Data.(map[string]any)
	require.Equal(t, 3, data["next_offset"].(int))
	require.Equal(t, "a\uFFFDb", data["text"].(string))
	require.True(t, data["eof"].(bool))
}

func TestRun_AssignsStableOutputID(t *testing.T) {
	e, _, _ := outputFixture(t)
	archive, err := e.openOutput(42)
	require.NoError(t, err)
	result := Result{ExitCode: 0, OutputID: archive.ID, LogPath: archive.Path}
	data, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(data), "output_id")
	require.NoError(t, archive.close(e))
	// Logs 继续识别统一归档命名，不引入第二套日志目录。
	logs, err := e.Logs(42)
	require.NoError(t, err)
	require.Len(t, logs, 1)
}
