package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"rua.plus/saber/internal/chat"
)

// validateSkillName 校验技能名称是否符合标识符规范。
//
// 规范：1-64 字符，仅允许小写字母、数字、连字符、下划线与点，首字符必须为字母或数字。
func validateSkillName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return ErrInvalidSkillName
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
			if i == 0 {
				return ErrInvalidSkillName
			}
		default:
			return ErrInvalidSkillName
		}
	}
	return nil
}

// ListSkills 列出当前空间内的所有技能元信息。
//
// 返回按技能名称升序排序的技能列表。
func (s *Service) ListSkills(ctx context.Context, identity chat.Identity) ([]SkillEntry, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+skillColumns+` FROM skill_entries WHERE scope_key=? ORDER BY name ASC`, scope.Key())
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var skills []SkillEntry
	for rows.Next() {
		entry, err := scanSkill(rows, scope)
		if err != nil {
			return nil, err
		}
		skills = append(skills, entry)
	}
	return skills, rows.Err()
}

// GetSkill 获取当前空间内指定名称的技能完整正文。
//
// 参数:
//   - ctx: 上下文
//   - identity: 真实受信身份
//   - name: 技能标识名称
//
// 返回值:
//   - SkillEntry: 技能条目
//   - error: 技能不存在时返回 ErrSkillNotFound
func (s *Service) GetSkill(ctx context.Context, identity chat.Identity, name string) (SkillEntry, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillEntry{}, err
	}
	if err := validateSkillName(name); err != nil {
		return SkillEntry{}, err
	}
	return s.getSkill(ctx, scope, name)
}

func (s *Service) getSkill(ctx context.Context, scope Scope, name string) (SkillEntry, error) {
	row := s.store.db.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skill_entries WHERE scope_key=? AND name=?`, scope.Key(), name)
	entry, err := scanSkill(row, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillEntry{}, ErrSkillNotFound
	}
	return entry, err
}

func (s *Service) getSkillByID(ctx context.Context, scope Scope, id int64) (SkillEntry, error) {
	row := s.store.db.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skill_entries WHERE scope_key=? AND id=?`, scope.Key(), id)
	entry, err := scanSkill(row, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillEntry{}, ErrSkillNotFound
	}
	return entry, err
}

// AddSkill 在当前空间新增技能；群聊无写权限时形成待确认建议。
func (s *Service) AddSkill(ctx context.Context, identity chat.Identity, name, description, content string, source Source) (SkillMutation, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	return s.writeSkill(ctx, identity, scope, skillMutationInput{
		Action:      ActionAdd,
		Name:        name,
		Description: description,
		Content:     content,
		Source:      source,
	})
}

// ReplaceSkill 修改当前空间的已有技能；群聊无写权限时形成待确认建议。
func (s *Service) ReplaceSkill(ctx context.Context, identity chat.Identity, name string, expectedVersion int64, description, content string, source Source) (SkillMutation, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	return s.writeSkill(ctx, identity, scope, skillMutationInput{
		Action:          ActionReplace,
		Name:            name,
		ExpectedVersion: expectedVersion,
		Description:     description,
		Content:         content,
		Source:          source,
	})
}

// RemoveSkill 删除当前空间的已有技能；群聊无写权限时形成待确认建议。
func (s *Service) RemoveSkill(ctx context.Context, identity chat.Identity, name string, expectedVersion int64, source Source) (SkillMutation, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	return s.writeSkill(ctx, identity, scope, skillMutationInput{
		Action:          ActionRemove,
		Name:            name,
		ExpectedVersion: expectedVersion,
		Source:          source,
	})
}

type skillMutationInput struct {
	Action          string
	Name            string
	ExpectedVersion int64
	Description     string
	Content         string
	Source          Source
}

func validateSkillMutation(in skillMutationInput) error {
	if err := validateSkillName(in.Name); err != nil {
		return err
	}
	switch in.Action {
	case ActionAdd, ActionReplace:
		if strings.TrimSpace(in.Description) == "" {
			return ErrEmptySkillDescription
		}
		if strings.TrimSpace(in.Content) == "" {
			return ErrEmptyContent
		}
	case ActionRemove:
		// 删除只需名称与预期版本
	default:
		return fmt.Errorf("未知技能操作 %q", in.Action)
	}
	return nil
}

func (s *Service) writeSkill(ctx context.Context, identity chat.Identity, scope Scope, in skillMutationInput) (SkillMutation, error) {
	if err := validateSkillMutation(in); err != nil {
		return SkillMutation{}, err
	}
	in.Source = sourceWithCreator(in.Source, identity)
	if s.writeAllowed(identity, scope) {
		skill, duplicate, err := s.applySkill(ctx, scope, in)
		return SkillMutation{Skill: skill, Duplicate: duplicate}, err
	}
	change, err := s.proposeSkill(ctx, scope, in)
	return SkillMutation{Change: change, Suggested: true}, err
}

func (s *Service) applySkill(ctx context.Context, scope Scope, in skillMutationInput) (SkillEntry, bool, error) {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillEntry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	key := scope.Key()
	now := nowMillis()

	switch in.Action {
	case ActionAdd:
		current, err := querySkillByName(ctx, tx, scope, in.Name)
		if err == nil {
			if current.Description == in.Description && current.Content == in.Content {
				return current, true, nil
			}
			return SkillEntry{}, false, ErrSkillDuplicate
		}
		if !errors.Is(err, ErrSkillNotFound) {
			return SkillEntry{}, false, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO skill_entries(scope_key,name,description,content,version,creator,source_task,created_at,updated_at)
			VALUES(?,?,?,?,1,?,?,?,?)`, key, in.Name, in.Description, in.Content, in.Source.Creator, in.Source.Task, now, now)
		if err != nil {
			return SkillEntry{}, false, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return SkillEntry{}, false, err
		}
		skill, err := querySkillByID(ctx, tx, scope, id)
		if err != nil {
			return SkillEntry{}, false, err
		}
		return skill, false, tx.Commit()

	case ActionReplace:
		current, err := querySkillByName(ctx, tx, scope, in.Name)
		if err != nil {
			return SkillEntry{}, false, err
		}
		if in.ExpectedVersion > 0 && current.Version != in.ExpectedVersion {
			return SkillEntry{}, false, fmt.Errorf("%w：技能 %s 当前版本 %d，预期版本 %d", ErrSkillConflict, current.Name, current.Version, in.ExpectedVersion)
		}
		if current.Description == in.Description && current.Content == in.Content {
			return current, false, nil
		}
		res, err := tx.ExecContext(ctx, `UPDATE skill_entries SET description=?,content=?,version=version+1,updated_at=? WHERE id=? AND scope_key=? AND version=?`,
			in.Description, in.Content, now, current.ID, key, current.Version)
		if err != nil {
			return SkillEntry{}, false, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return SkillEntry{}, false, err
		}
		if affected == 0 {
			return SkillEntry{}, false, fmt.Errorf("%w：技能 %s 已被其他任务修改", ErrSkillConflict, current.Name)
		}
		skill, err := querySkillByID(ctx, tx, scope, current.ID)
		if err != nil {
			return SkillEntry{}, false, err
		}
		return skill, false, tx.Commit()

	case ActionRemove:
		current, err := querySkillByName(ctx, tx, scope, in.Name)
		if err != nil {
			return SkillEntry{}, false, err
		}
		if in.ExpectedVersion > 0 && current.Version != in.ExpectedVersion {
			return SkillEntry{}, false, fmt.Errorf("%w：技能 %s 当前版本 %d，预期版本 %d", ErrSkillConflict, current.Name, current.Version, in.ExpectedVersion)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM skill_entries WHERE id=? AND scope_key=? AND version=?`, current.ID, key, current.Version)
		if err != nil {
			return SkillEntry{}, false, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return SkillEntry{}, false, err
		}
		if affected == 0 {
			return SkillEntry{}, false, fmt.Errorf("%w：技能 %s 已被其他任务修改", ErrSkillConflict, current.Name)
		}
		if current.SourceTask > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_excluded_sources(scope_key, task_id, reason, created_at) VALUES(?, ?, 'forgotten', ?)`, key, current.SourceTask, nowMillis()); err != nil {
				return SkillEntry{}, false, err
			}
		}
		return current, false, tx.Commit()

	default:
		return SkillEntry{}, false, fmt.Errorf("未知技能操作 %q", in.Action)
	}
}

func (s *Service) proposeSkill(ctx context.Context, scope Scope, in skillMutationInput) (SkillChange, error) {
	key := scope.Key()
	now := nowMillis()

	var skillID int64
	if in.Action == ActionReplace || in.Action == ActionRemove {
		current, err := s.getSkill(ctx, scope, in.Name)
		if err != nil {
			return SkillChange{}, err
		}
		skillID = current.ID
		if in.ExpectedVersion <= 0 {
			in.ExpectedVersion = current.Version
		}
	}

	res, err := s.store.db.ExecContext(ctx, `INSERT INTO skill_changes(scope_key,action,skill_id,expected_version,name,description,content,proposer,source_task,status,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,'pending',?)`, key, in.Action, skillID, in.ExpectedVersion, in.Name, in.Description, in.Content, in.Source.Creator, in.Source.Task, now)
	if err != nil {
		return SkillChange{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return SkillChange{}, err
	}
	return s.getSkillChange(ctx, scope, id)
}

func (s *Service) getSkillChange(ctx context.Context, scope Scope, id int64) (SkillChange, error) {
	row := s.store.db.QueryRowContext(ctx, `SELECT `+skillChangeColumns+` FROM skill_changes WHERE scope_key=? AND id=?`, scope.Key(), id)
	return scanSkillChange(row, scope)
}

func querySkillByName(ctx context.Context, tx *sql.Tx, scope Scope, name string) (SkillEntry, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skill_entries WHERE scope_key=? AND name=?`, scope.Key(), name)
	entry, err := scanSkill(row, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillEntry{}, ErrSkillNotFound
	}
	return entry, err
}

func querySkillByID(ctx context.Context, tx *sql.Tx, scope Scope, id int64) (SkillEntry, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+skillColumns+` FROM skill_entries WHERE scope_key=? AND id=?`, scope.Key(), id)
	entry, err := scanSkill(row, scope)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillEntry{}, ErrSkillNotFound
	}
	return entry, err
}

// PendingSkillChanges 返回当前空间中所有待确认的技能建议。
func (s *Service) PendingSkillChanges(ctx context.Context, identity chat.Identity) ([]SkillChange, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+skillChangeColumns+` FROM skill_changes WHERE scope_key=? AND status='pending' ORDER BY id ASC`, scope.Key())
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	var list []SkillChange
	for rows.Next() {
		c, err := scanSkillChange(rows, scope)
		if err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// ApproveSkillChange 审批通过一条待确认技能建议，将其应用至生效条目。
func (s *Service) ApproveSkillChange(ctx context.Context, identity chat.Identity, changeID int64) (SkillEntry, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillEntry{}, err
	}
	if !s.writeAllowed(identity, scope) {
		return SkillEntry{}, errors.New("无权确认该空间的技能建议")
	}

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	change, err := scanSkillChange(tx.QueryRowContext(ctx, `SELECT `+skillChangeColumns+` FROM skill_changes WHERE id=? AND scope_key=? AND status='pending'`, changeID, scope.Key()), scope)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillEntry{}, errors.New("待确认建议不存在或已被处理")
	}
	if err != nil {
		return SkillEntry{}, err
	}

	in := skillMutationInput{
		Action:          change.Action,
		Name:            change.Name,
		ExpectedVersion: change.ExpectedVersion,
		Description:     change.Description,
		Content:         change.Content,
		Source:          Source{Creator: change.Proposer, Task: change.SourceTask},
	}

	now := nowMillis()
	var skill SkillEntry

	switch in.Action {
	case ActionAdd:
		current, err := querySkillByName(ctx, tx, scope, in.Name)
		if err == nil {
			return SkillEntry{}, fmt.Errorf("%w：技能 %s 已存在", ErrSkillDuplicate, current.Name)
		}
		if !errors.Is(err, ErrSkillNotFound) {
			return SkillEntry{}, err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO skill_entries(scope_key,name,description,content,version,creator,source_task,created_at,updated_at)
			VALUES(?,?,?,?,1,?,?,?,?)`, scope.Key(), in.Name, in.Description, in.Content, in.Source.Creator, in.Source.Task, now, now)
		if err != nil {
			return SkillEntry{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return SkillEntry{}, err
		}
		skill, err = querySkillByID(ctx, tx, scope, id)
		if err != nil {
			return SkillEntry{}, err
		}

	case ActionReplace:
		current, err := querySkillByName(ctx, tx, scope, in.Name)
		if err != nil {
			return SkillEntry{}, err
		}
		if in.ExpectedVersion > 0 && current.Version != in.ExpectedVersion {
			return SkillEntry{}, fmt.Errorf("%w：技能 %s 当前版本 %d，建议版本 %d", ErrSkillConflict, current.Name, current.Version, in.ExpectedVersion)
		}
		res, err := tx.ExecContext(ctx, `UPDATE skill_entries SET description=?,content=?,version=version+1,updated_at=? WHERE id=? AND scope_key=? AND version=?`,
			in.Description, in.Content, now, current.ID, scope.Key(), current.Version)
		if err != nil {
			return SkillEntry{}, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return SkillEntry{}, err
		}
		if affected == 0 {
			return SkillEntry{}, fmt.Errorf("%w：技能 %s 已被其他任务修改", ErrSkillConflict, current.Name)
		}
		skill, err = querySkillByID(ctx, tx, scope, current.ID)
		if err != nil {
			return SkillEntry{}, err
		}

	case ActionRemove:
		current, err := querySkillByName(ctx, tx, scope, in.Name)
		if err != nil {
			return SkillEntry{}, err
		}
		if in.ExpectedVersion > 0 && current.Version != in.ExpectedVersion {
			return SkillEntry{}, fmt.Errorf("%w：技能 %s 当前版本 %d，建议版本 %d", ErrSkillConflict, current.Name, current.Version, in.ExpectedVersion)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM skill_entries WHERE id=? AND scope_key=? AND version=?`, current.ID, scope.Key(), current.Version)
		if err != nil {
			return SkillEntry{}, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return SkillEntry{}, err
		}
		if affected == 0 {
			return SkillEntry{}, fmt.Errorf("%w：技能 %s 已被其他任务修改", ErrSkillConflict, current.Name)
		}
		if current.SourceTask > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_excluded_sources(scope_key, task_id, reason, created_at) VALUES(?, ?, 'forgotten', ?)`, scope.Key(), current.SourceTask, nowMillis()); err != nil {
				return SkillEntry{}, err
			}
		}
		skill = current

	default:
		return SkillEntry{}, fmt.Errorf("未知技能操作 %q", in.Action)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE skill_changes SET status='applied', decided_at=? WHERE id=?`, now, changeID); err != nil {
		return SkillEntry{}, err
	}

	return skill, tx.Commit()
}

// RejectSkillChange 驳回一条待确认技能建议，将其标记为 rejected 并记录来源排除。
func (s *Service) RejectSkillChange(ctx context.Context, identity chat.Identity, changeID int64) (SkillChange, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillChange{}, err
	}
	if !s.writeAllowed(identity, scope) {
		return SkillChange{}, errors.New("无权拒绝该空间的技能建议")
	}

	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return SkillChange{}, err
	}
	defer func() { _ = tx.Rollback() }()

	change, err := scanSkillChange(tx.QueryRowContext(ctx, `SELECT `+skillChangeColumns+` FROM skill_changes WHERE id=? AND scope_key=? AND status='pending'`, changeID, scope.Key()), scope)
	if errors.Is(err, sql.ErrNoRows) {
		return SkillChange{}, errors.New("待确认建议不存在或已被处理")
	}
	if err != nil {
		return SkillChange{}, err
	}

	now := nowMillis()
	if _, err := tx.ExecContext(ctx, `UPDATE skill_changes SET status='rejected', decided_at=? WHERE id=?`, now, changeID); err != nil {
		return SkillChange{}, err
	}
	if change.SourceTask > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memory_excluded_sources(scope_key, task_id, reason, created_at) VALUES(?, ?, 'rejected', ?)`, scope.Key(), change.SourceTask, now); err != nil {
			return SkillChange{}, err
		}
	}
	change.Status = StatusRejected
	change.DecidedAt = time.UnixMilli(now)
	return change, tx.Commit()
}

// AutoAddSkill 用于后台自动复盘提炼新增技能。
//
// 个人空间直接生效；群共享空间一律先转为待确认建议。
func (s *Service) AutoAddSkill(ctx context.Context, identity chat.Identity, name, description, content string, source Source) (SkillMutation, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	in := skillMutationInput{
		Action:      ActionAdd,
		Name:        name,
		Description: description,
		Content:     content,
		Source:      source,
	}
	if err := validateSkillMutation(in); err != nil {
		return SkillMutation{}, err
	}
	in.Source = sourceWithCreator(in.Source, identity)
	if scope.Kind == ScopeUser {
		skill, duplicate, err := s.applySkill(ctx, scope, in)
		return SkillMutation{Skill: skill, Duplicate: duplicate}, err
	}
	change, err := s.proposeSkill(ctx, scope, in)
	return SkillMutation{Change: change, Suggested: true}, err
}

// AutoReplaceSkill 用于后台自动复盘提炼修改技能。
//
// 为防止自动复盘静默覆盖已有流程，无论个人还是群空间均转为待确认建议。
func (s *Service) AutoReplaceSkill(ctx context.Context, identity chat.Identity, name string, expectedVersion int64, description, content string, source Source) (SkillMutation, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	in := skillMutationInput{
		Action:          ActionReplace,
		Name:            name,
		ExpectedVersion: expectedVersion,
		Description:     description,
		Content:         content,
		Source:          source,
	}
	if err := validateSkillMutation(in); err != nil {
		return SkillMutation{}, err
	}
	in.Source = sourceWithCreator(in.Source, identity)
	change, err := s.proposeSkill(ctx, scope, in)
	return SkillMutation{Change: change, Suggested: true}, err
}

// AutoRemoveSkill 用于后台自动复盘提炼删除技能。
//
// 为防止误删已生效流程，无论个人还是群空间均转为待确认建议。
func (s *Service) AutoRemoveSkill(ctx context.Context, identity chat.Identity, name string, expectedVersion int64, source Source) (SkillMutation, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	in := skillMutationInput{
		Action:          ActionRemove,
		Name:            name,
		ExpectedVersion: expectedVersion,
		Source:          source,
	}
	if err := validateSkillMutation(in); err != nil {
		return SkillMutation{}, err
	}
	in.Source = sourceWithCreator(in.Source, identity)
	change, err := s.proposeSkill(ctx, scope, in)
	return SkillMutation{Change: change, Suggested: true}, err
}

// SkillCatalogSnapshot 生成当前空间可用技能的轻量目录快照（仅包含名称、描述与版本）。
//
// 遵循渐进式加载（Progressive Disclosure）原则，正文不在此阶段注入，由模型按需读取。
// 当没有技能或预算不足时返回空字符串。
func (s *Service) SkillCatalogSnapshot(ctx context.Context, identity chat.Identity, budget int) (string, error) {
	scope, err := space(identity)
	if err != nil {
		return "", err
	}
	skills, err := s.ListSkills(ctx, identity)
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "", nil
	}

	header := fmt.Sprintf("【可用技能目录（%s，按需使用 saber_skill 读取详情）】\n", scope.String())
	footer := "\n【技能目录结束】"
	minBytes := len(header) + len(footer)
	if budget > 0 && budget < minBytes {
		return "", nil
	}

	var items []string
	currentLen := len(header) + len(footer)

	for _, sk := range skills {
		line := fmt.Sprintf("- %s: %s (v%d)", sk.Name, sk.Description, sk.Version)
		extra := len(line)
		if len(items) > 0 {
			extra += 1 // 换行符
		}
		if budget > 0 && currentLen+extra > budget {
			break
		}
		items = append(items, line)
		currentLen += extra
	}

	if len(items) == 0 {
		return "", nil
	}

	return header + strings.Join(items, "\n") + footer, nil
}
