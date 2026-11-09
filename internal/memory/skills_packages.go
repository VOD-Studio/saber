package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"rua.plus/saber/internal/chat"
)

// MaxSkillPackageBytes 是单个技能包允许的总字节数。
const MaxSkillPackageBytes = 5 << 20

// MaxSkillPackageFiles 是单个技能包允许的文件数；文件只存内容，不保留来源执行权限。
const MaxSkillPackageFiles = 128

// SkillProvenance 记录精确来源、配置 pin、扫描和原始内容摘要。
type SkillProvenance struct {
	Source      string         `json:"source"`
	Ref         string         `json:"ref,omitempty"`
	Digest      string         `json:"digest"`
	Builtin     bool           `json:"builtin,omitempty"`
	InstalledAt time.Time      `json:"installed_at"`
	Scanner     string         `json:"scanner"`
	Findings    []SkillFinding `json:"findings,omitempty"`
}

// SkillPackage 持久化完整 metadata 和资源；SKILL.md 由条目正文及 metadata 对称生成。
type SkillPackage struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Content     string            `json:"content"`
	Metadata    map[string]any    `json:"metadata"`
	Files       map[string][]byte `json:"files"`
	Provenance  SkillProvenance   `json:"provenance"`
	Version     int64             `json:"version"`
}

// SkillUse 是当前空间技能读取次数与最后使用时间，供 curator 避免清理活跃技能。
type SkillUse struct {
	Name     string    `json:"name"`
	Count    int64     `json:"count"`
	LastUsed time.Time `json:"last_used"`
}

func safeSkillResource(p string) error {
	if p == "" || p == SkillFileName || strings.ContainsAny(p, "\\\x00") || path.IsAbs(p) || path.Clean(p) != p {
		return errors.New("技能资源路径无效")
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return errors.New("附属文件必须位于 references/templates/scripts/examples/assets 目录")
	}
	switch parts[0] {
	case "references", "templates", "scripts", "examples", "assets":
	default:
		return errors.New("技能资源目录不在授权范围")
	}
	for _, part := range parts {
		if part == ".." || strings.HasPrefix(part, ".") {
			return errors.New("技能资源不能含隐藏或父级路径")
		}
	}
	return nil
}

func validateSkillPackage(p SkillPackage) error {
	if err := validateSkillMutation(skillMutationInput{Action: ActionAdd, Name: p.Name, Description: p.Description, Content: p.Content}); err != nil {
		return err
	}
	data, err := renderSkillPackage(p)
	if err != nil {
		return err
	}
	if len(data) > MaxSkillFileBytes {
		return ErrSkillFileTooLarge
	}
	total := len(data)
	if len(p.Files) > MaxSkillPackageFiles {
		return errors.New("技能包文件数量超限")
	}
	for name, data := range p.Files {
		if err := safeSkillResource(name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if len(data) > MaxSkillFileBytes {
			return fmt.Errorf("%s: %w", name, ErrSkillFileTooLarge)
		}
		total += len(data)
	}
	if total > MaxSkillPackageBytes {
		return errors.New("技能包总大小超过 5 MiB")
	}
	return nil
}

func renderSkillPackage(p SkillPackage) ([]byte, error) {
	meta := make(map[string]any, len(p.Metadata)+2)
	for k, v := range p.Metadata {
		meta[k] = v
	}
	meta["name"] = p.Name
	meta["description"] = p.Description
	data, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(data) + "---\n\n" + strings.TrimSpace(p.Content) + "\n"), nil
}

func skillPackageDigest(p SkillPackage) (string, error) {
	entry, err := renderSkillPackage(p)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(p.Files)+1)
	names = append(names, SkillFileName)
	for n := range p.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		data := p.Files[n]
		if n == SkillFileName {
			data = entry
		}
		_, _ = fmt.Fprintf(h, "%d:%s:%d:", len(n), n, len(data))
		if _, err := h.Write(data); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReadSkillPackageDirectory 在 os.Root 内读取完整技能目录，拒绝符号链接、设备及越界资源。
func ReadSkillPackageDirectory(ctx context.Context, dir string) (SkillPackage, error) {
	if !filepath.IsAbs(dir) {
		return SkillPackage{}, errors.New("技能包目录必须绝对路径")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return SkillPackage{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return SkillPackage{}, errors.New("技能包必须真实目录")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return SkillPackage{}, err
	}
	defer func() { _ = root.Close() }()
	entry, err := readPackageRootFile(root, SkillFileName)
	if err != nil {
		return SkillPackage{}, err
	}
	file, err := ParseSkillFile(entry, SkillFileName)
	if err != nil {
		return SkillPackage{}, err
	}
	pkg := SkillPackage{Name: file.Name, Description: file.Description, Content: file.Content, Metadata: file.Metadata, Files: make(map[string][]byte)}
	err = fs.WalkDir(root.FS(), ".", func(n string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if n == "." || n == SkillFileName {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: 技能包禁止符号链接", n)
		}
		if d.IsDir() {
			first := strings.Split(n, "/")[0]
			switch first {
			case "references", "templates", "scripts", "examples", "assets":
				return nil
			default:
				return fs.SkipDir
			}
		}
		if err := safeSkillResource(n); err != nil {
			return err
		}
		data, err := readPackageRootFile(root, n)
		if err != nil {
			return err
		}
		pkg.Files[n] = data
		return validateSkillPackage(pkg)
	})
	if err != nil {
		return SkillPackage{}, err
	}
	if err = validateSkillPackage(pkg); err != nil {
		return SkillPackage{}, err
	}
	return pkg, nil
}

func readPackageRootFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("技能包文件必须普通文件")
	}
	if info.Size() > MaxSkillFileBytes {
		return nil, ErrSkillFileTooLarge
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSkillFileBytes+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSkillFileBytes {
		return nil, ErrSkillFileTooLarge
	}
	return data, nil
}

// PutSkillPackage 原子导入 metadata、附属资源及正文；更新必须提供当前版本，群无权限生成完整包建议。
func (s *Service) PutSkillPackage(ctx context.Context, identity chat.Identity, pkg SkillPackage, expectedVersion int64, source Source) (SkillMutation, error) {
	if err := validateSkillPackage(pkg); err != nil {
		return SkillMutation{}, err
	}
	scope, err := space(identity)
	if err != nil {
		return SkillMutation{}, err
	}
	findings, err := ScanSkillPackage(pkg)
	if err != nil {
		return SkillMutation{}, err
	}
	for _, finding := range findings {
		if finding.Severity == "critical" {
			return SkillMutation{}, fmt.Errorf("技能扫描拒绝 %s:%d %s", finding.File, finding.Line, finding.Rule)
		}
	}
	action := ActionAdd
	if expectedVersion > 0 {
		action = ActionReplace
	}
	return s.writeSkill(ctx, identity, scope, skillMutationInput{Action: action, Name: pkg.Name, Description: pkg.Description, Content: pkg.Content, ExpectedVersion: expectedVersion, Source: source, Package: &pkg})
}

// GetSkillPackage 只从当前空间读取完整包，不暴露来源绝对目录；普通条目同样可导出为完整包。
func (s *Service) GetSkillPackage(ctx context.Context, identity chat.Identity, name string) (SkillPackage, error) {
	scope, err := space(identity)
	if err != nil {
		return SkillPackage{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SkillPackage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := querySkillByName(ctx, tx, scope, name)
	if err != nil {
		return SkillPackage{}, err
	}
	pkg, err := loadSkillPackageTx(ctx, tx, scope, name, false, 0)
	if err != nil {
		return SkillPackage{}, err
	}
	if pkg == nil {
		pkg = &SkillPackage{Metadata: make(map[string]any), Files: make(map[string][]byte)}
	}
	pkg.Name = entry.Name
	pkg.Description = entry.Description
	pkg.Content = entry.Content
	pkg.Version = entry.Version
	return *pkg, tx.Commit()
}

func loadSkillPackageTx(ctx context.Context, tx *sql.Tx, scope Scope, name string, pending bool, id int64) (*SkillPackage, error) {
	var data []byte
	var err error
	if pending {
		err = tx.QueryRowContext(ctx, `SELECT data FROM skill_package_changes WHERE change_id=? AND scope_key=?`, id, scope.Key()).Scan(&data)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT data FROM skill_packages WHERE scope_key=? AND name=?`, scope.Key(), name).Scan(&data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p SkillPackage
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func saveSkillPackageTx(ctx context.Context, tx *sql.Tx, scope Scope, in skillMutationInput) error {
	if in.Action == ActionRemove {
		_, err := tx.ExecContext(ctx, `DELETE FROM skill_packages WHERE scope_key=? AND name=?`, scope.Key(), in.Name)
		return err
	}
	if in.Package == nil {
		return nil
	}
	data, err := json.Marshal(in.Package)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO skill_packages(scope_key,name,data) VALUES(?,?,?) ON CONFLICT(scope_key,name) DO UPDATE SET data=excluded.data`, scope.Key(), in.Name, data)
	return err
}

func sameSkillPackageTx(ctx context.Context, tx *sql.Tx, scope Scope, in skillMutationInput) (bool, error) {
	if in.Package == nil {
		return true, nil
	}
	old, err := loadSkillPackageTx(ctx, tx, scope, in.Name, false, 0)
	if err != nil {
		return false, err
	}
	if old == nil {
		return false, nil
	}
	a, err := skillPackageDigest(*old)
	if err != nil {
		return false, err
	}
	b, err := skillPackageDigest(*in.Package)
	return a == b, err
}

// ReadSkillResource 按版本读取当前空间的附属文本文件，不执行脚本或读取原始目录。
func (s *Service) ReadSkillResource(ctx context.Context, identity chat.Identity, name, resource string, offset, limit int) (map[string]any, error) {
	if err := safeSkillResource(resource); err != nil {
		return nil, err
	}
	pkg, err := s.GetSkillPackage(ctx, identity, name)
	if err != nil {
		return nil, err
	}
	data, ok := pkg.Files[resource]
	if !ok {
		return nil, ErrNotFound
	}
	if !utf8.Valid(data) {
		return nil, errors.New("该附属文件不是 UTF-8 文本；请使用 export 导出完整包")
	}
	if offset < 0 || limit < 0 {
		return nil, errors.New("offset 与 limit 不得为负")
	}
	if limit == 0 || limit > 200 {
		limit = 200
	}
	lines := strings.Split(string(data), "\n")
	if offset > len(lines) {
		offset = len(lines)
	}
	end := min(len(lines), offset+limit)
	if err := s.recordSkillUse(ctx, identity, name); err != nil {
		return nil, err
	}
	return map[string]any{"name": name, "path": resource, "version": pkg.Version, "content": strings.Join(lines[offset:end], "\n"), "offset": offset, "next_offset": end, "has_more": end < len(lines)}, nil
}

func (s *Service) recordSkillUse(ctx context.Context, identity chat.Identity, name string) error {
	scope, err := space(identity)
	if err != nil {
		return err
	}
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO skill_usage(scope_key,name,use_count,last_used) VALUES(?,?,1,?) ON CONFLICT(scope_key,name) DO UPDATE SET use_count=use_count+1,last_used=excluded.last_used`, scope.Key(), name, nowMillis())
	return err
}

// SkillUsage 返回当前空间的真实读取记录。
func (s *Service) SkillUsage(ctx context.Context, identity chat.Identity) ([]SkillUse, error) {
	scope, err := space(identity)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT name,use_count,last_used FROM skill_usage WHERE scope_key=? ORDER BY name`, scope.Key())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []SkillUse
	for rows.Next() {
		var u SkillUse
		var ms int64
		if err := rows.Scan(&u.Name, &u.Count, &ms); err != nil {
			return nil, err
		}
		u.LastUsed = time.UnixMilli(ms)
		out = append(out, u)
	}
	return out, rows.Err()
}

func exportSkillPackage(pkg SkillPackage, dir string) (string, error) {
	if err := validateSkillPackage(pkg); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if info, err := root.Lstat(pkg.Name); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("导出目标不能符号链接")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	stage := ".skill-export-" + hex.EncodeToString(random[:])
	if err := root.Mkdir(stage, 0700); err != nil {
		return "", err
	}
	defer func() { _ = root.RemoveAll(stage) }()
	target, err := root.OpenRoot(stage)
	if err != nil {
		return "", err
	}
	entry, err := renderSkillPackage(pkg)
	if err != nil {
		_ = target.Close()
		return "", err
	}
	write := func(n string, data []byte) error {
		if n != SkillFileName {
			base := ""
			for _, part := range strings.Split(path.Dir(n), "/") {
				base = path.Join(base, part)
				if err := target.Mkdir(base, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
					return err
				}
			}
		}
		f, err := target.OpenFile(n, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return errors.Join(err, f.Close())
	}
	if err = write(SkillFileName, entry); err == nil {
		for n, data := range pkg.Files {
			if err = write(n, data); err != nil {
				break
			}
		}
	}
	err = errors.Join(err, target.Close())
	if err != nil {
		return "", err
	}
	backup := stage + "-previous"
	hadPrevious := false
	if _, err := root.Lstat(pkg.Name); err == nil {
		if err = root.Rename(pkg.Name, backup); err != nil {
			return "", err
		}
		hadPrevious = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err = root.Rename(stage, pkg.Name); err != nil {
		if hadPrevious {
			err = errors.Join(err, root.Rename(backup, pkg.Name))
		}
		return "", err
	}
	if hadPrevious {
		if err = root.RemoveAll(backup); err != nil {
			return "", fmt.Errorf("技能已导出但旧版本清理失败: %w", err)
		}
	}
	return filepath.Join(dir, pkg.Name, SkillFileName), nil
}
