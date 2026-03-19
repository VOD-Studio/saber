//go:build cgo

// Package db 提供 SQLite 数据库驱动注册。
// 本文件是 CGO 构建，导入 mautrix 的 dbutil/litestream 驱动。
package db

import (
	// 导入 dbutil/litestream 以注册 sqlite3-fk-wal 驱动
	// 这个驱动使用 mattn/go-sqlite3 (CGO)
	_ "go.mau.fi/util/dbutil/litestream"
)
