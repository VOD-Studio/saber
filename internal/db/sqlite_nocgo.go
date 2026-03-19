//go:build !cgo

// Package db 提供 SQLite 数据库驱动注册。
// 本文件是非 CGO 构建，使用 modernc.org/sqlite 纯 Go 实现。
package db

import (
	"database/sql"
	"database/sql/driver"
	"strings"

	_ "modernc.org/sqlite"
)

func init() {
	// 注册 modernc sqlite 驱动为 sqlite3-fk-wal
	// 这兼容 mautrix 的 dbutil 包
	sql.Register("sqlite3-fk-wal", &pragmaDriver{})
}

// pragmaDriver 包装 modernc.org/sqlite 驱动，
// 自动在 DSN 中添加必要的 PRAGMA 设置。
type pragmaDriver struct{}

// Open 实现 driver.Driver 接口。
func (d *pragmaDriver) Open(name string) (driver.Conn, error) {
	// 获取 modernc sqlite 驱动
	underlyingDriver := getModerncDriver()

	// 修改 DSN 添加 PRAGMA
	dsn := addPragmas(name)

	// 使用底层驱动打开连接
	return underlyingDriver.Open(dsn)
}

// getModerncDriver 返回 modernc.org/sqlite 的驱动实例。
func getModerncDriver() driver.Driver {
	// 打开一个临时连接来获取驱动实例
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic("db: failed to get modernc sqlite driver: " + err.Error())
	}
	defer db.Close()

	return db.Driver()
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
