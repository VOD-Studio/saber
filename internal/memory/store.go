package memory

import (
	"database/sql"
	"errors"
	"os"
	"time"

	"rua.plus/saber/internal/db" // 注册 saber-sqlite 驱动
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
CREATE TABLE IF NOT EXISTS history_projections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id INTEGER UNIQUE NOT NULL,
	scope_key TEXT NOT NULL,
	scope_kind TEXT NOT NULL,
	platform TEXT NOT NULL,
	account TEXT NOT NULL,
	conversation TEXT NOT NULL,
	thread TEXT NOT NULL DEFAULT '',
	sender_id TEXT NOT NULL,
	user_message_id TEXT NOT NULL,
	user_text TEXT NOT NULL,
	assistant_text TEXT NOT NULL,
	task_status TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS history_projections_scope ON history_projections(scope_key, id DESC);
CREATE INDEX IF NOT EXISTS history_projections_task ON history_projections(task_id);

CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(
	user_text,
	assistant_text,
	content='history_projections',
	content_rowid='id',
	tokenize="trigram"
);

CREATE TRIGGER IF NOT EXISTS history_projections_ai AFTER INSERT ON history_projections BEGIN
	INSERT INTO history_fts(rowid, user_text, assistant_text) VALUES (new.id, new.user_text, new.assistant_text);
END;
CREATE TRIGGER IF NOT EXISTS history_projections_ad AFTER DELETE ON history_projections BEGIN
	INSERT INTO history_fts(history_fts, rowid, user_text, assistant_text) VALUES('delete', old.id, old.user_text, old.assistant_text);
END;
CREATE TRIGGER IF NOT EXISTS history_projections_au AFTER UPDATE ON history_projections BEGIN
	INSERT INTO history_fts(history_fts, rowid, user_text, assistant_text) VALUES('delete', old.id, old.user_text, old.assistant_text);
	INSERT INTO history_fts(rowid, user_text, assistant_text) VALUES (new.id, new.user_text, new.assistant_text);
END;

CREATE TABLE IF NOT EXISTS memory_scope_settings (
	scope_key TEXT PRIMARY KEY,
	paused INTEGER NOT NULL DEFAULT 0,
	review_count INTEGER NOT NULL DEFAULT 0,
	token_count INTEGER NOT NULL DEFAULT 0,
	last_reviewed_task_id INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_excluded_sources (
	scope_key TEXT NOT NULL,
	task_id INTEGER NOT NULL,
	reason TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	PRIMARY KEY(scope_key, task_id)
);

CREATE TABLE IF NOT EXISTS memory_reviewed_tasks (
	scope_key TEXT NOT NULL,
	task_id INTEGER NOT NULL,
	reviewed_at INTEGER NOT NULL,
	PRIMARY KEY(scope_key, task_id)
);
CREATE TABLE IF NOT EXISTS skill_entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scope_key TEXT NOT NULL,
	name TEXT NOT NULL,
	description TEXT NOT NULL,
	content TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 1,
	creator TEXT NOT NULL DEFAULT '',
	source_task INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(scope_key, name)
);
CREATE INDEX IF NOT EXISTS skill_entries_scope ON skill_entries(scope_key, updated_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS skill_changes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	scope_key TEXT NOT NULL,
	action TEXT NOT NULL,
	skill_id INTEGER NOT NULL DEFAULT 0,
	expected_version INTEGER NOT NULL DEFAULT 0,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	content TEXT NOT NULL DEFAULT '',
	proposer TEXT NOT NULL DEFAULT '',
	source_task INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at INTEGER NOT NULL,
	decided_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS skill_changes_pending ON skill_changes(scope_key, status, id);
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
	db, err := sql.Open(db.DriverName, path)
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

const historyColumns = `id,task_id,scope_kind,platform,account,conversation,thread,sender_id,user_message_id,user_text,assistant_text,task_status,created_at`
const qualifiedHistoryColumns = `p.id,p.task_id,p.scope_kind,p.platform,p.account,p.conversation,p.thread,p.sender_id,p.user_message_id,p.user_text,p.assistant_text,p.task_status,p.created_at`

func scanHistory(row scanner) (HistoryRecord, error) {
	var h HistoryRecord
	var created int64
	var kind, platform, account, conversation string
	if err := row.Scan(&h.ID, &h.TaskID, &kind, &platform, &account, &conversation, &h.Thread, &h.SenderID, &h.UserMessageID, &h.UserText, &h.AssistantText, &h.TaskStatus, &created); err != nil {
		return HistoryRecord{}, err
	}
	id := conversation
	if kind == ScopeUser {
		id = h.SenderID
	}
	h.Scope = Scope{Kind: kind, Platform: platform, Account: account, ID: id}
	h.Platform = platform
	h.Account = account
	h.Conversation = conversation
	h.CreatedAt = time.UnixMilli(created)
	return h, nil
}

const skillColumns = `id,name,description,content,version,creator,source_task,created_at,updated_at`

func scanSkill(row scanner, scope Scope) (SkillEntry, error) {
	var s SkillEntry
	var created, updated int64
	if err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Content, &s.Version, &s.Creator, &s.SourceTask, &created, &updated); err != nil {
		return SkillEntry{}, err
	}
	s.Scope = scope
	s.CreatedAt = time.UnixMilli(created)
	s.UpdatedAt = time.UnixMilli(updated)
	return s, nil
}

const skillChangeColumns = `id,action,skill_id,expected_version,name,description,content,proposer,source_task,status,created_at,decided_at`

func scanSkillChange(row scanner, scope Scope) (SkillChange, error) {
	var c SkillChange
	var created, decided int64
	if err := row.Scan(&c.ID, &c.Action, &c.SkillID, &c.ExpectedVersion, &c.Name, &c.Description, &c.Content, &c.Proposer, &c.SourceTask, &c.Status, &created, &decided); err != nil {
		return SkillChange{}, err
	}
	c.Scope = scope
	c.CreatedAt = time.UnixMilli(created)
	if decided != 0 {
		c.DecidedAt = time.UnixMilli(decided)
	}
	return c, nil
}
