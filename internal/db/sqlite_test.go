// Package db_test 包含 SQLite 驱动的单元测试。
package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPragmaDriver_Open 测试 pragma 驱动的 Open 方法。
func TestPragmaDriver_Open(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// 验证连接有效
	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}
}

// TestPragmaDriver_ForeignKeysEnabled 测试外键约束是否启用。
func TestPragmaDriver_ForeignKeysEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_fk.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("failed to query foreign_keys pragma: %v", err)
	}

	if fkEnabled != 1 {
		t.Errorf("expected foreign_keys to be enabled (1), got %d", fkEnabled)
	}
}

// TestPragmaDriver_JournalMode 测试日志模式是否为 WAL。
func TestPragmaDriver_JournalMode(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_journal.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatalf("failed to query journal_mode pragma: %v", err)
	}

	// journal_mode 返回值可能是小写或大写
	if strings.ToLower(journalMode) != "wal" {
		t.Errorf("expected journal_mode to be wal, got %s", journalMode)
	}
}

// TestPragmaDriver_Synchronous 测试同步模式设置。
func TestPragmaDriver_Synchronous(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_sync.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	var synchronous int
	err = db.QueryRow("PRAGMA synchronous").Scan(&synchronous)
	if err != nil {
		t.Fatalf("failed to query synchronous pragma: %v", err)
	}

	// NORMAL = 1
	if synchronous != 1 {
		t.Errorf("expected synchronous to be 1 (NORMAL), got %d", synchronous)
	}
}

// TestPragmaDriver_BusyTimeout 测试忙碌超时设置。
func TestPragmaDriver_BusyTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_busy.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	var busyTimeout int
	err = db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout)
	if err != nil {
		t.Fatalf("failed to query busy_timeout pragma: %v", err)
	}

	// 期望 5000ms
	if busyTimeout != 5000 {
		t.Errorf("expected busy_timeout to be 5000, got %d", busyTimeout)
	}
}

// TestAddPragmas_SimplePath 测试简单路径添加 PRAGMA。
func TestAddPragmas_SimplePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantHas []string
	}{
		{
			name:    "simple_path",
			input:   "/path/to/db.sqlite",
			wantHas: []string{"_pragma=foreign_keys(ON)", "_pragma=journal_mode(WAL)"},
		},
		{
			name:    "memory_db",
			input:   ":memory:",
			wantHas: []string{"_pragma=foreign_keys(ON)"},
		},
		{
			name:    "with_existing_params",
			input:   "/path/db.sqlite?mode=rwc",
			wantHas: []string{"mode=rwc", "_pragma=foreign_keys(ON)"},
		},
		{
			name:    "multiple_existing_params",
			input:   "/path/db.sqlite?mode=rwc&cache=shared",
			wantHas: []string{"mode=rwc", "cache=shared", "_pragma=foreign_keys(ON)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := addPragmas(tt.input)

			for _, want := range tt.wantHas {
				if !strings.Contains(result, want) {
					t.Errorf("result missing %q: got %s", want, result)
				}
			}
		})
	}
}

// TestPragmaDriver_InMemory 测试内存数据库。
func TestPragmaDriver_InMemory(t *testing.T) {
	db, err := sql.Open("sqlite3-fk-wal", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// 创建测试表
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// 插入数据
	_, err = db.Exec("INSERT INTO test (name) VALUES (?)", "test_name")
	if err != nil {
		t.Fatalf("failed to insert data: %v", err)
	}

	// 查询数据
	var name string
	err = db.QueryRow("SELECT name FROM test WHERE id = 1").Scan(&name)
	if err != nil {
		t.Fatalf("failed to query data: %v", err)
	}

	if name != "test_name" {
		t.Errorf("expected name 'test_name', got %q", name)
	}
}

// TestPragmaDriver_ForeignKeyConstraint 测试外键约束实际生效。
func TestPragmaDriver_ForeignKeyConstraint(t *testing.T) {
	db, err := sql.Open("sqlite3-fk-wal", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	// 创建父表
	_, err = db.Exec("CREATE TABLE parent (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("failed to create parent table: %v", err)
	}

	// 创建子表，带外键约束
	_, err = db.Exec(`
		CREATE TABLE child (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER REFERENCES parent(id)
		)
	`)
	if err != nil {
		t.Fatalf("failed to create child table: %v", err)
	}

	// 尝试插入无效的外键（应该失败）
	_, err = db.Exec("INSERT INTO child (parent_id) VALUES (999)")
	if err == nil {
		t.Error("expected foreign key constraint violation, but insert succeeded")
	}
}

// TestGetModerncDriver 测试获取 modernc 驱动。
func TestGetModerncDriver(t *testing.T) {
	driver, err := getModerncDriver()
	if err != nil {
		t.Fatalf("getModerncDriver failed: %v", err)
	}

	if driver == nil {
		t.Error("driver is nil")
	}
}

// TestPragmaDriver_FileBased 测试文件数据库。
func TestPragmaDriver_FileBased(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "file_test.db")

	// 打开数据库
	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// 创建表
	_, err = db.Exec("CREATE TABLE test (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// 关闭数据库
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	// 验证文件存在
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}

	// 验证 WAL 文件存在（在使用 WAL 模式后）
	// 注意：WAL 文件在连接关闭后可能不存在，取决于 checkpoint
}

// TestPragmaDriver_ConcurrentAccess 测试并发访问。
func TestPragmaDriver_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "concurrent.db")

	// 打开多个连接
	db1, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database 1: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db1.Close())
	})

	db2, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database 2: %v", err)
	}
	t.Cleanup(func() {
		require.NoError(t, db2.Close())
	})

	// 在第一个连接创建表
	_, err = db1.Exec("CREATE TABLE concurrent_test (id INTEGER PRIMARY KEY, value TEXT)")
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// 在第二个连接插入数据
	_, err = db2.Exec("INSERT INTO concurrent_test (value) VALUES ('test')")
	if err != nil {
		t.Fatalf("failed to insert from second connection: %v", err)
	}

	// 在第一个连接读取
	var value string
	err = db1.QueryRow("SELECT value FROM concurrent_test WHERE id = 1").Scan(&value)
	if err != nil {
		t.Fatalf("failed to query from first connection: %v", err)
	}

	if value != "test" {
		t.Errorf("expected value 'test', got %q", value)
	}
}
