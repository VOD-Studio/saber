package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"rua.plus/saber/internal/chat"
)

// Profile 读取当前可信空间的画像并判定新鲜度。
//
// 归属由身份推导，调用方无法指定其他用户或群的作用域；
// 条目最大 updated_at 与画像记录的来源版本不一致时视为过期。
func (s *Service) Profile(ctx context.Context, identity chat.Identity) (Profile, error) {
	scope, err := space(identity)
	if err != nil {
		return Profile{}, err
	}
	return s.ProfileState(ctx, scope)
}

// ProfileState 按已确认作用域读取画像，供后台刷新等不接受外部身份参数的路径使用。
func (s *Service) ProfileState(ctx context.Context, scope Scope) (Profile, error) {
	if err := scope.Validate(); err != nil {
		return Profile{}, err
	}
	version, err := s.profileVersion(ctx, scope)
	if err != nil {
		return Profile{}, err
	}
	profile := Profile{Scope: scope, Version: version, Stale: true}
	var content, sourceVersion string
	var updated int64
	err = s.store.db.QueryRowContext(ctx, `
		SELECT content, source_version, updated_at FROM memory_profiles WHERE scope_key=?
	`, scope.Key()).Scan(&content, &sourceVersion, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, nil
	}
	if err != nil {
		return Profile{}, err
	}
	profile.Exists = true
	profile.Content = content
	profile.SourceVersion = sourceVersion
	profile.UpdatedAt = time.UnixMilli(updated)
	profile.Stale = sourceVersion != version
	return profile, nil
}

// ProfileSource 收集当前空间的画像生成输入：明确保存的条目优先，最多 50 条、总 8 KiB。
//
// 只读取该作用域自身条目，因此群画像不包含成员私聊内容，私聊画像也不包含群条目。
func (s *Service) ProfileSource(ctx context.Context, scope Scope) (ProfileSource, error) {
	if err := scope.Validate(); err != nil {
		return ProfileSource{}, err
	}
	version, err := s.profileVersion(ctx, scope)
	if err != nil {
		return ProfileSource{}, err
	}
	entries, err := s.list(ctx, scope)
	if err != nil {
		return ProfileSource{}, err
	}
	src := ProfileSource{Scope: scope, Version: version}
	var b strings.Builder
	for i, entry := range entries {
		if len(src.Entries) >= ProfileSourceEntries {
			src.Truncated = len(entries) - i
			break
		}
		line := "- " + entry.Content + "\n"
		if b.Len()+len(line) > ProfileSourceBytes {
			remaining := ProfileSourceBytes - b.Len()
			if remaining <= 0 {
				src.Truncated = len(entries) - i
				break
			}
			b.WriteString(truncateUTF8Bytes(line, remaining))
			src.Entries = append(src.Entries, entry)
			src.Truncated = len(entries) - i - 1
			break
		}
		b.WriteString(line)
		src.Entries = append(src.Entries, entry)
	}
	src.Text = b.String()
	return src, nil
}

// SaveProfile 写入生成好的画像，并记录生成时使用的条目版本。
//
// 画像正文按 MaxProfileRunes 截断，sourceVersion 为空时取当前条目版本。
// 返回写入后的画像状态，便于调用方直接回执或注入。
func (s *Service) SaveProfile(ctx context.Context, scope Scope, content, sourceVersion string) (Profile, error) {
	if err := scope.Validate(); err != nil {
		return Profile{}, err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return Profile{}, errors.New("画像内容不能为空")
	}
	content = truncateUTF8Runes(content, MaxProfileRunes)
	version := sourceVersion
	if version == "" {
		current, err := s.profileVersion(ctx, scope)
		if err != nil {
			return Profile{}, err
		}
		version = current
	}
	_, err := s.store.db.ExecContext(ctx, `
		INSERT INTO memory_profiles(scope_key, content, source_version, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(scope_key) DO UPDATE SET
			content = excluded.content,
			source_version = excluded.source_version,
			updated_at = excluded.updated_at
	`, scope.Key(), content, version, nowMillis())
	if err != nil {
		return Profile{}, err
	}
	return s.ProfileState(ctx, scope)
}

// CanWriteProfile 判定当前身份能否重建该空间画像，规则与记忆写入一致：
// 个人空间允许本人重建，群空间要求本群记忆管理员权限。
func (s *Service) CanWriteProfile(ctx context.Context, identity chat.Identity) (bool, error) {
	scope, err := space(identity)
	if err != nil {
		return false, err
	}
	return s.writeAllowed(identity, scope), nil
}

// profileVersion 返回空间内条目的版本：最大 updated_at 的毫秒值，空空间为 "0"。
func (s *Service) profileVersion(ctx context.Context, scope Scope) (string, error) {
	var latest int64
	err := s.store.db.QueryRowContext(ctx, `SELECT COALESCE(max(updated_at), 0) FROM memory_entries WHERE scope_key=?`, scope.Key()).Scan(&latest)
	if err != nil {
		return "", fmt.Errorf("读取记忆条目版本失败: %w", err)
	}
	return strconv.FormatInt(latest, 10), nil
}

// truncateUTF8Bytes 按字节截断字符串并保证不切断 UTF-8 字符。
func truncateUTF8Bytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// truncateUTF8Runes 按字符截断字符串。
func truncateUTF8Runes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
