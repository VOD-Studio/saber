package memory

import (
	"database/sql"
	"errors"
	"os"
	"time"

	_ "rua.plus/saber/internal/db" // 注册 sqlite3-fk-wal 驱动
)

// storeSchema 建立记忆条目、待确认建议两类表；一个空间内的内容保持唯一。
const storeSchema = `
CREATE TABLE IF NOT EXISTS memory_entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scope_key TEXT NOT NULL,
	content TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 1,
	explicit INTEGER NOT NULL DEFAULT 0,
	creator TEXT NOT NULL DEFAULT '',
	source_task INTEGER NOT NULL DEFAULT 0,
	source_message TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(scope_key, content)
);
CREATE INDEX IF NOT EXISTS memory_entries_scope ON memory_entries(scope_key, explicit, updated_at, id);
CREATE TABLE IF NOT EXISTS memory_changes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scope_key TEXT NOT NULL,
	action TEXT NOT NULL,
	entry_id INTEGER NOT NULL DEFAULT 0,
	expected_version INTEGER NOT NULL DEFAULT 0,
	content TEXT NOT NULL DEFAULT '',
	proposer TEXT NOT NULL DEFAULT '',
	source_task INTEGER NOT NULL DEFAULT 0,
	source_message TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending',
	created_at INTEGER NOT NULL,
	decided_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS memory_changes_pending ON memory_changes(scope_key, status, id);
`

// store 独占 memory.db 的表结构与连接。
type store struct{ db *sql.DB }

// openStore 以私有权限创建数据库文件，并初始化表结构。
func openStore(path string) (*store, error) {
	// 先以私有权限创建文件，SQLite 的 WAL/SHM 会继承数据库权限。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3-fk-wal", path)
	if err != nil {
		return nil, err
	}
	// 单连接串行化写入，避免容量判断与提交之间出现竞态。
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(storeSchema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

const entryColumns = `id,content,version,explicit,creator,source_task,source_message,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func scanEntry(row scanner) (Entry, error) {
	var e Entry
	var explicit int
	var created, updated int64
	if err := row.Scan(&e.ID, &e.Content, &e.Version, &explicit, &e.Creator, &e.SourceTask, &e.SourceMessage, &created, &updated); err != nil {
		return Entry{}, err
	}
	e.Explicit = explicit != 0
	e.CreatedAt = time.UnixMilli(created)
	e.UpdatedAt = time.UnixMilli(updated)
	return e, nil
}

const changeColumns = `id,action,entry_id,expected_version,content,proposer,source_task,source_message,status,created_at,decided_at`

func scanChange(row scanner) (Change, error) {
	var c Change
	var created, decided int64
	if err := row.Scan(&c.ID, &c.Action, &c.EntryID, &c.ExpectedVersion, &c.Content, &c.Proposer, &c.SourceTask, &c.SourceMessage, &c.Status, &created, &decided); err != nil {
		return Change{}, err
	}
	c.CreatedAt = time.UnixMilli(created)
	if decided != 0 {
		c.DecidedAt = time.UnixMilli(decided)
	}
	return c, nil
}
