package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
)

func TestManager_DeliveryResumesUploadedFilesAfterRestart(t *testing.T) {
	ctx := context.Background()
	path, dir := filepath.Join(t.TempDir(), "tasks.db"), t.TempDir()
	open := func() *Manager {
		m, err := Open(path, func(ctx context.Context, _ agent.Request, _ func(agent.Event)) (agent.Result, error) {
			<-ctx.Done()
			return agent.Result{}, ctx.Err()
		}, func(context.Context, Task) (string, error) { return "", nil })
		require.NoError(t, err)
		return m
	}
	m := open()
	source, err := m.Submit(ctx, message("source", "alice"), dir, request("files"))
	require.NoError(t, err)
	uploads, sends := map[string]int{}, map[string]int{}
	deliver := func(m *Manager, key string, fail bool) (string, error) {
		return m.DeliverPart(ctx, source.ID, key, func(ctx context.Context) ([]byte, error) {
			_, ok := ctx.Deadline()
			require.True(t, ok)
			uploads[key]++
			return []byte("encrypted-upload-" + key), nil
		}, func(ctx context.Context, data []byte) (string, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Greater(t, time.Until(deadline), time.Second)
			require.Equal(t, "encrypted-upload-"+key, string(data))
			sends[key]++
			if fail {
				return "", errors.New("offline")
			}
			return key + "-event", nil
		})
	}
	_, err = deliver(m, "file1", false)
	require.NoError(t, err)
	_, err = deliver(m, "file2", true)
	require.Error(t, err)
	closeManager(t, m)
	m = open()
	defer closeManager(t, m)
	_, err = deliver(m, "file1", false)
	require.NoError(t, err)
	_, err = deliver(m, "file2", false)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"file1": 1, "file2": 1}, uploads)
	require.Equal(t, map[string]int{"file1": 1, "file2": 2}, sends)
}
