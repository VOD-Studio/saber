package persona

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"
	_ "rua.plus/saber/internal/db" // 注册 SQLite 驱动
)

// newTestService 创建一个使用临时数据库的测试服务。
func newTestService(t *testing.T) *Service {
	t.Helper()

	// 创建临时目录
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	svc, err := NewService(dbPath)
	if err != nil {
		t.Fatalf("创建测试服务失败: %v", err)
	}

	t.Cleanup(func() {
		_ = svc.Close()
	})

	return svc
}

func TestNewService(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	svc, err := NewService(dbPath)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	defer func() { _ = svc.Close() }()

	// 验证数据库文件已创建
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("数据库文件未创建")
	}

	// 验证内置人格已加载
	if len(svc.personas) < 5 {
		t.Errorf("期望至少 5 个内置人格，实际 %d 个", len(svc.personas))
	}
}

func TestServiceList(t *testing.T) {
	svc := newTestService(t)

	list := svc.List()
	if len(list) < 5 {
		t.Errorf("List() 返回 %d 个人格，期望至少 5 个", len(list))
	}

	// 验证列表中的每个元素都不为空
	for _, p := range list {
		if p.IsEmpty() {
			t.Error("List() 返回空人格")
		}
	}
}

func TestServiceGet(t *testing.T) {
	svc := newTestService(t)

	tests := []struct {
		id       string
		expected bool
	}{
		{"catgirl", true},
		{"butler", true},
		{"nonexistent", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			p := svc.Get(tt.id)
			if (p != nil) != tt.expected {
				t.Errorf("Get(%q) = %v, want exists: %v", tt.id, p, tt.expected)
			}
		})
	}
}

func TestServiceCreate(t *testing.T) {
	svc := newTestService(t)

	// 测试创建新人格
	err := svc.Create("custom", "自定义", "自定义提示词", "自定义描述")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 验证可以获取到新创建的人格
	p := svc.Get("custom")
	if p == nil {
		t.Fatal("创建的人格未找到")
	}
	if p.Name != "自定义" {
		t.Errorf("Name = %q, want %q", p.Name, "自定义")
	}
	if p.IsBuiltin {
		t.Error("自定义人格不应标记为内置")
	}

	// 测试创建已存在的人格
	err = svc.Create("custom", "另一个", "提示", "描述")
	if err == nil {
		t.Error("创建已存在的人格应该返回错误")
	}

	// 测试创建与内置人格冲突的 ID
	err = svc.Create("catgirl", "猫娘2", "提示", "描述")
	if err == nil {
		t.Error("创建与内置人格冲突的 ID 应该返回错误")
	}
}

func TestServiceDelete(t *testing.T) {
	svc := newTestService(t)

	// 先创建一个自定义人格
	err := svc.Create("to-delete", "待删除", "提示", "描述")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 删除自定义人格
	err = svc.Delete("to-delete")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// 验证已删除
	if svc.Get("to-delete") != nil {
		t.Error("人格未被删除")
	}

	// 测试删除不存在的内存格（应返回 nil）
	err = svc.Delete("nonexistent")
	if err != nil {
		t.Errorf("删除不存在的人格应返回 nil, got error: %v", err)
	}

	// 测试删除内置人格
	err = svc.Delete("catgirl")
	if err == nil {
		t.Error("删除内置人格应该返回错误")
	}
}

func TestServiceRoomPersona(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	roomID := id.RoomID("!test:example.com")

	// 初始状态应无人格
	p := svc.GetRoomPersona(roomID)
	if p != nil {
		t.Error("新房间不应有人格")
	}

	// 设置房间人格
	err := svc.SetRoomPersona(ctx, roomID, "catgirl")
	if err != nil {
		t.Fatalf("SetRoomPersona() error = %v", err)
	}

	// 验证设置成功
	p = svc.GetRoomPersona(roomID)
	if p == nil {
		t.Fatal("房间人格未设置")
	}
	if p.ID != "catgirl" {
		t.Errorf("人格 ID = %q, want %q", p.ID, "catgirl")
	}

	// 清除房间人格
	err = svc.ClearRoomPersona(ctx, roomID)
	if err != nil {
		t.Fatalf("ClearRoomPersona() error = %v", err)
	}

	// 验证清除成功
	p = svc.GetRoomPersona(roomID)
	if p != nil {
		t.Error("房间人格未清除")
	}
}

func TestServiceSetRoomPersonaEmpty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	roomID := id.RoomID("!test:example.com")

	// 先设置人格
	err := svc.SetRoomPersona(ctx, roomID, "catgirl")
	if err != nil {
		t.Fatalf("SetRoomPersona() error = %v", err)
	}

	// 使用空 ID 清除
	err = svc.SetRoomPersona(ctx, roomID, "")
	if err != nil {
		t.Fatalf("SetRoomPersona with empty ID error = %v", err)
	}

	if svc.GetRoomPersona(roomID) != nil {
		t.Error("空 ID 应清除房间人格")
	}
}

func TestServiceSetRoomPersonaInvalid(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	roomID := id.RoomID("!test:example.com")

	err := svc.SetRoomPersona(ctx, roomID, "nonexistent")
	if err == nil {
		t.Error("设置不存在的人格应该返回错误")
	}
}

func TestServiceGetSystemPrompt(t *testing.T) {
	svc := newTestService(t)
	roomID := id.RoomID("!test:example.com")
	ctx := context.Background()

	tests := []struct {
		name       string
		basePrompt string
		personaID  string
		wantEmpty  bool
	}{
		{
			name:       "无人格，空基础提示词",
			basePrompt: "",
			personaID:  "",
			wantEmpty:  true,
		},
		{
			name:       "无人格，有基础提示词",
			basePrompt: "基础提示",
			personaID:  "",
			wantEmpty:  false,
		},
		{
			name:       "有人格，空基础提示词",
			basePrompt: "",
			personaID:  "catgirl",
			wantEmpty:  false,
		},
		{
			name:       "有人格，有基础提示词",
			basePrompt: "基础提示",
			personaID:  "butler",
			wantEmpty:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 清除之前的人格设置
			_ = svc.ClearRoomPersona(ctx, roomID)

			if tt.personaID != "" {
				err := svc.SetRoomPersona(ctx, roomID, tt.personaID)
				if err != nil {
					t.Fatalf("SetRoomPersona() error = %v", err)
				}
			}

			result := svc.GetSystemPrompt(roomID, tt.basePrompt)

			if tt.wantEmpty && result != "" {
				t.Errorf("期望空提示词，实际: %q", result)
			}
			if !tt.wantEmpty && result == "" {
				t.Error("期望非空提示词，实际为空")
			}

			// 验证合并逻辑
			if tt.basePrompt != "" && tt.personaID != "" {
				if result == tt.basePrompt {
					t.Error("应该包含人格提示词")
				}
			}
		})
	}
}

func TestServicePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "persist.db")

	// 创建第一个服务实例
	svc1, err := NewService(dbPath)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	// 创建自定义人格
	err = svc1.Create("persist-test", "持久化测试", "测试提示词", "测试描述")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 设置房间人格
	ctx := context.Background()
	roomID := id.RoomID("!persist:example.com")
	err = svc1.SetRoomPersona(ctx, roomID, "persist-test")
	if err != nil {
		t.Fatalf("SetRoomPersona() error = %v", err)
	}

	// 关闭服务
	_ = svc1.Close()

	// 创建第二个服务实例（从同一数据库加载）
	svc2, err := NewService(dbPath)
	if err != nil {
		t.Fatalf("NewService() second instance error = %v", err)
	}
	defer func() { _ = svc2.Close() }()

	// 验证自定义人格被保留
	p := svc2.Get("persist-test")
	if p == nil {
		t.Fatal("自定义人格未被持久化")
	}
	if p.Name != "持久化测试" {
		t.Errorf("Name = %q, want %q", p.Name, "持久化测试")
	}

	// 验证房间人格被保留
	p = svc2.GetRoomPersona(roomID)
	if p == nil {
		t.Fatal("房间人格未被持久化")
	}
	if p.ID != "persist-test" {
		t.Errorf("人格 ID = %q, want %q", p.ID, "persist-test")
	}
}

func TestServiceDeleteRoomPersonaCleanup(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	roomID := id.RoomID("!test:example.com")

	// 创建自定义人格
	err := svc.Create("to-delete-room", "待删除", "提示", "描述")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// 设置房间人格
	err = svc.SetRoomPersona(ctx, roomID, "to-delete-room")
	if err != nil {
		t.Fatalf("SetRoomPersona() error = %v", err)
	}

	// 删除人格
	err = svc.Delete("to-delete-room")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// 验证房间人格也被清除
	p := svc.GetRoomPersona(roomID)
	if p != nil {
		t.Error("删除人格后房间人格应被清除")
	}
}

func TestServiceConcurrentAccess(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// 并发读写测试
	done := make(chan bool)

	// 并发读取
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				svc.List()
				svc.Get("catgirl")
			}
			done <- true
		}()
	}

	// 并发写入
	for i := 0; i < 5; i++ {
		go func(i int) {
			roomID := id.RoomID(fmt.Sprintf("!room%d:example.com", i))
			_ = svc.SetRoomPersona(ctx, roomID, "catgirl")
			svc.GetRoomPersona(roomID)
			_ = svc.ClearRoomPersona(ctx, roomID)
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 15; i++ {
		<-done
	}
}

func TestServiceTimestamps(t *testing.T) {
	svc := newTestService(t)

	beforeCreate := time.Now()
	err := svc.Create("timestamp-test", "时间戳测试", "提示", "描述")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	afterCreate := time.Now()

	p := svc.Get("timestamp-test")
	if p == nil {
		t.Fatal("人格未找到")
	}

	// 验证时间戳在合理范围内
	if p.CreatedAt.Before(beforeCreate) || p.CreatedAt.After(afterCreate) {
		t.Errorf("CreatedAt = %v, 应在 [%v, %v] 范围内", p.CreatedAt, beforeCreate, afterCreate)
	}
	if p.UpdatedAt.Before(beforeCreate) || p.UpdatedAt.After(afterCreate) {
		t.Errorf("UpdatedAt = %v, 应在 [%v, %v] 范围内", p.UpdatedAt, beforeCreate, afterCreate)
	}
}
