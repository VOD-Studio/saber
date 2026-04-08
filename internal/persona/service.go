// Package persona 提供机器人人格管理功能。
package persona

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"maunium.net/go/mautrix/id"
)

// Service 提供人格管理功能。
// 它管理人格数据的持久化存储和房间人格映射。
type Service struct {
	// db 是 SQLite 数据库连接
	db *sql.DB

	// personas 缓存所有人格数据，key 为人格 ID
	personas map[string]*Persona

	// roomPersonas 缓存房间人格映射，key 为房间 ID
	roomPersonas map[id.RoomID]string

	// mu 保护缓存数据的并发访问
	mu sync.RWMutex
}

// NewService 创建新的人格服务。
// dbPath 是 SQLite 数据库文件路径，如果文件不存在会自动创建。
func NewService(dbPath string) (*Service, error) {
	// 使用项目自定义的 sqlite3-fk-wal 驱动
	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 创建表结构
	if err := initSchema(db); err != nil {
		_ = db.Close() // 忽略关闭错误，返回主要错误
		return nil, fmt.Errorf("初始化数据库表失败: %w", err)
	}

	svc := &Service{
		db:           db,
		personas:     make(map[string]*Persona),
		roomPersonas: make(map[id.RoomID]string),
	}

	// 加载内置人格
	if err := svc.loadBuiltinPersonas(); err != nil {
		_ = db.Close() // 忽略关闭错误，返回主要错误
		return nil, fmt.Errorf("加载内置人格失败: %w", err)
	}

	// 加载现有数据到缓存
	if err := svc.loadFromDB(); err != nil {
		_ = db.Close() // 忽略关闭错误，返回主要错误
		return nil, fmt.Errorf("加载数据失败: %w", err)
	}

	slog.Info("人格服务初始化成功", "db_path", dbPath, "personas", len(svc.personas))
	return svc, nil
}

// initSchema 创建数据库表结构。
func initSchema(db *sql.DB) error {
	// 人格表
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS personas (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			prompt TEXT NOT NULL,
			description TEXT NOT NULL,
			is_builtin INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 personas 表失败: %w", err)
	}

	// 房间人格映射表
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS room_personas (
			room_id TEXT NOT NULL PRIMARY KEY,
			persona_id TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (persona_id) REFERENCES personas(id) ON DELETE CASCADE
		)
	`)
	if err != nil {
		return fmt.Errorf("创建 room_personas 表失败: %w", err)
	}

	return nil
}

// loadBuiltinPersonas 将内置人格插入数据库（如果不存在）。
func (s *Service) loadBuiltinPersonas() error {
	now := time.Now()
	for _, p := range BuiltinPersonas {
		// 检查是否已存在
		var exists bool
		err := s.db.QueryRow("SELECT 1 FROM personas WHERE id = ?", p.ID).Scan(&exists)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("检查人格 %s 失败: %w", p.ID, err)
		}
		if exists {
			continue
		}

		// 插入新的人格
		_, err = s.db.Exec(
			"INSERT INTO personas (id, name, prompt, description, is_builtin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
			p.ID, p.Name, p.Prompt, p.Description, boolToInt(p.IsBuiltin), now.Unix(), now.Unix(),
		)
		if err != nil {
			return fmt.Errorf("插入内置人格 %s 失败: %w", p.ID, err)
		}
		slog.Debug("插入内置人格", "id", p.ID, "name", p.Name)
	}
	return nil
}

// loadFromDB 从数据库加载所有数据到缓存。
func (s *Service) loadFromDB() error {
	// 加载人格
	rows, err := s.db.Query("SELECT id, name, prompt, description, is_builtin, created_at, updated_at FROM personas")
	if err != nil {
		return fmt.Errorf("查询人格失败: %w", err)
	}
	defer func() { _ = rows.Close() }() // 忽略关闭错误，主要错误已处理

	for rows.Next() {
		p := &Persona{}
		var isBuiltin int
		var createdAt, updatedAt int64
		err := rows.Scan(&p.ID, &p.Name, &p.Prompt, &p.Description, &isBuiltin, &createdAt, &updatedAt)
		if err != nil {
			return fmt.Errorf("扫描人格数据失败: %w", err)
		}
		p.IsBuiltin = isBuiltin == 1
		p.CreatedAt = time.Unix(createdAt, 0)
		p.UpdatedAt = time.Unix(updatedAt, 0)
		s.personas[p.ID] = p
	}

	// 加载房间人格映射
	roomRows, err := s.db.Query("SELECT room_id, persona_id FROM room_personas")
	if err != nil {
		return fmt.Errorf("查询房间人格映射失败: %w", err)
	}
	defer func() { _ = roomRows.Close() }() // 忽略关闭错误，主要错误已处理

	for roomRows.Next() {
		var roomID, personaID string
		err := roomRows.Scan(&roomID, &personaID)
		if err != nil {
			return fmt.Errorf("扫描房间人格映射失败: %w", err)
		}
		s.roomPersonas[id.RoomID(roomID)] = personaID
	}

	return nil
}

// Close 关闭数据库连接。
func (s *Service) Close() error {
	return s.db.Close()
}

// List 返回所有可用人格列表。
func (s *Service) List() []*Persona {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Persona, 0, len(s.personas))
	for _, p := range s.personas {
		result = append(result, p)
	}
	return result
}

// Get 根据 ID 获取人格。如果不存在返回 nil。
func (s *Service) Get(id string) *Persona {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.personas[id]
}

// Create 创建新的自定义人格。
// 如果 ID 已存在或与内置人格冲突，返回错误。
func (s *Service) Create(id, name, prompt, description string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 检查是否与内置人格 ID 冲突
	if IsValidBuiltinID(id) {
		return fmt.Errorf("人格 ID %q 与内置人格冲突", id)
	}

	// 检查是否已存在
	if _, exists := s.personas[id]; exists {
		return fmt.Errorf("人格 ID %q 已存在", id)
	}

	now := time.Now()
	_, err := s.db.Exec(
		"INSERT INTO personas (id, name, prompt, description, is_builtin, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)",
		id, name, prompt, description, now.Unix(), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("插入人格失败: %w", err)
	}

	s.personas[id] = &Persona{
		ID:          id,
		Name:        name,
		Prompt:      prompt,
		Description: description,
		IsBuiltin:   false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	slog.Info("创建新人格", "id", id, "name", name)
	return nil
}

// Delete 删除指定 ID 的人格。
// 内置人格不可删除。如果人格不存在返回 nil。
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, exists := s.personas[id]
	if !exists {
		return nil
	}

	if p.IsBuiltin {
		return fmt.Errorf("内置人格 %q 不可删除", id)
	}

	// 删除数据库记录
	_, err := s.db.Exec("DELETE FROM personas WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("删除人格失败: %w", err)
	}

	// 清除使用此人格的房间映射
	for roomID, personaID := range s.roomPersonas {
		if personaID == id {
			delete(s.roomPersonas, roomID)
		}
	}

	delete(s.personas, id)
	slog.Info("删除人格", "id", id)
	return nil
}

// GetRoomPersona 获取指定房间的人格。
// 如果房间未设置人格，返回 nil。
func (s *Service) GetRoomPersona(roomID id.RoomID) *Persona {
	s.mu.RLock()
	defer s.mu.RUnlock()

	personaID, exists := s.roomPersonas[roomID]
	if !exists {
		return nil
	}
	return s.personas[personaID]
}

// SetRoomPersona 设置指定房间的人格。
// 如果人格 ID 不存在，返回错误。personaID 为空则清除房间人格。
func (s *Service) SetRoomPersona(ctx context.Context, roomID id.RoomID, personaID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 空人格 ID 表示清除
	if personaID == "" {
		return s.clearRoomPersonaLocked(roomID)
	}

	// 检查人格是否存在
	if _, exists := s.personas[personaID]; !exists {
		return fmt.Errorf("人格 %q 不存在", personaID)
	}

	now := time.Now()
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO room_personas (room_id, persona_id, updated_at) VALUES (?, ?, ?)",
		string(roomID), personaID, now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("设置房间人格失败: %w", err)
	}

	s.roomPersonas[roomID] = personaID
	slog.Info("设置房间人格", "room_id", roomID, "persona_id", personaID)
	return nil
}

// ClearRoomPersona 清除指定房间的人格设置。
func (s *Service) ClearRoomPersona(ctx context.Context, roomID id.RoomID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearRoomPersonaLocked(roomID)
}

// clearRoomPersonaLocked 清除房间人格（调用者已持有锁）。
func (s *Service) clearRoomPersonaLocked(roomID id.RoomID) error {
	_, err := s.db.Exec("DELETE FROM room_personas WHERE room_id = ?", string(roomID))
	if err != nil {
		return fmt.Errorf("清除房间人格失败: %w", err)
	}

	delete(s.roomPersonas, roomID)
	slog.Info("清除房间人格", "room_id", roomID)
	return nil
}

// GetSystemPrompt 获取指定房间的系统提示词。
// 将基础提示词与房间人格提示词合并。
func (s *Service) GetSystemPrompt(roomID id.RoomID, basePrompt string) string {
	persona := s.GetRoomPersona(roomID)
	if persona == nil {
		return basePrompt
	}
	return persona.FullPrompt(basePrompt)
}

// boolToInt 将布尔值转换为整数（0 或 1）。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
