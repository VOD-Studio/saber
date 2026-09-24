package task

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandReceiptsPersistAndDoNotReplayUncertain(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, err := openStore(path)
	require.NoError(t, err)
	m := &Manager{store: store}
	msg := message("event", "alice")
	state, _, err := m.ClaimCommand(ctx, msg, "persona.new")
	require.NoError(t, err)
	require.Equal(t, "new", state)
	state, _, err = m.ClaimCommand(ctx, msg, "persona.new")
	require.NoError(t, err)
	require.Equal(t, "running", state)
	require.NoError(t, m.CompleteCommand(ctx, msg, "persona.new", "已创建"))
	require.NoError(t, store.db.Close())
	store, err = openStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.db.Close()) }()
	m.store = store
	state, body, err := m.ClaimCommand(ctx, msg, "persona.new")
	require.NoError(t, err)
	require.Equal(t, "done", state)
	require.Equal(t, "已创建", body)
}
