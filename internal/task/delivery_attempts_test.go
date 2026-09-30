package task

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

func completedRunner(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
	return agent.Result{Status: agent.Completed, Content: "完成"}, nil
}

// expedite 跳过退避等待，让下一轮 deliverLoop 立刻取到待投递任务。
func expedite(t *testing.T, m *Manager) {
	t.Helper()
	_, err := m.store.db.Exec(`UPDATE tasks SET next_delivery=0 WHERE delivery='pending'`)
	require.NoError(t, err)
}

func TestDeliveryDelay_CapsAt256Seconds(t *testing.T) {
	want := []time.Duration{1, 2, 4, 8, 16, 32, 64, 128, 256, 256, 256}
	for attempts, seconds := range want {
		require.Equal(t, seconds*time.Second, deliveryDelay(attempts), "attempts=%d", attempts)
	}
}

func TestManager_DeliveryMaxAttemptsDefault(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), completedRunner, nil)
	require.NoError(t, err)
	defer closeManager(t, m)
	require.Equal(t, DefaultDeliveryMaxAttempts, m.deliveryMaxAttempts)
	m2, err := Open(filepath.Join(t.TempDir(), "tasks.db"), completedRunner, nil, Options{DeliveryMaxAttempts: 3})
	require.NoError(t, err)
	defer closeManager(t, m2)
	require.Equal(t, 3, m2.deliveryMaxAttempts)
}

// 未达上限的失败仍保持 pending 并退避重试，最终成功。
func TestManager_DeliveryBelowLimitKeepsRetrying(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "tasks.db"), completedRunner, nil, Options{DeliveryMaxAttempts: 4})
	require.NoError(t, err)
	defer closeManager(t, m)
	var sends atomic.Int32
	require.NoError(t, m.RegisterDelivery("matrix", func(context.Context, Task) (string, error) {
		if sends.Add(1) <= 3 {
			return "", errors.New("offline")
		}
		return "reply", nil
	}))
	item, err := m.Submit(ctx, message("retry", "user"), dir, request("input"))
	require.NoError(t, err)
	for want := 1; want <= 3; want++ {
		got := waitTask(t, m, item, func(t Task) bool { return t.DeliveryAttempts >= want })
		require.Equal(t, "pending", got.Delivery, "第 %d 次失败仍应 pending", want)
		require.Contains(t, got.DeliveryError, "offline")
		expedite(t, m)
	}
	sent := waitTask(t, m, item, func(t Task) bool { return t.Delivery == "sent" })
	require.Equal(t, 4, sent.DeliveryAttempts)
	require.Equal(t, "reply", sent.DeliveryID)
	require.Empty(t, sent.DeliveryError)
}

// 失败次数达到上限后标记 abandoned，保留最后错误，不再被取到重试。
func TestManager_DeliveryAbandonedAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "tasks.db"), completedRunner, nil, Options{DeliveryMaxAttempts: 2})
	require.NoError(t, err)
	defer closeManager(t, m)
	var sends atomic.Int32
	require.NoError(t, m.RegisterDelivery("matrix", func(context.Context, Task) (string, error) {
		return "", errors.New("offline-" + string(rune('0'+sends.Add(1))))
	}))
	item, err := m.Submit(ctx, message("abandon", "user"), dir, request("input"))
	require.NoError(t, err)
	first := waitTask(t, m, item, func(t Task) bool { return t.DeliveryAttempts >= 1 })
	require.Equal(t, "pending", first.Delivery)
	expedite(t, m)
	got := waitTask(t, m, item, func(t Task) bool { return t.Delivery == "abandoned" })
	require.Equal(t, 2, got.DeliveryAttempts)
	require.Equal(t, "offline-2", got.DeliveryError, "保留最后一次错误")
	require.Equal(t, "completed", got.Status, "放弃投递不改变任务结果")
	require.Equal(t, "完成", got.Result.Content)

	// 即便退避时间已到，也不会再被投递循环取到。
	_, err = m.store.db.Exec(`UPDATE tasks SET next_delivery=0`)
	require.NoError(t, err)
	time.Sleep(800 * time.Millisecond)
	require.EqualValues(t, 2, sends.Load())
	after, err := m.Get(ctx, item.Message.Session, item.ID)
	require.NoError(t, err)
	require.Equal(t, "abandoned", after.Delivery)
	require.Equal(t, 2, after.DeliveryAttempts)
}

// 重启后按已持久化的尝试次数继续计数：重启前失败 1 次，重启后再失败即达上限。
func TestManager_DeliveryAttemptsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.db")
	m, err := Open(path, completedRunner, nil, Options{DeliveryMaxAttempts: 2})
	require.NoError(t, err)
	require.NoError(t, m.RegisterDelivery("matrix", func(context.Context, Task) (string, error) { return "", errors.New("offline") }))
	item, err := m.Submit(ctx, message("restart", "user"), dir, request("input"))
	require.NoError(t, err)
	waitTask(t, m, item, func(t Task) bool { return t.DeliveryAttempts == 1 && t.Delivery == "pending" })
	require.NoError(t, m.Close())

	m, err = Open(path, completedRunner, nil, Options{DeliveryMaxAttempts: 2})
	require.NoError(t, err)
	defer closeManager(t, m)
	restored, err := m.Get(ctx, item.Message.Session, item.ID)
	require.NoError(t, err)
	require.Equal(t, 1, restored.DeliveryAttempts, "重启后计数保持")
	var sends atomic.Int32
	require.NoError(t, m.RegisterDelivery("matrix", func(context.Context, Task) (string, error) {
		sends.Add(1)
		return "", errors.New("still offline")
	}))
	expedite(t, m)
	got := waitTask(t, m, item, func(t Task) bool { return t.Delivery == "abandoned" })
	require.Equal(t, 2, got.DeliveryAttempts)
	require.Equal(t, "still offline", got.DeliveryError)
	require.EqualValues(t, 1, sends.Load(), "重启后只再尝试一次就达到上限")
}

// 放弃投递不影响已持久化的投递分片（已上传/已发送的 message_id 保留）。
func TestManager_AbandonedKeepsDeliveryParts(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	m, err := Open(filepath.Join(dir, "tasks.db"), completedRunner, nil, Options{DeliveryMaxAttempts: 1})
	require.NoError(t, err)
	defer closeManager(t, m)
	require.NoError(t, m.RegisterDelivery("matrix", func(ctx context.Context, t Task) (string, error) {
		if _, err := m.DeliverPart(ctx, t.ID, "result", func(context.Context) ([]byte, error) { return nil, nil }, func(context.Context, []byte) (string, error) { return "$sent", nil }); err != nil {
			return "", err
		}
		return "", errors.New("文件发送失败")
	}))
	item, err := m.Submit(ctx, message("parts", "user"), dir, request("input"))
	require.NoError(t, err)
	got := waitTask(t, m, item, func(t Task) bool { return t.Delivery == "abandoned" })
	require.Equal(t, 1, got.DeliveryAttempts)
	var messageID string
	require.NoError(t, m.store.db.QueryRow(`SELECT message_id FROM task_delivery_parts WHERE task_id=? AND part='result'`, item.ID).Scan(&messageID))
	require.Equal(t, "$sent", messageID)
}

// 旧任务的尝试次数已超过上限（如上限调小、或升级前已大量失败）时，再试一次：
// 失败直接变 abandoned，成功则变 sent。固定「再试一次后放弃」的行为。
func TestManager_DeliveryAttemptsAlreadyOverLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sendErr error
		want    string
	}{
		{"再试失败直接放弃", errors.New("still offline"), "abandoned"},
		{"再试成功变 sent", nil, "sent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "tasks.db")
			m, err := Open(path, completedRunner, nil, Options{DeliveryMaxAttempts: 3})
			require.NoError(t, err)
			item, err := m.Submit(ctx, message("over", "user"), dir, request("input"))
			require.NoError(t, err)
			waitTask(t, m, item, func(t Task) bool { return t.Status == "completed" })
			// 模拟旧数据：尝试 7 次仍 pending，已超过上限 3。
			_, err = m.store.db.Exec(`UPDATE tasks SET delivery='pending',delivery_attempts=7,delivery_error='old error',next_delivery=0 WHERE id=?`, item.ID)
			require.NoError(t, err)
			var sends atomic.Int32
			require.NoError(t, m.RegisterDelivery("matrix", func(context.Context, Task) (string, error) {
				sends.Add(1)
				if tc.sendErr != nil {
					return "", tc.sendErr
				}
				return "reply", nil
			}))
			got := waitTask(t, m, item, func(t Task) bool { return t.Delivery != "pending" })
			require.Equal(t, tc.want, got.Delivery)
			require.Equal(t, 8, got.DeliveryAttempts)
			if tc.sendErr != nil {
				require.Equal(t, "still offline", got.DeliveryError)
			} else {
				require.Empty(t, got.DeliveryError)
				require.Equal(t, "reply", got.DeliveryID)
			}
			// 终态之后不再被取到。
			_, err = m.store.db.Exec(`UPDATE tasks SET next_delivery=0`)
			require.NoError(t, err)
			time.Sleep(600 * time.Millisecond)
			require.EqualValues(t, 1, sends.Load())
			closeManager(t, m)
		})
	}
}
