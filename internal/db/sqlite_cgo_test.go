//go:build cgo

// Package db_test 包含 SQLite CGO 驱动的单元测试。
package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCGODriver_Open 测试 CGO 驱动的 Open 方法。
func TestCGODriver_Open(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cgo.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

	// 验证连接有效
	if err := db.Ping(); err != nil {
		t.Fatalf("failed to ping database: %v", err)
	}
}

// TestCGODriver_ForeignKeysEnabled 测试外键约束是否启用。
func TestCGODriver_ForeignKeysEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_fk_cgo.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("failed to query foreign_keys pragma: %v", err)
	}

	if fkEnabled != 1 {
		t.Errorf("expected foreign_keys to be enabled (1), got %d", fkEnabled)
	}
}

// TestCGODriver_JournalMode 测试日志模式是否为 WAL。
func TestCGODriver_JournalMode(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_journal_cgo.db")

	db, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

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

// TestCGODriver_InMemory 测试内存数据库。
func TestCGODriver_InMemory(t *testing.T) {
	db, err := sql.Open("sqlite3-fk-wal", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

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

// TestCGODriver_ForeignKeyConstraint 测试外键约束实际生效。
func TestCGODriver_ForeignKeyConstraint(t *testing.T) {
	db, err := sql.Open("sqlite3-fk-wal", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Logf("failed to close database: %v", err)
		}
	}()

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

// TestCGODriver_FileBased 测试文件数据库。
func TestCGODriver_FileBased(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "file_test_cgo.db")

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
}

// TestCGODriver_ConcurrentAccess 测试并发访问。
func TestCGODriver_ConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "concurrent_cgo.db")

	// 打开多个连接
	db1, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database 1: %v", err)
	}
	defer func() {
		if err := db1.Close(); err != nil {
			t.Logf("failed to close database 1: %v", err)
		}
	}()

	db2, err := sql.Open("sqlite3-fk-wal", dbPath)
	if err != nil {
		t.Fatalf("failed to open database 2: %v", err)
	}
	defer func() {
		if err := db2.Close(); err != nil {
			t.Logf("failed to close database 2: %v", err)
		}
	}()

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
