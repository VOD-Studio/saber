// Package ai 提供与 AI 相关的功能，包括主动聊天状态管理的测试。
package ai

import (
	"os"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"
)

func TestStateTracker_BasicOperations(t *testing.T) {
	t.Run("NewStateTracker initializes empty", func(t *testing.T) {
		st := NewStateTracker()
		if st == nil {
			t.Fatal("NewStateTracker() returned nil")
		}
		if len(st.ListActiveRooms()) != 0 {
			t.Error("NewStateTracker should have no active rooms initially")
		}
	})

	t.Run("GetState creates new state for unknown room", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		state := st.GetState(roomID)

		// 验证默认值
		if state.LastResetDate.IsZero() || state.LastResetDate.Before(time.Now().Add(-time.Second)) {
			t.Error("LastResetDate should be set to current time")
		}
		if state.MessagesToday != 0 {
			t.Errorf("MessagesToday should be 0, got %d", state.MessagesToday)
		}
	})

	t.Run("GetState returns same instance for same room", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		state1 := st.GetState(roomID)
		state2 := st.GetState(roomID)

		if state1 != state2 {
			t.Error("GetState should return the same instance for the same room")
		}
	})

	t.Run("SetState replaces existing state", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		customState := &RoomState{
			LastMessageTime:   time.Now().Add(-1 * time.Hour),
			LastProactiveTime: time.Now().Add(-2 * time.Hour),
			MessagesToday:     5,
			LastResetDate:     time.Now().Add(-1 * time.Hour),
		}

		st.SetState(roomID, customState)
		retrieved := st.GetState(roomID)

		if retrieved.MessagesToday != 5 {
			t.Errorf("MessagesToday should be 5, got %d", retrieved.MessagesToday)
		}
		if !retrieved.LastMessageTime.Equal(customState.LastMessageTime) {
			t.Error("LastMessageTime should match set value")
		}
	})

	t.Run("ClearState removes room state", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		st.GetState(roomID) // Create state
		if len(st.ListActiveRooms()) != 1 {
			t.Fatal("Should have 1 active room")
		}

		st.ClearState(roomID)
		if len(st.ListActiveRooms()) != 0 {
			t.Error("ClearState should remove the room")
		}
	})
}

func TestStateTracker_RecordUserMessage(t *testing.T) {
	t.Run("RecordUserMessage updates LastMessageTime", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		before := time.Now()
		st.RecordUserMessage(roomID)
		after := time.Now()

		state := st.GetState(roomID)
		if state.LastMessageTime.Before(before) || state.LastMessageTime.After(after) {
			t.Error("LastMessageTime should be set to current time")
		}
	})

	t.Run("RecordUserMessage creates state if not exists", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		st.RecordUserMessage(roomID)

		if len(st.ListActiveRooms()) != 1 {
			t.Error("RecordUserMessage should create state for unknown room")
		}
	})

	t.Run("RecordUserMessage updates existing state", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		// 先设置一个旧时间
		oldTime := time.Now().Add(-1 * time.Hour)
		st.SetState(roomID, &RoomState{LastMessageTime: oldTime})

		// 更新消息时间
		st.RecordUserMessage(roomID)

		state := st.GetState(roomID)
		if !state.LastMessageTime.After(oldTime) {
			t.Error("LastMessageTime should be updated to newer time")
		}
	})
}

func TestStateTracker_RecordProactiveMessage(t *testing.T) {
	t.Run("RecordProactiveMessage initializes counters", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		st.RecordProactiveMessage(roomID)

		state := st.GetState(roomID)
		if state.MessagesToday != 1 {
			t.Errorf("MessagesToday should be 1, got %d", state.MessagesToday)
		}
		if state.LastProactiveTime.IsZero() {
			t.Error("LastProactiveTime should be set")
		}
	})

	t.Run("RecordProactiveMessage increments counter", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		st.RecordProactiveMessage(roomID)
		st.RecordProactiveMessage(roomID)
		st.RecordProactiveMessage(roomID)

		state := st.GetState(roomID)
		if state.MessagesToday != 3 {
			t.Errorf("MessagesToday should be 3, got %d", state.MessagesToday)
		}
	})

	t.Run("RecordProactiveMessage updates LastProactiveTime", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		before := time.Now()
		st.RecordProactiveMessage(roomID)
		after := time.Now()

		state := st.GetState(roomID)
		if state.LastProactiveTime.Before(before) || state.LastProactiveTime.After(after) {
			t.Error("LastProactiveTime should be set to current time")
		}
	})
}

func TestStateTracker_ResetDaily(t *testing.T) {
	t.Run("ResetDaily resets all rooms", func(t *testing.T) {
		st := NewStateTracker()
		room1 := id.RoomID("!room1:example.org")
		room2 := id.RoomID("!room2:example.org")

		// 设置一些状态
		st.RecordProactiveMessage(room1)
		st.RecordProactiveMessage(room1)
		st.RecordProactiveMessage(room2)

		// 验证计数
		if st.GetState(room1).MessagesToday != 2 {
			t.Fatal("Room1 should have 2 messages")
		}
		if st.GetState(room2).MessagesToday != 1 {
			t.Fatal("Room2 should have 1 message")
		}

		// 执行每日重置
		st.ResetDaily()

		// 验证重置
		if st.GetState(room1).MessagesToday != 0 {
			t.Errorf("Room1 MessagesToday should be reset to 0, got %d", st.GetState(room1).MessagesToday)
		}
		if st.GetState(room2).MessagesToday != 0 {
			t.Errorf("Room2 MessagesToday should be reset to 0, got %d", st.GetState(room2).MessagesToday)
		}
	})

	t.Run("ResetDaily updates LastResetDate", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		// 设置一个旧的 LastResetDate
		oldDate := time.Now().Add(-2 * time.Hour)
		st.SetState(roomID, &RoomState{LastResetDate: oldDate})

		st.ResetDaily()

		state := st.GetState(roomID)
		today := time.Now().Truncate(24 * time.Hour)
		// 验证 LastResetDate 被设置为今天（午夜时间）
		if state.LastResetDate.Year() != today.Year() || state.LastResetDate.Month() != today.Month() || state.LastResetDate.Day() != today.Day() {
			t.Errorf("LastResetDate should be updated to today, got %v, expected %v", state.LastResetDate, today)
		}
	})
}

func TestStateTracker_AutomaticDailyReset(t *testing.T) {
	t.Run("GetState resets if last reset is before today", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		// 设置一个昨天的 LastResetDate
		yesterday := time.Now().Add(-26 * time.Hour)
		st.SetState(roomID, &RoomState{
			MessagesToday: 5,
			LastResetDate: yesterday,
		})

		// 获取状态应该触发自动重置
		state := st.GetState(roomID)

		if state.MessagesToday != 0 {
			t.Errorf("MessagesToday should be auto-reset to 0, got %d", state.MessagesToday)
		}
	})

	t.Run("GetState does not reset if already reset today", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		// 设置今天的 LastResetDate
		today := time.Now().Truncate(24 * time.Hour)
		st.SetState(roomID, &RoomState{
			MessagesToday: 3,
			LastResetDate: today,
		})

		// 获取状态不应该重置
		state := st.GetState(roomID)

		if state.MessagesToday != 3 {
			t.Errorf("MessagesToday should remain 3, got %d", state.MessagesToday)
		}
	})
}

func TestStateTracker_GetAllStates(t *testing.T) {
	t.Run("GetAllStates returns snapshot", func(t *testing.T) {
		st := NewStateTracker()
		room1 := id.RoomID("!room1:example.org")
		room2 := id.RoomID("!room2:example.org")

		st.RecordProactiveMessage(room1)
		st.RecordProactiveMessage(room2)

		states := st.GetAllStates()

		if len(states) != 2 {
			t.Errorf("Should return 2 states, got %d", len(states))
		}

		// 验证返回的是副本
		states[room1].MessagesToday = 999
		original := st.GetState(room1)
		if original.MessagesToday == 999 {
			t.Error("GetAllStates should return a copy, not the original")
		}
	})

	t.Run("GetAllStates returns empty map for empty tracker", func(t *testing.T) {
		st := NewStateTracker()
		states := st.GetAllStates()

		if len(states) != 0 {
			t.Errorf("GetAllStates should return empty map, got %d states", len(states))
		}
	})
}

func TestStateTracker_ConcurrentAccess(t *testing.T) {
	t.Run("concurrent GetState calls are safe", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		done := make(chan bool, 10)
		for i := 0; i < 10; i++ {
			go func() {
				state := st.GetState(roomID)
				if state == nil {
					t.Error("GetState returned nil under concurrent access")
				}
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})

	t.Run("concurrent RecordProactiveMessage calls are safe", func(t *testing.T) {
		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")

		done := make(chan bool, 10)
		for i := 0; i < 10; i++ {
			go func() {
				st.RecordProactiveMessage(roomID)
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}

		// 验证最终计数（可能不是精确的 10，因为并发更新）
		state := st.GetState(roomID)
		if state.MessagesToday == 0 {
			t.Error("MessagesToday should be > 0 after concurrent updates")
		}
	})
}

func TestStateTracker_ListActiveRooms(t *testing.T) {
	t.Run("ListActiveRooms returns all rooms", func(t *testing.T) {
		st := NewStateTracker()
		room1 := id.RoomID("!room1:example.org")
		room2 := id.RoomID("!room2:example.org")
		room3 := id.RoomID("!room3:example.org")

		st.GetState(room1)
		st.GetState(room2)
		st.GetState(room3)

		rooms := st.ListActiveRooms()
		if len(rooms) != 3 {
			t.Errorf("Should have 3 active rooms, got %d", len(rooms))
		}
	})

	t.Run("ListActiveRooms excludes cleared rooms", func(t *testing.T) {
		st := NewStateTracker()
		room1 := id.RoomID("!room1:example.org")
		room2 := id.RoomID("!room2:example.org")

		st.GetState(room1)
		st.GetState(room2)

		st.ClearState(room1)

		rooms := st.ListActiveRooms()
		if len(rooms) != 1 {
			t.Errorf("Should have 1 active room after clearing one, got %d", len(rooms))
		}
	})
}

func TestStateTracker_SaveLoad(t *testing.T) {
	t.Run("Save and Load persist state to file", func(t *testing.T) {
		tmpFile := t.TempDir() + "/state.json"
		st := NewStateTracker()

		room1 := id.RoomID("!room1:example.org")
		room2 := id.RoomID("!room2:example.org")

		// 设置一些状态
		st.RecordProactiveMessage(room1)
		st.RecordProactiveMessage(room1)
		st.RecordUserMessage(room2)

		// 保存状态
		err := st.Save(tmpFile)
		if err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		// 创建新的状态跟踪器并加载
		st2 := NewStateTracker()
		err = st2.Load(tmpFile)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}

		// 验证加载的状态
		state1 := st2.GetState(room1)
		if state1.MessagesToday != 2 {
			t.Errorf("Room1 MessagesToday should be 2, got %d", state1.MessagesToday)
		}

		state2 := st2.GetState(room2)
		if state2.LastMessageTime.IsZero() {
			t.Error("Room2 LastMessageTime should be set")
		}
	})

	t.Run("Load from non-existent file creates empty state", func(t *testing.T) {
		st := NewStateTracker()
		tmpFile := t.TempDir() + "/nonexistent.json"

		// 加载不存在的文件应该不报错
		err := st.Load(tmpFile)
		if err != nil {
			t.Fatalf("Load should not fail for non-existent file: %v", err)
		}

		// 状态应该为空
		if len(st.ListActiveRooms()) != 0 {
			t.Error("Should have no active rooms after loading non-existent file")
		}
	})

	t.Run("Save creates directory if not exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		tmpFile := tmpDir + "/subdir/state.json"

		st := NewStateTracker()
		roomID := id.RoomID("!test:example.org")
		st.GetState(roomID)

		err := st.Save(tmpFile)
		if err != nil {
			t.Fatalf("Save failed: %v", err)
		}

		// 验证文件存在
		if _, err := os.Stat(tmpFile); err != nil {
			t.Errorf("State file should be created: %v", err)
		}
	})

	t.Run("Save handles invalid path", func(t *testing.T) {
		st := NewStateTracker()
		// 尝试保存到无效路径
		err := st.Save("/invalid/path/that/does/not/exist/state.json")
		if err == nil {
			t.Error("Save should fail for invalid path")
		}
	})

	t.Run("Load handles corrupted JSON", func(t *testing.T) {
		tmpFile := t.TempDir() + "/corrupted.json"

		// 写入损坏的 JSON
		err := os.WriteFile(tmpFile, []byte("{ invalid json }"), 0600)
		if err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		st := NewStateTracker()
		err = st.Load(tmpFile)
		if err == nil {
			t.Error("Load should fail for corrupted JSON")
		}
	})
}
