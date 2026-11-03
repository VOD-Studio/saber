package task

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

// TestManager_MaxConcurrent 验证并发上限来自配置：1 严格串行，2 允许重叠。
//
// 两个任务使用不同工作目录，目录互斥由 TestManager_DirectorySerializationAndCancellation 覆盖，
// 这里只观察 Options.MaxConcurrent 决定的全局工作槽。
func TestManager_MaxConcurrent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		concurrent int
		wantPeak   int32
	}{
		{name: "sequential", concurrent: 1, wantPeak: 1},
		{name: "overlapping", concurrent: 2, wantPeak: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			started := make(chan string, 4)
			release := make(chan struct{})
			var inflight, peak atomic.Int32
			run := func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
				current := inflight.Add(1)
				defer inflight.Add(-1)
				for {
					previous := peak.Load()
					if current <= previous || peak.CompareAndSwap(previous, current) {
						break
					}
				}
				started <- WorkDir(ctx)
				select {
				case <-release:
					return agent.Result{Status: agent.Completed}, nil
				case <-ctx.Done():
					return agent.Result{Status: agent.Cancelled}, ctx.Err()
				}
			}
			m, err := Open(filepath.Join(root, "tasks.db"), run, func(context.Context, Task) (string, error) { return "reply", nil }, Options{MaxConcurrent: tc.concurrent})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, m.Close()) })
			var closeOnce sync.Once
			t.Cleanup(func() { closeOnce.Do(func() { close(release) }) })

			first, err := m.Submit(ctx, message("concurrent-a", "alice"), t.TempDir(), request("first"))
			require.NoError(t, err)
			second, err := m.Submit(ctx, message("concurrent-b", "bob"), t.TempDir(), request("second"))
			require.NoError(t, err)

			for index := range tc.concurrent {
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatalf("只启动了 %d/%d 个任务", index, tc.concurrent)
				}
			}
			if tc.concurrent == 1 {
				pending, getErr := m.Get(ctx, second.Message.Session, second.ID)
				require.NoError(t, getErr)
				require.Equal(t, "queued", pending.Status, "达到并发上限后第二个任务必须保持排队")
			}

			closeOnce.Do(func() { close(release) })
			waitTask(t, m, first, func(t Task) bool { return t.Status == "completed" })
			waitTask(t, m, second, func(t Task) bool { return t.Status == "completed" })
			require.EqualValues(t, tc.wantPeak, peak.Load())
		})
	}
}
