package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"maunium.net/go/mautrix/id"

	"rua.plus/saber/internal/config"
)

const outboxTestRoom = id.RoomID("!room:test")

// newOutboxManager 用同一个状态路径构造开启持久化的管理器，便于模拟进程重启。
func newOutboxManager(t *testing.T, rooms *FakeProactiveRooms, statePath string) *ProactiveManager {
	t.Helper()
	cfg := &config.ProactiveConfig{Enabled: true, PersistState: true, StatePath: statePath}
	manager, err := NewProactiveManager(cfg, &Service{}, rooms, NewStateTracker(), &config.AIConfig{})
	require.NoError(t, err)
	return manager
}

// readOutboxEntries 读取待发队列；文件不存在时返回 nil。
func readOutboxEntries(t *testing.T, statePath string) []outboxEntry {
	t.Helper()
	data, err := os.ReadFile(statePath + outboxSuffix)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var entries []outboxEntry
	require.NoError(t, json.Unmarshal(data, &entries))
	return entries
}

// writeOutboxFile 直接构造待发队列文件，模拟上一进程遗留的条目。
func writeOutboxFile(t *testing.T, path string, entries []outboxEntry) {
	t.Helper()
	data, err := json.MarshalIndent(entries, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
}

// TestProactiveManagerStateSavedWithoutStop 验证状态在没有 Stop 的情况下也会落盘。
func TestProactiveManagerStateSavedWithoutStop(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "proactive.json")
	cfg := &config.ProactiveConfig{Enabled: true, PersistState: true, StatePath: statePath}
	tracker := NewStateTracker()
	manager, err := NewProactiveManager(cfg, &Service{}, &FakeProactiveRooms{}, tracker, &config.AIConfig{})
	require.NoError(t, err)

	before := time.Now()
	manager.RecordUserMessage(outboxTestRoom)
	require.NoError(t, manager.SendMessage(context.Background(), outboxTestRoom, "早上好", false))
	// 只运行保存钩子，不调用 Stop：崩溃前也必须已经落盘。
	manager.flushState()

	restored := NewStateTracker()
	require.NoError(t, restored.Load(statePath))
	state := restored.GetState(outboxTestRoom)
	source := tracker.GetState(outboxTestRoom)
	require.Equal(t, 1, state.MessagesToday)
	require.Equal(t, source.MessagesToday, state.MessagesToday)
	require.True(t, state.LastProactiveTime.Equal(source.LastProactiveTime))
	require.True(t, state.LastMessageTime.Equal(source.LastMessageTime))
	require.False(t, state.LastProactiveTime.Before(before))
	require.False(t, state.LastMessageTime.Before(before))
}

// TestProactiveOutboxClearsAfterSuccessfulSend 验证发送成功后队列被清空并落盘为 []。
func TestProactiveOutboxClearsAfterSuccessfulSend(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "proactive.json")
	rooms := &FakeProactiveRooms{}
	manager := newOutboxManager(t, rooms, statePath)

	require.NoError(t, manager.SendMessage(context.Background(), outboxTestRoom, "早上好", false))
	require.Len(t, rooms.Sent, 1)
	require.Empty(t, readOutboxEntries(t, statePath))
	// 空队列也应写回文件，重启时读到的是空数组而不是缺失文件。
	_, statErr := os.Stat(statePath + outboxSuffix)
	require.NoError(t, statErr)
}

// TestProactiveOutboxKeepsFailedMessage 验证发送失败后条目保留、正文完整、失败次数为 1。
func TestProactiveOutboxKeepsFailedMessage(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "proactive.json")
	rooms := &FakeProactiveRooms{SendErr: errors.New("offline")}
	manager := newOutboxManager(t, rooms, statePath)

	err := manager.SendMessage(context.Background(), outboxTestRoom, "早", true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "offline")

	entries := readOutboxEntries(t, statePath)
	require.Len(t, entries, 1)
	require.Equal(t, "早", entries[0].Text)
	require.Equal(t, outboxTestRoom.String(), entries[0].Room)
	require.True(t, entries[0].Notice)
	require.Equal(t, 1, entries[0].Attempts)
	require.NotEmpty(t, entries[0].ID)
	require.False(t, entries[0].CreatedAt.IsZero())
}

// TestProactiveOutboxRetriesOnStart 验证重启后按创建顺序补发一次，成功即删除，再次启动不重发。
func TestProactiveOutboxRetriesOnStart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "proactive.json")
	failing := &FakeProactiveRooms{SendErr: errors.New("offline")}
	first := newOutboxManager(t, failing, statePath)
	require.Error(t, first.SendMessage(context.Background(), outboxTestRoom, "补发我", false))
	require.Len(t, readOutboxEntries(t, statePath), 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rooms := &FakeProactiveRooms{}
	second := newOutboxManager(t, rooms, statePath)
	second.Start(ctx)
	require.Len(t, rooms.Sent, 1, "启动时只补发一次")
	require.Equal(t, "补发我", rooms.Sent[0].Text)
	require.Empty(t, readOutboxEntries(t, statePath))
	second.Stop()

	idle := &FakeProactiveRooms{}
	third := newOutboxManager(t, idle, statePath)
	third.Start(ctx)
	require.Empty(t, idle.Sent, "已删除的条目不得再次发送")
	third.Stop()
}

// TestProactiveOutboxStopsAtAttemptCap 验证达到重试上限的条目不再自动重试，但仍保留为死信。
func TestProactiveOutboxStopsAtAttemptCap(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "proactive.json")
	writeOutboxFile(t, statePath+outboxSuffix, []outboxEntry{{
		ID: "dead", Room: outboxTestRoom.String(), Text: "死信", Attempts: maxOutboxAttempts, CreatedAt: time.Now(),
	}})
	rooms := &FakeProactiveRooms{}
	manager := newOutboxManager(t, rooms, statePath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	manager.Stop()

	require.Empty(t, rooms.Sent, "达到上限的条目不得自动重试")
	entries := readOutboxEntries(t, statePath)
	require.Len(t, entries, 1)
	require.Equal(t, "死信", entries[0].Text)
	require.Equal(t, maxOutboxAttempts, entries[0].Attempts)
}

// TestProactiveOutboxRejectsEmptyText 验证空正文在入队前就被拒绝，不产生队列文件。
func TestProactiveOutboxRejectsEmptyText(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "proactive.json")
	rooms := &FakeProactiveRooms{}
	manager := newOutboxManager(t, rooms, statePath)

	require.Error(t, manager.SendMessage(context.Background(), outboxTestRoom, "", false))
	require.Empty(t, rooms.Sent)
	require.Empty(t, readOutboxEntries(t, statePath))
	_, statErr := os.Stat(statePath + outboxSuffix)
	require.True(t, errors.Is(statErr, fs.ErrNotExist), "空正文不应写入待发队列")
}
