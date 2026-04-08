// Package db 提供 SQLite 数据库驱动注册。
// 使用 modernc.org/sqlite 纯 Go 实现。
package db

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	moderncDriver driver.Driver
	driverOnce    sync.Once
	driverErr     error
)

func init() {
	// 注册 modernc sqlite 驱动为 sqlite3-fk-wal
	// 这兼容 mautrix 的 dbutil 包
	// 检查驱动是否已注册，避免测试环境中重复注册导致 panic
	registerDriverOnce("sqlite3-fk-wal", &pragmaDriver{})
}

// registerDriverOnce 安全地注册 SQL 驱动，避免重复注册导致 panic。
func registerDriverOnce(name string, driver driver.Driver) {
	// 尝试打开一个空 DSN 来检查驱动是否已注册
	// 如果驱动已存在，sql.Open 会成功（不会真正连接）
	db, err := sql.Open(name, ":memory:")
	if err == nil {
		_ = db.Close() // 忽略关闭错误，驱动已注册
		return         // 驱动已注册
	}

	// 如果错误是"驱动不存在"，则注册驱动
	sql.Register(name, driver)
}

// pragmaDriver 包装 modernc.org/sqlite 驱动，
// 自动在 DSN 中添加必要的 PRAGMA 设置。
type pragmaDriver struct{}

// Open 实现 driver.Driver 接口。
func (d *pragmaDriver) Open(name string) (driver.Conn, error) {
	// 惰性获取底层驱动
	underlyingDriver, err := getModerncDriver()
	if err != nil {
		return nil, fmt.Errorf("sqlite driver not available: %w", err)
	}

	// 修改 DSN 添加 PRAGMA
	dsn := addPragmas(name)

	// 使用底层驱动打开连接
	return underlyingDriver.Open(dsn)
}

// getModerncDriver 惰性获取 modernc.org/sqlite 驱动实例。
func getModerncDriver() (driver.Driver, error) {
	driverOnce.Do(func() {
		// 打开一个临时连接来获取驱动实例
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			driverErr = fmt.Errorf("failed to open sqlite driver: %w", err)
			slog.Error("SQLite 驱动初始化失败", "error", err)
			return
		}
		defer func() {
			_ = db.Close() // 忽略关闭错误，驱动已获取
		}()

		moderncDriver = db.Driver()
		slog.Debug("SQLite 驱动初始化成功")
	})

	return moderncDriver, driverErr
}

// addPragmas 向 DSN 添加必要的 PRAGMA 参数。
// modernc.org/sqlite 使用 _pragma 参数设置 PRAGMA。
func addPragmas(dsn string) string {
	// 这些 PRAGMA 与 mattn/go-sqlite3 的 sqlite3-fk-wal 驱动保持一致
	pragmas := []string{
		"_pragma=foreign_keys(ON)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
	}

	// 检查是否已有参数
	if strings.Contains(dsn, "?") {
		return dsn + "&" + strings.Join(pragmas, "&")
	}
	return dsn + "?" + strings.Join(pragmas, "&")
}
