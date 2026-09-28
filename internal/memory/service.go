package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// Config 是记忆服务的可选参数。
type Config struct {
	// MaxChars 限制每个空间的存储字符数；零值使用 DefaultMaxChars。
	MaxChars int
	// InjectBytes 限制单次注入字节数；零值使用 DefaultInjectBytes。
	InjectBytes int
	// GroupWriter 判定群空间直接写入权限，由命令授权配置提供。
	// 为 nil 时群空间的写入请求一律形成待确认建议。
	GroupWriter func(chat.Identity) bool
}

// Service 是长期记忆的唯一读写入口，隐藏作用域判断、容量与版本冲突。
type Service struct {
	store       *store
	maxChars    int
	injectBytes int
	groupWriter func(chat.Identity) bool
}

// Open 打开或创建记忆数据库，并返回服务实例。
func Open(path string, cfg Config) (*Service, error) {
	if path == "" {
		return nil, errors.New("记忆数据库路径不能为空")
	}
	st, err := openStore(path)
	if err != nil {
		return nil, err
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = DefaultMaxChars
	}
	if cfg.InjectBytes <= 0 {
		cfg.InjectBytes = DefaultInjectBytes
	}
	return &Service{store: st, maxChars: cfg.MaxChars, injectBytes: cfg.InjectBytes, groupWriter: cfg.GroupWriter}, nil
}

// Close 关闭数据库连接。
func (s *Service) Close() error { return s.store.Close() }

// MaxChars 返回每个空间的存储字符上限。
func (s *Service) MaxChars() int { return s.maxChars }

// InjectBytes 返回单次注入的字节上限。
func (s *Service) InjectBytes() int { return s.injectBytes }

func nowMillis() int64 { return time.Now().UnixMilli() }

// writeAllowed 判定身份能否直接写入该空间；个人空间已由身份推导，总是允许本人写入。
func (s *Service) writeAllowed(identity chat.Identity, scope Scope) bool {
	if scope.Kind == ScopeUser {
		return true
	}
	return s.groupWriter != nil && s.groupWriter(groupScopeIdentity(identity))
}

// groupScopeIdentity 把线程内身份收敛到群主会话，使线程写入权限不能修改整个群的记忆。
func groupScopeIdentity(identity chat.Identity) chat.Identity {
	identity.Session.Thread = ""
	identity.Direct = false
	return identity
}

// List 返回当前空间内按注入优先级排序的条目。
func (s *Service) List(ctx context.Context, identity chat.Identity) ([]Entry, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, err
	}
	return s.list(ctx, scope)
}

func (s *Service) list(ctx context.Context, scope Scope) (entries []Entry, err error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+entryColumns+` FROM memory_entries WHERE scope_key=? ORDER BY explicit DESC, updated_at DESC, id DESC`, scope.Key())
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Usage 返回当前空间的容量占用。
func (s *Service) Usage(ctx context.Context, identity chat.Identity) (Usage, error) {
	scope, err := space(identity)
	if err != nil {
		return Usage{}, err
	}
	return s.usage(ctx, scope)
}

func (s *Service) usage(ctx context.Context, scope Scope) (Usage, error) {
	u := Usage{Scope: scope, MaxChars: s.maxChars}
	err := s.store.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(length(content)),0) FROM memory_entries WHERE scope_key=?`, scope.Key()).Scan(&u.Count, &u.Chars)
	return u, err
}

// space 从可信身份推导作用域；无法证明可信空间时返回 ErrScope。
func space(identity chat.Identity) (Scope, error) {
	scope, ok := Space(identity)
	if !ok {
		return Scope{}, ErrScope
	}
	return scope, nil
}

// Add 在当前空间新增条目；群空间无写权限时形成待确认建议。
func (s *Service) Add(ctx context.Context, identity chat.Identity, content string, source Source, explicit bool) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	return s.write(ctx, identity, scope, mutationInput{Action: ActionAdd, Content: content, Source: source, Explicit: explicit})
}

// Replace 修改当前空间条目；群空间无写权限时形成待确认建议。
func (s *Service) Replace(ctx context.Context, identity chat.Identity, id, expectedVersion int64, content string, source Source) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	return s.write(ctx, identity, scope, mutationInput{Action: ActionReplace, EntryID: id, ExpectedVersion: expectedVersion, Content: content, Source: source, Explicit: true})
}

// Remove 删除当前空间条目；群空间无写权限时形成待确认建议。
func (s *Service) Remove(ctx context.Context, identity chat.Identity, id, expectedVersion int64, source Source) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	return s.write(ctx, identity, scope, mutationInput{Action: ActionRemove, EntryID: id, ExpectedVersion: expectedVersion, Source: source, Explicit: true})
}

// mutationInput 是 applyTx 的内部输入。
type mutationInput struct {
	Action          string
	EntryID         int64
	ExpectedVersion int64
	Content         string
	Source          Source
	Explicit        bool
}

// write 按写权限直接应用变更，或将其记录为待确认建议。
func (s *Service) write(ctx context.Context, identity chat.Identity, scope Scope, in mutationInput) (Mutation, error) {
	if err := validateMutation(in); err != nil {
		return Mutation{}, err
	}
	in.Source = sourceWithCreator(in.Source, identity)
	if s.writeAllowed(identity, scope) {
		entry, duplicate, err := s.apply(ctx, scope, in)
		return Mutation{Entry: entry, Duplicate: duplicate}, err
	}
	change, err := s.propose(ctx, scope, in)
	return Mutation{Change: change, Suggested: true}, err
}

// validateMutation 拒绝无意义或缺少必要字段的变更。
func validateMutation(in mutationInput) error {
	if in.Action != ActionAdd && in.Action != ActionReplace && in.Action != ActionRemove {
		return fmt.Errorf("未知记忆操作 %q", in.Action)
	}
	if in.Action == ActionAdd || in.Action == ActionReplace {
		if strings.TrimSpace(in.Content) == "" {
			return ErrEmptyContent
		}
	}
	if in.Action != ActionAdd && in.EntryID <= 0 {
		return errors.New("修改或删除记忆需要条目 ID")
	}
	return nil
}

func sourceWithCreator(source Source, identity chat.Identity) Source {
	source.Creator = identity.SenderID
	return source
}

// apply 在单个事务内应用变更，返回生效条目。
func (s *Service) apply(ctx context.Context, scope Scope, in mutationInput) (Entry, bool, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	entry, duplicate, err := s.applyTx(ctx, tx, scope, in, nowMillis())
	if err != nil {
		return Entry{}, false, err
	}
	return entry, duplicate, tx.Commit()
}

// applyTx 在调用方事务内应用变更，容量判断与写入保持原子。
// duplicate 为 true 时表示内容已存在，未新增条目。
func (s *Service) applyTx(ctx context.Context, tx *sql.Tx, scope Scope, in mutationInput, now int64) (Entry, bool, error) {
	key := scope.Key()
	switch in.Action {
	case ActionAdd:
		existing, err := queryEntry(ctx, tx, key, in.Content)
		if err == nil {
			return existing, true, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Entry{}, false, err
		}
		if err := s.checkCapacity(ctx, tx, key, utf8.RuneCountInString(in.Content)); err != nil {
			return Entry{}, false, err
		}
		explicit := 0
		if in.Explicit {
			explicit = 1
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO memory_entries(scope_key,content,version,explicit,creator,source_task,source_message,created_at,updated_at)
			VALUES(?,?,1,?,?,?,?,?,?)`, key, in.Content, explicit, in.Source.Creator, in.Source.Task, in.Source.Message, now, now)
		if err != nil {
			return Entry{}, false, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return Entry{}, false, err
		}
		entry, err := queryEntryByID(ctx, tx, key, id)
		return entry, false, err
	case ActionReplace:
		return s.replaceTx(ctx, tx, key, in, now)
	default:
		return s.removeTx(ctx, tx, key, in)
	}
}

func (s *Service) replaceTx(ctx context.Context, tx *sql.Tx, key string, in mutationInput, now int64) (Entry, bool, error) {
	current, err := queryEntryByID(ctx, tx, key, in.EntryID)
	if err != nil {
		return Entry{}, false, err
	}
	if in.ExpectedVersion > 0 && current.Version != in.ExpectedVersion {
		return Entry{}, false, fmt.Errorf("%w：条目 #%d 当前版本 %d，建议版本 %d", ErrConflict, current.ID, current.Version, in.ExpectedVersion)
	}
	if current.Content == in.Content {
		return current, false, nil
	}
	if dup, err := queryEntry(ctx, tx, key, in.Content); err == nil && dup.ID != current.ID {
		return Entry{}, false, ErrDuplicate
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return Entry{}, false, err
	}
	delta := utf8.RuneCountInString(in.Content) - utf8.RuneCountInString(current.Content)
	if err := s.checkCapacity(ctx, tx, key, delta); err != nil {
		return Entry{}, false, err
	}
	explicit := 0
	if in.Explicit {
		explicit = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE memory_entries SET content=?,version=version+1,explicit=?,updated_at=? WHERE id=? AND scope_key=? AND version=?`,
		in.Content, explicit, now, current.ID, key, current.Version)
	if err != nil {
		return Entry{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Entry{}, false, err
	}
	if affected == 0 {
		return Entry{}, false, fmt.Errorf("%w：条目 #%d 已被其他任务修改", ErrConflict, current.ID)
	}
	entry, err := queryEntryByID(ctx, tx, key, current.ID)
	return entry, false, err
}

func (s *Service) removeTx(ctx context.Context, tx *sql.Tx, key string, in mutationInput) (Entry, bool, error) {
	current, err := queryEntryByID(ctx, tx, key, in.EntryID)
	if err != nil {
		return Entry{}, false, err
	}
	if in.ExpectedVersion > 0 && current.Version != in.ExpectedVersion {
		return Entry{}, false, fmt.Errorf("%w：条目 #%d 当前版本 %d，建议版本 %d", ErrConflict, current.ID, current.Version, in.ExpectedVersion)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM memory_entries WHERE id=? AND scope_key=? AND version=?`, current.ID, key, current.Version)
	if err != nil {
		return Entry{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Entry{}, false, err
	}
	if affected == 0 {
		return Entry{}, false, fmt.Errorf("%w：条目 #%d 已被其他任务修改", ErrConflict, current.ID)
	}
	if current.SourceTask > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_excluded_sources(scope_key, task_id, reason, created_at) VALUES(?, ?, 'forgotten', ?)`, key, current.SourceTask, nowMillis()); err != nil {
			return Entry{}, false, err
		}
	}
	return current, false, nil
}

// checkCapacity 在同一事务内校验空间容量，delta 为本次变更的字符增量。
func (s *Service) checkCapacity(ctx context.Context, tx *sql.Tx, key string, delta int) error {
	var used int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(content)),0) FROM memory_entries WHERE scope_key=?`, key).Scan(&used); err != nil {
		return err
	}
	if used+delta > s.maxChars {
		return fmt.Errorf("%w：当前 %d/%d 字符，请先整理或删除后再写入", ErrFull, used, s.maxChars)
	}
	return nil
}

// queryEntry 按内容查找条目，未找到时返回 ErrNotFound。
func queryEntry(ctx context.Context, tx *sql.Tx, key, content string) (Entry, error) {
	entry, err := scanEntry(tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM memory_entries WHERE scope_key=? AND content=?`, key, content))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return entry, err
}

// queryEntryByID 按编号查找条目，未找到时返回 ErrNotFound，避免跨空间读取。
func queryEntryByID(ctx context.Context, tx *sql.Tx, key string, id int64) (Entry, error) {
	entry, err := scanEntry(tx.QueryRowContext(ctx, `SELECT `+entryColumns+` FROM memory_entries WHERE scope_key=? AND id=?`, key, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return entry, err
}

// propose 记录一条待确认建议，不修改任何已生效条目。
func (s *Service) propose(ctx context.Context, scope Scope, in mutationInput) (Change, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Change{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `INSERT INTO memory_changes(scope_key,action,entry_id,expected_version,content,proposer,source_task,source_message,status,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, scope.Key(), in.Action, in.EntryID, in.ExpectedVersion, in.Content, in.Source.Creator, in.Source.Task, in.Source.Message, StatusPending, nowMillis())
	if err != nil {
		return Change{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Change{}, err
	}
	change, err := scanChange(tx.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM memory_changes WHERE id=?`, id))
	if err != nil {
		return Change{}, err
	}
	return change, tx.Commit()
}

// Pending 返回当前空间的待确认建议。
func (s *Service) Pending(ctx context.Context, identity chat.Identity) ([]Change, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, err
	}
	return s.pending(ctx, scope)
}

func (s *Service) pending(ctx context.Context, scope Scope) (changes []Change, err error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+changeColumns+` FROM memory_changes WHERE scope_key=? AND status=? ORDER BY id`, scope.Key(), StatusPending)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		c, err := scanChange(rows)
		if err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}

// Approve 由有写权限的成员确认建议；审批时重新检查权限与目标版本。
func (s *Service) Approve(ctx context.Context, identity chat.Identity, changeID int64) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	if !s.writeAllowed(identity, scope) {
		return Mutation{}, errors.New("只有本群记忆管理员可以确认建议")
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Mutation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	change, err := scanChange(tx.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM memory_changes WHERE id=? AND scope_key=? AND status=?`, changeID, scope.Key(), StatusPending))
	if errors.Is(err, sql.ErrNoRows) {
		return Mutation{}, ErrNotFound
	}
	if err != nil {
		return Mutation{}, err
	}
	in := mutationInput{
		Action:          change.Action,
		EntryID:         change.EntryID,
		ExpectedVersion: change.ExpectedVersion,
		Content:         change.Content,
		Source:          Source{Creator: change.Proposer, Task: change.SourceTask, Message: change.SourceMessage},
		Explicit:        true,
	}
	entry, duplicate, err := s.applyTx(ctx, tx, scope, in, nowMillis())
	if err != nil {
		return Mutation{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory_changes SET status=?,decided_at=? WHERE id=? AND status=?`, StatusApplied, nowMillis(), change.ID, StatusPending); err != nil {
		return Mutation{}, err
	}
	return Mutation{Entry: entry, Duplicate: duplicate}, tx.Commit()
}

// Reject 由有写权限的成员拒绝建议；被拒绝的建议不再生效。
func (s *Service) Reject(ctx context.Context, identity chat.Identity, changeID int64) (Change, error) {
	scope, err := space(identity)
	if err != nil {
		return Change{}, err
	}
	if !s.writeAllowed(identity, scope) {
		return Change{}, errors.New("只有本群记忆管理员可以处理建议")
	}
	result, err := s.store.db.ExecContext(ctx, `UPDATE memory_changes SET status=?,decided_at=? WHERE id=? AND scope_key=? AND status=?`, StatusRejected, nowMillis(), changeID, scope.Key(), StatusPending)
	if err != nil {
		return Change{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Change{}, err
	}
	if affected == 0 {
		return Change{}, ErrNotFound
	}
	change, err := scanChange(s.store.db.QueryRowContext(ctx, `SELECT `+changeColumns+` FROM memory_changes WHERE id=?`, changeID))
	if err == nil && change.SourceTask > 0 {
		_, _ = s.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO memory_excluded_sources(scope_key, task_id, reason, created_at) VALUES(?, ?, 'rejected', ?)`, scope.Key(), change.SourceTask, nowMillis())
	}
	return change, err
}
