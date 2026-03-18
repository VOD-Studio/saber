// Package ai 提供与 AI 相关的功能，包括主动聊天状态管理。
package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"maunium.net/go/mautrix/id"
)

// RoomState 跟踪单个房间的主动聊天状态。
type RoomState struct {
	// LastMessageTime 记录最后一次用户消息的时间。
	LastMessageTime time.Time
	// LastProactiveTime 记录最后一次主动发消息的时间。
	LastProactiveTime time.Time
	// MessagesToday 记录今日已发送的主动消息数量。
	MessagesToday int
	// LastResetDate 记录上次重置的日期（用于每日重置判断）。
	LastResetDate time.Time
}

// StateTracker 管理所有房间的主动聊天状态。
//
// 它提供线程安全的状态访问和自动每日重置功能。
type StateTracker struct {
	mu     sync.RWMutex
	states map[id.RoomID]*RoomState
}

// NewStateTracker 创建并返回一个新的状态跟踪器实例。
func NewStateTracker() *StateTracker {
	return &StateTracker{
		states: make(map[id.RoomID]*RoomState),
	}
}

// GetState 返回指定房间的状态。
//
// 如果房间状态不存在，则创建一个新的状态并返回。
// 此方法会自动检查并执行每日重置。
func (st *StateTracker) GetState(roomID id.RoomID) *RoomState {
	st.mu.Lock()
	defer st.mu.Unlock()

	state, exists := st.states[roomID]
	if !exists {
		state = &RoomState{
			LastResetDate: time.Now(),
		}
		st.states[roomID] = state
	}

	// 检查是否需要每日重置
	st.checkAndResetDaily(state)

	return state
}

// SetState 设置指定房间的状态。
//
// 此方法用于完全替换房间状态，通常在恢复持久化状态时使用。
func (st *StateTracker) SetState(roomID id.RoomID, state *RoomState) {
	st.mu.Lock()
	defer st.mu.Unlock()

	st.states[roomID] = state
}

// RecordUserMessage 记录用户发送的消息。
//
// 更新 LastMessageTime 为当前时间。
func (st *StateTracker) RecordUserMessage(roomID id.RoomID) {
	st.mu.Lock()
	defer st.mu.Unlock()

	state, exists := st.states[roomID]
	if !exists {
		state = &RoomState{
			LastMessageTime: time.Now(),
			LastResetDate:   time.Now(),
		}
		st.states[roomID] = state
		return
	}

	state.LastMessageTime = time.Now()
}

// RecordProactiveMessage 记录主动发送的消息。
//
// 更新 LastProactiveTime 为当前时间，并将 MessagesToday 计数加 1。
func (st *StateTracker) RecordProactiveMessage(roomID id.RoomID) {
	st.mu.Lock()
	defer st.mu.Unlock()

	state, exists := st.states[roomID]
	if !exists {
		state = &RoomState{
			LastProactiveTime: time.Now(),
			MessagesToday:     1,
			LastResetDate:     time.Now(),
		}
		st.states[roomID] = state
		return
	}

	// 检查是否需要每日重置
	st.checkAndResetDaily(state)

	state.LastProactiveTime = time.Now()
	state.MessagesToday++
}

// ResetDaily 重置所有房间的每日计数器。
//
// 将 MessagesToday 归零，并更新 LastResetDate 为当前日期。
// 此方法通常由后台定时任务调用。
func (st *StateTracker) ResetDaily() {
	st.mu.Lock()
	defer st.mu.Unlock()

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	for _, state := range st.states {
		state.MessagesToday = 0
		state.LastResetDate = today
	}
}

// checkAndResetDaily 检查并执行每日重置（必须在持有锁的情况下调用）。
func (st *StateTracker) checkAndResetDaily(state *RoomState) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	// 如果上次重置日期早于今日，则重置
	if state.LastResetDate.Before(today) {
		state.MessagesToday = 0
		state.LastResetDate = today
	}
}

// ListActiveRooms 返回所有有活动状态的房间 ID 列表。
func (st *StateTracker) ListActiveRooms() []id.RoomID {
	st.mu.RLock()
	defer st.mu.RUnlock()

	rooms := make([]id.RoomID, 0, len(st.states))
	for roomID := range st.states {
		rooms = append(rooms, roomID)
	}

	return rooms
}

// ClearState 清除指定房间的状态。
func (st *StateTracker) ClearState(roomID id.RoomID) {
	st.mu.Lock()
	defer st.mu.Unlock()

	delete(st.states, roomID)
}

// GetAllStates 返回所有房间的状态快照。
//
// 返回的是状态副本，调用者可以安全修改返回的状态而不影响跟踪器。
func (st *StateTracker) GetAllStates() map[id.RoomID]*RoomState {
	st.mu.RLock()
	defer st.mu.RUnlock()

	snapshot := make(map[id.RoomID]*RoomState, len(st.states))
	for roomID, state := range st.states {
		// 创建状态副本
		stateCopy := *state
		snapshot[roomID] = &stateCopy
	}

	return snapshot
}

// persistentState 用于 JSON 序列化的持久化状态结构。
type persistentState struct {
	LastMessageTime     time.Time `json:"last_message_time"`
	LastProactiveTime   time.Time `json:"last_proactive_time"`
	MessagesToday       int       `json:"messages_today"`
	LastResetDateString string    `json:"last_reset_date"`
}

// Save 将状态跟踪器的所有状态保存到 JSON 文件。
//
// 参数:
//   - filePath: 要保存到的文件路径
//
// 返回值:
//   - error: 保存过程中发生的错误
//
// 注意:
//   - 如果目录不存在会尝试创建
//   - 文件权限设置为 0600 以保护敏感数据
func (st *StateTracker) Save(filePath string) error {
	st.mu.RLock()
	defer st.mu.RUnlock()

	// 构建持久化状态映射
	states := make(map[string]persistentState, len(st.states))
	for roomID, state := range st.states {
		states[string(roomID)] = persistentState{
			LastMessageTime:     state.LastMessageTime,
			LastProactiveTime:   state.LastProactiveTime,
			MessagesToday:       state.MessagesToday,
			LastResetDateString: state.LastResetDate.Format(time.RFC3339),
		}
	}

	// 序列化为 JSON
	data, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态失败：%w", err)
	}

	// 确保目录存在
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	// 写入文件（使用 0600 权限保护敏感数据）
	if err := os.WriteFile(filePath, data, 0600); err != nil {
		return fmt.Errorf("写入文件失败：%w", err)
	}

	slog.Debug("状态已保存到文件", "path", filePath, "rooms", len(states))
	return nil
}

// Load 从 JSON 文件加载状态到状态跟踪器。
//
// 参数:
//   - filePath: 要加载的文件路径
//
// 返回值:
//   - error: 加载过程中发生的错误
//
// 注意:
//   - 如果文件不存在，不会返回错误，而是创建新状态
//   - 加载的状态会替换现有状态
func (st *StateTracker) Load(filePath string) error {
	// 读取文件
	data, err := os.ReadFile(filePath)
	if err != nil {
		// 文件不存在时，视为首次使用，不报错
		if errors.Is(err, fs.ErrNotExist) {
			slog.Debug("状态文件不存在，使用新状态", "path", filePath)
			return nil
		}
		return fmt.Errorf("读取文件失败：%w", err)
	}

	// 反序列化 JSON
	var states map[string]persistentState
	if err := json.Unmarshal(data, &states); err != nil {
		return fmt.Errorf("反序列化状态失败：%w", err)
	}

	st.mu.Lock()
	defer st.mu.Unlock()

	// 将加载的状态转换为 RoomState
	for roomIDStr, pstate := range states {
		roomID := id.RoomID(roomIDStr)
		lastResetDate, err := time.Parse(time.RFC3339, pstate.LastResetDateString)
		if err != nil {
			slog.Warn("解析重置日期失败，使用当前时间",
				"room_id", roomID,
				"date_string", pstate.LastResetDateString,
				"error", err)
			lastResetDate = time.Now()
		}

		st.states[roomID] = &RoomState{
			LastMessageTime:   pstate.LastMessageTime,
			LastProactiveTime: pstate.LastProactiveTime,
			MessagesToday:     pstate.MessagesToday,
			LastResetDate:     lastResetDate,
		}
	}

	slog.Debug("状态已从文件加载", "path", filePath, "rooms", len(states))
	return nil
}
