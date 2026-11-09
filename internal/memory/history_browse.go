package memory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// HistoryBrowseOptions 在可信空间内按会话、时间与游标浏览，不接受平台/用户身份。
type HistoryBrowseOptions struct {
	SessionID string
	Query     string
	Since     time.Time
	Until     time.Time
	Cursor    int64
	Limit     int
}

// HistorySession 是当前空间中的真实历史会话目录，时间为授权记录的书目边界。
type HistorySession struct {
	ID           string    `json:"id"`
	Conversation string    `json:"conversation"`
	Thread       string    `json:"thread"`
	Count        int64     `json:"count"`
	FirstAt      time.Time `json:"first_at"`
	LastAt       time.Time `json:"last_at"`
	LastRecordID int64     `json:"last_record_id"`
}

func historySessionID(conversation, thread string) string {
	data, _ := json.Marshal([]string{conversation, thread})
	return string(data)
}
func historySessionParts(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	var parts []string
	if err := json.Unmarshal([]byte(value), &parts); err != nil || len(parts) != 2 || parts[0] == "" {
		return "", "", errors.New("session_id 必须由历史 sessions 返回")
	}
	return parts[0], parts[1], nil
}

// ParseHistoryTime 解析 RFC3339、日期和相对 h/d/w 范围；日期明确以 UTC 解释。
func ParseHistoryTime(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if len(value) > 1 {
		unit := value[len(value)-1]
		var scale time.Duration
		switch unit {
		case 'h':
			scale = time.Hour
		case 'd':
			scale = 24 * time.Hour
		case 'w':
			scale = 7 * 24 * time.Hour
		}
		if scale != 0 {
			n, err := strconv.ParseInt(value[:len(value)-1], 10, 32)
			if err != nil || n <= 0 || n > 100000 {
				return time.Time{}, errors.New("相对历史时间需要正整数 h/d/w")
			}
			return now.Add(-time.Duration(n) * scale), nil
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("历史时间必须 RFC3339、YYYY-MM-DD 或 24h/7d/2w")
}

func historyWindowArgs(scope Scope, opts HistoryBrowseOptions) (string, []any, error) {
	if opts.Cursor < 0 {
		return "", nil, errors.New("历史游标不得为负")
	}
	if !opts.Since.IsZero() && !opts.Until.IsZero() && !opts.Since.Before(opts.Until) {
		return "", nil, errors.New("历史 since 必须早于 until")
	}
	conversation, thread, err := historySessionParts(opts.SessionID)
	if err != nil {
		return "", nil, err
	}
	where := `scope_key=? AND (?<=0 OR id<?)`
	args := []any{scope.Key(), opts.Cursor, opts.Cursor}
	if opts.SessionID != "" {
		where += ` AND conversation=? AND thread=?`
		args = append(args, conversation, thread)
	}
	if !opts.Since.IsZero() {
		where += ` AND created_at>=?`
		args = append(args, opts.Since.UnixMilli())
	}
	if !opts.Until.IsZero() {
		where += ` AND created_at<?`
		args = append(args, opts.Until.UnixMilli())
	}
	if opts.Query != "" {
		where += ` AND (user_text LIKE ? ESCAPE '\' OR assistant_text LIKE ? ESCAPE '\')`
		pattern := "%" + escapeLike(opts.Query) + "%"
		args = append(args, pattern, pattern)
	}
	return where, args, nil
}

// BrowseHistory 列出授权原始记录；时间和会话条件在 SQL 召回前生效，不对跨空间结果事后过滤。
func (s *Service) BrowseHistory(ctx context.Context, identity chat.Identity, opts HistoryBrowseOptions) (HistorySearchResult, error) {
	scope, err := space(identity)
	if err != nil {
		return HistorySearchResult{}, err
	}
	where, args, err := historyWindowArgs(scope, opts)
	if err != nil {
		return HistorySearchResult{}, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	args = append(args, limit+1)
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+historyColumns+` FROM history_projections WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return HistorySearchResult{}, err
	}
	defer func() { _ = rows.Close() }()
	var records []HistoryRecord
	for rows.Next() {
		r, err := scanHistory(rows)
		if err != nil {
			return HistorySearchResult{}, err
		}
		records = append(records, r)
	}
	if err = rows.Err(); err != nil {
		return HistorySearchResult{}, err
	}
	res := HistorySearchResult{Scope: scope, Query: opts.Query, Records: records}
	if len(records) > limit {
		res.HasMore = true
		res.NextCursor = records[limit-1].ID
		res.Records = records[:limit]
	}
	data, _ := json.Marshal(struct {
		Scope, Session, Query string
		Since, Until          int64
		Cursor                int64
		Limit                 int
	}{scope.Key(), opts.SessionID, opts.Query, opts.Since.UnixMilli(), opts.Until.UnixMilli(), opts.Cursor, limit})
	sum := sha256.Sum256(data)
	res.QueryHash = hex.EncodeToString(sum[:])
	return res, nil
}

// BrowseHistorySessions 返回真实会话目录和稳定记录水位，不能用于查询其他身份的会话。
func (s *Service) BrowseHistorySessions(ctx context.Context, identity chat.Identity, opts HistoryBrowseOptions) ([]HistorySession, int64, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, 0, err
	}
	cursor := opts.Cursor
	opts.Cursor = 0
	where, args, err := historyWindowArgs(scope, opts)
	if err != nil {
		return nil, 0, err
	}
	if cursor < 0 {
		return nil, 0, errors.New("历史游标不得为负")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	args = append(args, cursor, cursor, limit+1)
	rows, err := s.store.db.QueryContext(ctx, `SELECT conversation,thread,count(*),min(created_at),max(created_at),max(id) FROM history_projections WHERE `+where+` GROUP BY conversation,thread HAVING (?<=0 OR max(id)<?) ORDER BY max(id) DESC LIMIT ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var out []HistorySession
	for rows.Next() {
		var item HistorySession
		var first, last int64
		if err := rows.Scan(&item.Conversation, &item.Thread, &item.Count, &first, &last, &item.LastRecordID); err != nil {
			return nil, 0, err
		}
		item.ID = historySessionID(item.Conversation, item.Thread)
		item.FirstAt = time.UnixMilli(first)
		item.LastAt = time.UnixMilli(last)
		out = append(out, item)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	var nextCursor int64
	if len(out) > limit {
		nextCursor = out[limit-1].LastRecordID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

// ReadHistoryOriginal 按字符分页恢复单条记录原文，绝不把历史工具调用当作待执行副作用。
func (s *Service) ReadHistoryOriginal(ctx context.Context, identity chat.Identity, id int64, role string, offset, limit int) (map[string]any, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, err
	}
	if id <= 0 || offset < 0 || limit < 0 {
		return nil, errors.New("历史 id 必须正数，offset/limit 不得为负")
	}
	r, err := scanHistory(s.store.db.QueryRowContext(ctx, `SELECT `+historyColumns+` FROM history_projections WHERE id=? AND scope_key=?`, id, scope.Key()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var text string
	switch role {
	case "user":
		text = r.UserText
	case "assistant":
		text = r.AssistantText
	default:
		return nil, errors.New("原文 role 必须 user 或 assistant")
	}
	if limit == 0 {
		limit = 2000
	}
	if limit > 8000 {
		limit = 8000
	}
	count := utf8.RuneCountInString(text)
	if offset > count {
		offset = count
	}
	end := min(count, offset+limit)
	runes := []rune(text)
	return map[string]any{"id": id, "task_id": r.TaskID, "session_id": historySessionID(r.Conversation, r.Thread), "role": role, "content": string(runes[offset:end]), "offset": offset, "next_offset": end, "total_chars": count, "has_more": end < count, "status": r.TaskStatus}, nil
}

// RestoreHistoryMessages 恢复指定已授权历史会话的原始 user/assistant 消息，不重新派发旧工具。
func (s *Service) RestoreHistoryMessages(ctx context.Context, identity chat.Identity, sessionID string) ([]HistoryRecord, error) {
	if sessionID == "" {
		return nil, errors.New("恢复需要 session_id")
	}
	opts := HistoryBrowseOptions{SessionID: sessionID, Limit: 20}
	var out []HistoryRecord
	total := 0
	for {
		res, err := s.BrowseHistory(ctx, identity, opts)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Records {
			total += len(r.UserText) + len(r.AssistantText)
			if total > 4<<20 {
				return nil, fmt.Errorf("恢复历史原文超过 4 MiB，请用 session/raw 分页恢复")
			}
			out = append(out, r)
		}
		if !res.HasMore {
			break
		}
		opts.Cursor = res.NextCursor
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
