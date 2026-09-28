package memory

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"rua.plus/saber/internal/chat"
)

// Pause 暂停当前空间的长期记忆自动学习，现有记忆仍可正常读取。
// 私聊空间允许本人暂停；群聊空间要求具备管理员权限。
func (s *Service) Pause(ctx context.Context, identity chat.Identity) error {
	scope, err := space(identity)
	if err != nil {
		return err
	}
	if !s.writeAllowed(identity, scope) {
		return errors.New("只有本群记忆管理员可以控制自动学习")
	}
	now := nowMillis()
	_, err = s.store.db.ExecContext(ctx, `
		INSERT INTO memory_scope_settings(scope_key, paused, updated_at)
		VALUES(?, 1, ?)
		ON CONFLICT(scope_key) DO UPDATE SET paused = 1, updated_at = excluded.updated_at
	`, scope.Key(), now)
	return err
}

// Resume 恢复当前空间的长期记忆自动学习。
// 私聊空间允许本人恢复；群聊空间要求具备管理员权限。
func (s *Service) Resume(ctx context.Context, identity chat.Identity) error {
	scope, err := space(identity)
	if err != nil {
		return err
	}
	if !s.writeAllowed(identity, scope) {
		return errors.New("只有本群记忆管理员可以控制自动学习")
	}
	now := nowMillis()
	_, err = s.store.db.ExecContext(ctx, `
		INSERT INTO memory_scope_settings(scope_key, paused, updated_at)
		VALUES(?, 0, ?)
		ON CONFLICT(scope_key) DO UPDATE SET paused = 0, updated_at = excluded.updated_at
	`, scope.Key(), now)
	return err
}

// IsPaused 查询指定空间是否已暂停自动学习。未记录时默认为未暂停。
func (s *Service) IsPaused(ctx context.Context, scope Scope) (bool, error) {
	var paused int
	err := s.store.db.QueryRowContext(ctx, `SELECT paused FROM memory_scope_settings WHERE scope_key=?`, scope.Key()).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return paused != 0, nil
}

// ScopeSettings 查询指定空间的设置与复盘统计。
func (s *Service) ScopeSettings(ctx context.Context, scope Scope) (ScopeSettings, error) {
	var paused int
	var reviewCount, tokenCount int
	var lastTaskID, updated int64
	err := s.store.db.QueryRowContext(ctx, `
		SELECT paused, review_count, token_count, last_reviewed_task_id, updated_at
		FROM memory_scope_settings WHERE scope_key=?
	`, scope.Key()).Scan(&paused, &reviewCount, &tokenCount, &lastTaskID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopeSettings{Scope: scope}, nil
	}
	if err != nil {
		return ScopeSettings{}, err
	}
	return ScopeSettings{
		Scope:              scope,
		Paused:             paused != 0,
		ReviewCount:        reviewCount,
		TokenCount:         tokenCount,
		LastReviewedTaskID: lastTaskID,
		UpdatedAt:          time.UnixMilli(updated),
	}, nil
}

// Status 返回当前可信空间的容量、学习状态、复盘次数与未确认建议总数。
func (s *Service) Status(ctx context.Context, identity chat.Identity) (ScopeStatus, error) {
	scope, err := space(identity)
	if err != nil {
		return ScopeStatus{}, err
	}
	usage, err := s.usage(ctx, scope)
	if err != nil {
		return ScopeStatus{}, err
	}
	settings, err := s.ScopeSettings(ctx, scope)
	if err != nil {
		return ScopeStatus{}, err
	}
	var pendingCount int
	err = s.store.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_changes WHERE scope_key=? AND status=?`, scope.Key(), StatusPending).Scan(&pendingCount)
	if err != nil {
		return ScopeStatus{}, err
	}
	return ScopeStatus{
		Usage:        usage,
		Paused:       settings.Paused,
		ReviewCount:  settings.ReviewCount,
		TokenCount:   settings.TokenCount,
		PendingCount: pendingCount,
	}, nil
}

// GlobalUsage 查询全部空间累计的后台复盘总次数与 token 消耗。
func (s *Service) GlobalUsage(ctx context.Context) (reviews int, tokens int, err error) {
	err = s.store.db.QueryRowContext(ctx, `SELECT COALESCE(sum(review_count), 0), COALESCE(sum(token_count), 0) FROM memory_scope_settings`).Scan(&reviews, &tokens)
	return reviews, tokens, err
}

// ExcludeSource 将指定任务设为排除来源，阻止其再次参与自动提炼。
func (s *Service) ExcludeSource(ctx context.Context, scope Scope, taskID int64, reason string) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	if taskID <= 0 {
		return errors.New("任务编号必须为正整数")
	}
	_, err := s.store.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO memory_excluded_sources(scope_key, task_id, reason, created_at)
		VALUES(?, ?, ?, ?)
	`, scope.Key(), taskID, reason, nowMillis())
	return err
}

// IsSourceExcluded 查询任务是否已被当前空间排除。
func (s *Service) IsSourceExcluded(ctx context.Context, scope Scope, taskID int64) (bool, error) {
	var count int
	err := s.store.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_excluded_sources WHERE scope_key=? AND task_id=?`, scope.Key(), taskID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// UnreviewedProjections 查询指定空间尚未被复盘且未被排除的历史记录，按时间正序排列。
// 若该空间已暂停自动学习，则返回空列表。
func (s *Service) UnreviewedProjections(ctx context.Context, scope Scope, limit int) ([]HistoryRecord, error) {
	paused, err := s.IsPaused(ctx, scope)
	if err != nil {
		return nil, err
	}
	if paused {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT `+historyColumns+` FROM history_projections
		WHERE scope_key = ?
		  AND task_id NOT IN (SELECT task_id FROM memory_excluded_sources WHERE scope_key = ?)
		  AND task_id NOT IN (SELECT task_id FROM memory_reviewed_tasks WHERE scope_key = ?)
		ORDER BY id ASC
		LIMIT ?
	`, scope.Key(), scope.Key(), scope.Key(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var records []HistoryRecord
	for rows.Next() {
		r, err := scanHistory(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// PendingReviewScopes 发现存在待复盘记录且未暂停的记忆空间列表。
func (s *Service) PendingReviewScopes(ctx context.Context, minTurns int) ([]Scope, error) {
	if minTurns <= 0 {
		minTurns = 5
	}
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT p.scope_kind, p.platform, p.account, p.conversation, p.sender_id
		FROM history_projections p
		WHERE p.task_id NOT IN (SELECT task_id FROM memory_excluded_sources WHERE scope_key = p.scope_key)
		  AND p.task_id NOT IN (SELECT task_id FROM memory_reviewed_tasks WHERE scope_key = p.scope_key)
		  AND p.scope_key NOT IN (SELECT scope_key FROM memory_scope_settings WHERE paused = 1)
		GROUP BY p.scope_key
		HAVING count(*) >= ?
	`, minTurns)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var scopes []Scope
	for rows.Next() {
		var kind, platform, account, conversation, senderID string
		if err := rows.Scan(&kind, &platform, &account, &conversation, &senderID); err != nil {
			return nil, err
		}
		id := conversation
		if kind == ScopeUser {
			id = senderID
		}
		scope := Scope{Kind: kind, Platform: platform, Account: account, ID: id}
		if scope.Validate() == nil {
			scopes = append(scopes, scope)
		}
	}
	return scopes, rows.Err()
}

// MarkTasksReviewed 记录任务已完成复盘，更新最后复盘任务标记。
func (s *Service) MarkTasksReviewed(ctx context.Context, scope Scope, taskIDs []int64) error {
	if len(taskIDs) == 0 {
		return nil
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := nowMillis()
	var maxTaskID int64
	for _, id := range taskIDs {
		if id > maxTaskID {
			maxTaskID = id
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO memory_reviewed_tasks(scope_key, task_id, reviewed_at)
			VALUES(?, ?, ?)
		`, scope.Key(), id, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_scope_settings(scope_key, last_reviewed_task_id, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(scope_key) DO UPDATE SET
			last_reviewed_task_id = MAX(last_reviewed_task_id, excluded.last_reviewed_task_id),
			updated_at = excluded.updated_at
	`, scope.Key(), maxTaskID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordReviewUsage 记录一次后台复盘执行的次数递增与 token 消耗。
func (s *Service) RecordReviewUsage(ctx context.Context, scope Scope, tokens int) error {
	now := nowMillis()
	_, err := s.store.db.ExecContext(ctx, `
		INSERT INTO memory_scope_settings(scope_key, review_count, token_count, updated_at)
		VALUES(?, 1, ?, ?)
		ON CONFLICT(scope_key) DO UPDATE SET
			review_count = review_count + 1,
			token_count = token_count + excluded.token_count,
			updated_at = excluded.updated_at
	`, scope.Key(), tokens, now)
	return err
}

// AutoAdd 在自动复盘时新增条目：私聊空间直接写入生效（Explicit 为 false），群聊空间形成待确认建议。
func (s *Service) AutoAdd(ctx context.Context, identity chat.Identity, content string, source Source) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	paused, err := s.IsPaused(ctx, scope)
	if err != nil {
		return Mutation{}, err
	}
	if paused {
		return Mutation{}, ErrPaused
	}
	in := mutationInput{Action: ActionAdd, Content: content, Source: sourceWithCreator(source, identity), Explicit: false}
	if err := validateMutation(in); err != nil {
		return Mutation{}, err
	}
	if scope.Kind == ScopeUser {
		entry, duplicate, err := s.apply(ctx, scope, in)
		return Mutation{Entry: entry, Duplicate: duplicate}, err
	}
	change, err := s.propose(ctx, scope, in)
	return Mutation{Change: change, Suggested: true}, err
}

// AutoReplace 在自动复盘时修改已有条目：无论私聊或群聊均形成待确认建议，需本人或管理员确认后生效。
func (s *Service) AutoReplace(ctx context.Context, identity chat.Identity, id, expectedVersion int64, content string, source Source) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	paused, err := s.IsPaused(ctx, scope)
	if err != nil {
		return Mutation{}, err
	}
	if paused {
		return Mutation{}, ErrPaused
	}
	if expectedVersion <= 0 {
		var currentVer int64
		err := s.store.db.QueryRowContext(ctx, `SELECT version FROM memory_entries WHERE id=? AND scope_key=?`, id, scope.Key()).Scan(&currentVer)
		if errors.Is(err, sql.ErrNoRows) {
			return Mutation{}, ErrNotFound
		}
		if err != nil {
			return Mutation{}, err
		}
		expectedVersion = currentVer
	}
	in := mutationInput{Action: ActionReplace, EntryID: id, ExpectedVersion: expectedVersion, Content: content, Source: sourceWithCreator(source, identity), Explicit: false}
	if err := validateMutation(in); err != nil {
		return Mutation{}, err
	}
	change, err := s.propose(ctx, scope, in)
	return Mutation{Change: change, Suggested: true}, err
}

// AutoRemove 在自动复盘时删除已有条目：无论私聊或群聊均形成待确认建议，需本人或管理员确认后生效。
func (s *Service) AutoRemove(ctx context.Context, identity chat.Identity, id, expectedVersion int64, source Source) (Mutation, error) {
	scope, err := space(identity)
	if err != nil {
		return Mutation{}, err
	}
	paused, err := s.IsPaused(ctx, scope)
	if err != nil {
		return Mutation{}, err
	}
	if paused {
		return Mutation{}, ErrPaused
	}
	if expectedVersion <= 0 {
		var currentVer int64
		err := s.store.db.QueryRowContext(ctx, `SELECT version FROM memory_entries WHERE id=? AND scope_key=?`, id, scope.Key()).Scan(&currentVer)
		if errors.Is(err, sql.ErrNoRows) {
			return Mutation{}, ErrNotFound
		}
		if err != nil {
			return Mutation{}, err
		}
		expectedVersion = currentVer
	}
	in := mutationInput{Action: ActionRemove, EntryID: id, ExpectedVersion: expectedVersion, Source: sourceWithCreator(source, identity), Explicit: false}
	if err := validateMutation(in); err != nil {
		return Mutation{}, err
	}
	change, err := s.propose(ctx, scope, in)
	return Mutation{Change: change, Suggested: true}, err
}
