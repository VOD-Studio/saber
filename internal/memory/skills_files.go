package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"rua.plus/saber/internal/chat"
)

// SkillFileName 是技能目录内约定的入口文件名。
const SkillFileName = "SKILL.md"

// MaxSkillFileBytes 是单个 SKILL.md 允许的最大字节数（256 KiB）。
const MaxSkillFileBytes = 256 << 10

// skillFileSourcePrefix 标记由文件导入的技能，附加在 Source.Message 中用于审计。
const skillFileSourcePrefix = "file:"

var (
	// ErrSkillFileTooLarge 表示技能文件超过 256 KiB 上限。
	ErrSkillFileTooLarge = errors.New("技能文件超过 256 KiB 上限")
	// ErrSkillFileFrontmatter 表示技能文件缺少或包含无效的 YAML frontmatter。
	ErrSkillFileFrontmatter = errors.New("技能文件的 YAML frontmatter 无效")
	// ErrSkillFileBody 表示技能文件正文为空。
	ErrSkillFileBody = errors.New("技能文件正文为空")
	// ErrSkillFileEncoding 表示技能文件不是有效的 UTF-8 文本。
	ErrSkillFileEncoding = errors.New("技能文件必须是 UTF-8 编码")
)

// SkillFile 是从 SKILL.md 解析出的技能定义。
type SkillFile struct {
	// Name 是 frontmatter 中的技能标识，必须与所在目录名一致。
	Name string
	// Description 是技能适用场景描述，同时用于目录展示与模型路由。
	Description string
	// Content 是 frontmatter 之后的 Markdown 指令正文。
	Content string
	// Path 是用于展示与审计的相对路径（如 deploy/SKILL.md），不含绝对路径。
	Path string
	// Metadata 保留完整 frontmatter，包含许可证、平台与 provider 配置声明。
	Metadata map[string]any
}

// SkillFileListing 描述外部只读目录中的一个可用技能。
type SkillFileListing struct {
	// Dir 是配置的技能根目录。
	Dir string
	// Name 是技能标识。
	Name string
	// Description 是技能描述。
	Description string
	// RelPath 是相对根目录的 SKILL.md 路径，用于展示与审计。
	RelPath string
}

// SkillImportResult 汇总一次 SKILL.md 导入的结果。
type SkillImportResult struct {
	// Imported 是新增生效的技能数。
	Imported int
	// Updated 是更新生效的技能数。
	Updated int
	// Skipped 是内容完全一致、无需写入的数量。
	Skipped int
	// Suggested 是转为待确认建议、尚未生效的数量。
	Suggested int
	// Errors 记录单个文件或目录的失败原因，不中断整批导入。
	Errors []error
}

// ParseSkillFile 解析 SKILL.md 内容，path 只用于错误信息，必须是不含隐私的展示名。
//
// 支持的格式为 agentskills.io 兼容的技能文件：以 `---` 包裹的 YAML frontmatter
// （至少包含 name 与 description）加上 Markdown 指令正文。解析前统一去除 UTF-8 BOM，
// 并把 CRLF、CR 归一为 LF，因此 Windows 换行不会影响 frontmatter 边界识别。
func ParseSkillFile(data []byte, path string) (SkillFile, error) {
	display := skillDisplayPath(path)
	if len(data) > MaxSkillFileBytes {
		return SkillFile{}, fmt.Errorf("%s: %w", display, ErrSkillFileTooLarge)
	}
	if !utf8.Valid(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))) {
		return SkillFile{}, fmt.Errorf("%s: %w", display, ErrSkillFileEncoding)
	}
	meta, body, ok := splitSkillFrontmatter(normalizeSkillText(data))
	if !ok {
		return SkillFile{}, fmt.Errorf("%s: %w（需要以 --- 开头的 YAML 头）", display, ErrSkillFileFrontmatter)
	}
	name, description, err := parseSkillMetadata(meta)
	if err != nil {
		return SkillFile{}, fmt.Errorf("%s: %w", display, err)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return SkillFile{}, fmt.Errorf("%s: %w", display, ErrSkillFileBody)
	}
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(meta), &metadata); err != nil {
		return SkillFile{}, fmt.Errorf("%s: %w", display, ErrSkillFileFrontmatter)
	}
	return SkillFile{Name: name, Description: description, Content: body, Path: filepath.ToSlash(display), Metadata: metadata}, nil
}

// skillDisplayPath 只保留用于展示的相对路径，避免错误信息泄露绝对路径。
func skillDisplayPath(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(filepath.FromSlash(clean)) {
		return filepath.Base(clean)
	}
	for strings.HasPrefix(clean, "../") {
		clean = strings.TrimPrefix(clean, "../")
	}
	return clean
}

// normalizeSkillText 去除 UTF-8 BOM 并把所有换行统一为 LF。
func normalizeSkillText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// splitSkillFrontmatter 以独立的 --- 行切分 YAML 头与正文。
func splitSkillFrontmatter(text string) (meta, body string, ok bool) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
	}
	return "", "", false
}

// parseSkillMetadata 读取 frontmatter 中的 name 与 description，并拒绝重复键。
//
// 其他键（如 license、metadata）按 agentskills.io 约定保留但不解释，便于复用现成技能文件。
func parseSkillMetadata(meta string) (name, description string, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(meta), &doc); err != nil {
		return "", "", fmt.Errorf("%w：%v", ErrSkillFileFrontmatter, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return "", "", fmt.Errorf("%w：缺少键值对", ErrSkillFileFrontmatter)
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return "", "", fmt.Errorf("%w：必须是键值映射", ErrSkillFileFrontmatter)
	}
	values := make(map[string]string, 2)
	seen := make(map[string]bool)
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode, valueNode := root.Content[i], root.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			return "", "", fmt.Errorf("%w：键必须是字符串", ErrSkillFileFrontmatter)
		}
		key := keyNode.Value
		if seen[key] {
			return "", "", fmt.Errorf("%w：重复键 %s", ErrSkillFileFrontmatter, key)
		}
		seen[key] = true
		if key != "name" && key != "description" {
			continue
		}
		var value string
		if err := valueNode.Decode(&value); err != nil {
			return "", "", fmt.Errorf("%w：%s 必须是字符串", ErrSkillFileFrontmatter, key)
		}
		values[key] = value
	}
	name = strings.TrimSpace(values["name"])
	description = strings.TrimSpace(values["description"])
	if name == "" || validateSkillName(name) != nil {
		return "", "", fmt.Errorf("%w：name 必须由 1-64 位小写字母、数字、连字符、下划线或点组成", ErrSkillFileFrontmatter)
	}
	if description == "" {
		return "", "", fmt.Errorf("%w：description 不能为空", ErrSkillFileFrontmatter)
	}
	return name, description, nil
}

// skillFileSource 是待导入的技能文件及其根目录。
type skillFileSource struct {
	dir  string
	file SkillFile
}

// ListSkillFiles 列出各外部目录中直接子目录下的可用技能，不写记忆数据库。
//
// 只扫描一层：每个 `<dir>/<name>/SKILL.md` 是一个技能；符号链接一律跳过，
// 目录名或 frontmatter 名不合法的子目录忽略，单个文件失败只记录错误而不影响其他目录。
func ListSkillFiles(dirs []string) ([]SkillFileListing, []error) {
	files, errs := loadSkillFiles(dirs)
	listings := make([]SkillFileListing, 0, len(files))
	for _, src := range files {
		listings = append(listings, SkillFileListing{Dir: src.dir, Name: src.file.Name, Description: src.file.Description, RelPath: src.file.Path})
	}
	return listings, errs
}

// SyncExternalSkills 把多个外部目录中的技能导入当前空间，内容相同则跳过。
//
// 群共享空间无写权限时沿用待确认建议语义，导入不会直接生效。
func (s *Service) SyncExternalSkills(ctx context.Context, identity chat.Identity, dirs []string) (SkillImportResult, error) {
	if _, err := space(identity); err != nil {
		return SkillImportResult{}, err
	}
	files, errs := loadSkillFiles(dirs)
	result := SkillImportResult{Errors: errs}
	for _, src := range files {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		s.importSkillFile(ctx, identity, src, &result)
	}
	return result, nil
}

// ImportSkills 导入单个技能根目录下直接子目录中的 SKILL.md。
//
// 目录必须是已存在的绝对路径；导入逐文件进行，失败不会中断整批。
func (s *Service) ImportSkills(ctx context.Context, identity chat.Identity, root string) (SkillImportResult, error) {
	if err := validateSkillRoot(root); err != nil {
		return SkillImportResult{}, err
	}
	return s.SyncExternalSkills(ctx, identity, []string{filepath.Clean(strings.TrimSpace(root))})
}

// ExportSkill 把当前空间的技能写为 `<dir>/<name>/SKILL.md`。
//
// 文件以 0600 创建、目录以 0700 创建，并通过同目录临时文件加 rename 原子替换。
// 名称先经 validateSkillName 校验，路径再与导出目录做包含性检查，拒绝 `..` 穿越。
func (s *Service) ExportSkill(ctx context.Context, identity chat.Identity, name, dir string) (string, error) {
	if err := validateSkillName(name); err != nil {
		return "", err
	}
	root := filepath.Clean(strings.TrimSpace(dir))
	if root == "" || root == "." || !filepath.IsAbs(root) {
		return "", errors.New("导出目录必须是绝对路径")
	}
	if hasParentTraversal(dir) {
		return "", errors.New("导出目录不能包含 .. 路径段")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", errors.New("导出目录不存在或不可访问")
	}
	if !info.IsDir() {
		return "", errors.New("导出目录不是目录")
	}
	target := filepath.Join(root, name)
	if rel, err := filepath.Rel(root, target); err != nil || rel != name {
		return "", errors.New("非法的技能导出路径")
	}
	pkg, err := s.GetSkillPackage(ctx, identity, name)
	if err != nil {
		return "", err
	}
	return exportSkillPackage(pkg, root)
}

// hasParentTraversal 判断路径是否包含 `..` 路径段，避免从导出目录向上穿越。
func hasParentTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// validateSkillRoot 校验导入根目录：必须是存在的绝对路径目录。
func validateSkillRoot(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." || !filepath.IsAbs(root) {
		return errors.New("技能目录必须是绝对路径")
	}
	info, err := os.Stat(root)
	if err != nil {
		return errors.New("技能目录不存在或不可访问")
	}
	if !info.IsDir() {
		return errors.New("技能目录不是目录")
	}
	return nil
}

// loadSkillFiles 读取并解析所有外部目录中的技能文件。
func loadSkillFiles(dirs []string) ([]skillFileSource, []error) {
	var files []skillFileSource
	var errs []error
	seen := make(map[string]bool)
	for _, raw := range dirs {
		dir := filepath.Clean(strings.TrimSpace(raw))
		if err := validateSkillRoot(dir); err != nil {
			errs = append(errs, err)
			continue
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			errs = append(errs, errors.New("无法读取技能目录"))
			continue
		}
		walkErr := fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, err error) error {
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: 无法读取技能目录", rel))
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if !entry.IsDir() {
				return nil
			}
			if rel == "." {
				return nil
			}
			name := entry.Name()
			if validateSkillName(name) != nil || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			info, err := root.Lstat(filepath.Join(rel, SkillFileName))
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil || !info.Mode().IsRegular() {
				errs = append(errs, fmt.Errorf("%s: 技能入口不可读取", rel))
				return fs.SkipDir
			}
			data, err := readPackageRootFile(root, filepath.Join(rel, SkillFileName))
			if err != nil {
				errs = append(errs, err)
				return fs.SkipDir
			}
			file, err := ParseSkillFile(data, filepath.Join(rel, SkillFileName))
			if err != nil {
				errs = append(errs, err)
				return fs.SkipDir
			}
			if file.Name != name {
				errs = append(errs, fmt.Errorf("%s/SKILL.md: frontmatter 名称与目录名不一致", rel))
				return fs.SkipDir
			}
			if !seen[file.Name] {
				files = append(files, skillFileSource{dir: dir, file: file})
				seen[file.Name] = true
			}
			return fs.SkipDir
		})
		if walkErr != nil {
			errs = append(errs, walkErr)
		}
		if err := root.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return files, errs
}

// importSkillFile 导入单个技能文件：不存在则新增，内容不同则替换，相同则跳过。
func (s *Service) importSkillFile(ctx context.Context, identity chat.Identity, src skillFileSource, result *SkillImportResult) {
	pkg, err := ReadSkillPackageDirectory(ctx, filepath.Join(src.dir, filepath.Dir(src.file.Path)))
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("%s: %w", src.file.Path, err))
		return
	}
	digest, err := skillPackageDigest(pkg)
	if err != nil {
		result.Errors = append(result.Errors, err)
		return
	}
	pkg.Provenance = SkillProvenance{Source: skillFileSourcePrefix + src.file.Path, Digest: digest, InstalledAt: time.Now(), Scanner: skillScannerVersion}
	current, err := s.GetSkillPackage(ctx, identity, pkg.Name)
	var version int64
	if err == nil {
		version = current.Version
		oldDigest, hashErr := skillPackageDigest(current)
		if hashErr != nil {
			result.Errors = append(result.Errors, hashErr)
			return
		}
		if oldDigest == digest {
			result.Skipped++
			return
		}
	} else if !errors.Is(err, ErrSkillNotFound) {
		result.Errors = append(result.Errors, err)
		return
	}
	mut, err := s.PutSkillPackage(ctx, identity, pkg, version, Source{Message: pkg.Provenance.Source})
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("%s: %w", src.file.Path, err))
		return
	}
	if mut.Suggested {
		result.Suggested++
	} else if mut.Duplicate {
		result.Skipped++
	} else if version > 0 {
		result.Updated++
	} else {
		result.Imported++
	}
}
