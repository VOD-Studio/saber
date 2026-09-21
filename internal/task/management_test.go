package task

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
)

func TestManager_AdminCanManageOnlyScopedTasksAndSchedules(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := openStore(filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, store.db.Close()) }()
	m := &Manager{store: store, ctx: ctx, authorize: func(chat.Identity, string) error { return nil }}
	source, err := m.Submit(ctx, message("source", "alice"), dir, request("work"))
	require.NoError(t, err)
	admin := chat.Identity{Session: source.Message.Session, SenderID: "admin"}
	_, err = m.Cancel(ctx, admin, source.ID)
	require.Error(t, err)
	m.manage = func(i chat.Identity) bool {
		return i.SenderID == "admin" && i.Session.Account == "bot" && i.Session.Conversation == "room"
	}
	_, err = m.Cancel(ctx, admin, source.ID)
	require.NoError(t, err)
	plan, err := m.CreateSchedule(ctx, message("schedule", "alice"), dir, request("work"), ScheduleSpec{Kind: "every", Every: "1h", Timezone: "UTC"})
	require.NoError(t, err)
	stranger := admin
	stranger.SenderID = "bob"
	_, err = m.ChangeSchedule(ctx, stranger, plan.ID, "delete")
	require.Error(t, err)
	paused, err := m.ChangeSchedule(ctx, admin, plan.ID, "pause")
	require.NoError(t, err)
	require.Equal(t, "paused", paused.Status)
	removed, err := m.ChangeSchedule(ctx, admin, plan.ID, "delete")
	require.NoError(t, err)
	require.Equal(t, "deleted", removed.Status)
	admin.Session.Conversation = "other"
	_, err = m.Cancel(ctx, admin, source.ID)
	require.Error(t, err)
	_, err = m.ChangeSchedule(ctx, admin, plan.ID, "delete")
	require.Error(t, err)
}
