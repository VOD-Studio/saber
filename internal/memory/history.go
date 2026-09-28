package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// RecordProjection 写入或更新历史对话投影。
//
// 来源任务必须提供正整数编号与可信作用域。完全相同的任务以 task_id 去重并更新最新状态。
func (s *Service) RecordProjection(ctx context.Context, in ProjectionInput) (HistoryRecord, error) {
	if in.TaskID <= 0 {
		return HistoryRecord{}, errors.New("历史投影需要任务 ID")
	}
	if err := in.Scope.Validate(); err != nil {
		return HistoryRecord{}, fmt.Errorf("历史投影作用域无效: %w", err)
	}
	created := in.CreatedAt.UnixMilli()
	if created <= 0 {
		created = nowMillis()
	}
	key := in.Scope.Key()
	_, err := s.store.db.ExecContext(ctx, `
		INSERT INTO history_projections(
			task_id, scope_key, scope_kind, platform, account, conversation, thread,
			sender_id, user_message_id, user_text, assistant_text, task_status, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			user_text = excluded.user_text,
			assistant_text = excluded.assistant_text,
			task_status = excluded.task_status
	`, in.TaskID, key, in.Scope.Kind, in.Scope.Platform, in.Scope.Account, in.Conversation, in.Thread,
		in.SenderID, in.UserMessageID, in.UserText, in.AssistantText, in.TaskStatus, created)
	if err != nil {
		return HistoryRecord{}, err
	}
	return s.getHistoryByTaskID(ctx, in.TaskID)
}

func (s *Service) getHistoryByTaskID(ctx context.Context, taskID int64) (HistoryRecord, error) {
	row := s.store.db.QueryRowContext(ctx, `SELECT `+historyColumns+` FROM history_projections WHERE task_id=?`, taskID)
	return scanHistory(row)
}

// SearchHistory 在当前可信作用域内检索历史记录。
//
// 授权条件在检索前生效，私聊只检索本人私聊，群聊只检索本群。
// 3 个及以上字符使用 SQLite FTS5 trigram 全文索引；少于 3 个字符使用字面子串模糊匹配。
func (s *Service) SearchHistory(ctx context.Context, identity chat.Identity, opts HistorySearchOptions) (HistorySearchResult, error) {
	scope, err := space(identity)
	if err != nil {
		return HistorySearchResult{}, err
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return HistorySearchResult{Scope: scope}, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	} else if limit > 20 {
		limit = 20
	}
	runes := utf8.RuneCountInString(query)
	var records []HistoryRecord
	if runes < 3 {
		records, err = s.searchHistoryShort(ctx, scope, query, opts.Cursor, limit+1)
	} else {
		records, err = s.searchHistoryTrigram(ctx, scope, query, opts.Cursor, limit+1)
	}
	if err != nil {
		return HistorySearchResult{}, err
	}
	var nextCursor int64
	hasMore := false
	if len(records) > limit {
		hasMore = true
		nextCursor = records[limit-1].ID
		records = records[:limit]
	}
	return HistorySearchResult{
		Scope:      scope,
		Query:      query,
		Records:    records,
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func (s *Service) searchHistoryShort(ctx context.Context, scope Scope, query string, cursor int64, fetchCount int) ([]HistoryRecord, error) {
	likePattern := "%" + escapeLike(query) + "%"
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT `+historyColumns+` FROM history_projections
		WHERE scope_key = ? AND (? <= 0 OR id < ?) AND (user_text LIKE ? ESCAPE '\' OR assistant_text LIKE ? ESCAPE '\')
		ORDER BY id DESC LIMIT ?
	`, scope.Key(), cursor, cursor, likePattern, likePattern, fetchCount)
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

func (s *Service) searchHistoryTrigram(ctx context.Context, scope Scope, query string, cursor int64, fetchCount int) ([]HistoryRecord, error) {
	phrase := escapeFTS5Phrase(query)
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT `+qualifiedHistoryColumns+`
		FROM history_projections p
		JOIN history_fts f ON f.rowid = p.id
		WHERE p.scope_key = ? AND (? <= 0 OR p.id < ?) AND history_fts MATCH ?
		ORDER BY p.id DESC LIMIT ?
	`, scope.Key(), cursor, cursor, phrase, fetchCount)
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

// ReadHistoryContext 读取指定记录及其前后上下文。
//
// 目标记录必须属于当前可信作用域，不同空间或不存在的编号返回 ErrNotFound，避免泄露其他群或用户的历史。
func (s *Service) ReadHistoryContext(ctx context.Context, identity chat.Identity, opts HistoryContextOptions) (HistoryContextResult, error) {
	scope, err := space(identity)
	if err != nil {
		return HistoryContextResult{}, err
	}
	if opts.ID <= 0 {
		return HistoryContextResult{}, errors.New("读取历史上下文需要有效记录 ID")
	}
	target, err := scanHistory(s.store.db.QueryRowContext(ctx, `SELECT `+historyColumns+` FROM history_projections WHERE id=? AND scope_key=?`, opts.ID, scope.Key()))
	if errors.Is(err, sql.ErrNoRows) {
		return HistoryContextResult{}, ErrNotFound
	}
	if err != nil {
		return HistoryContextResult{}, err
	}
	beforeLimit := opts.Before
	if beforeLimit < 0 {
		beforeLimit = 0
	} else if beforeLimit > 10 {
		beforeLimit = 10
	}
	afterLimit := opts.After
	if afterLimit < 0 {
		afterLimit = 0
	} else if afterLimit > 10 {
		afterLimit = 10
	}
	var before []HistoryRecord
	if beforeLimit > 0 {
		rows, err := s.store.db.QueryContext(ctx, `
			SELECT `+historyColumns+` FROM history_projections
			WHERE scope_key = ? AND id < ?
			ORDER BY id DESC LIMIT ?
		`, scope.Key(), target.ID, beforeLimit)
		if err != nil {
			return HistoryContextResult{}, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanHistory(rows)
			if err != nil {
				return HistoryContextResult{}, err
			}
			before = append(before, r)
		}
		if err := rows.Err(); err != nil {
			return HistoryContextResult{}, err
		}
		// 反转为时间递增顺序
		for i, j := 0, len(before)-1; i < j; i, j = i+1, j-1 {
			before[i], before[j] = before[j], before[i]
		}
	}
	var after []HistoryRecord
	if afterLimit > 0 {
		rows, err := s.store.db.QueryContext(ctx, `
			SELECT `+historyColumns+` FROM history_projections
			WHERE scope_key = ? AND id > ?
			ORDER BY id ASC LIMIT ?
		`, scope.Key(), target.ID, afterLimit)
		if err != nil {
			return HistoryContextResult{}, err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanHistory(rows)
			if err != nil {
				return HistoryContextResult{}, err
			}
			after = append(after, r)
		}
		if err := rows.Err(); err != nil {
			return HistoryContextResult{}, err
		}
	}
	return HistoryContextResult{
		Scope:  scope,
		Target: target,
		Before: before,
		After:  after,
	}, nil
}

// escapeLike 转义 LIKE 查询中的通配符 %、_ 与转义符自身 \。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// escapeFTS5Phrase 将检索关键词安全包装为 FTS5 短语，转义内部的双引号。
func escapeFTS5Phrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
