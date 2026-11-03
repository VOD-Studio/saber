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
	return SkillFile{Name: name, Description: description, Content: body, Path: filepath.ToSlash(display)}, nil
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
	for i := 0; i+1 < len(root.Content); i += 2 {
		keyNode, valueNode := root.Content[i], root.Content[i+1]
		if keyNode.Kind != yaml.ScalarNode {
			return "", "", fmt.Errorf("%w：键必须是字符串", ErrSkillFileFrontmatter)
		}
		key := keyNode.Value
		if _, exists := values[key]; exists {
			return "", "", fmt.Errorf("%w：重复键 %s", ErrSkillFileFrontmatter, key)
		}
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
	skill, err := s.GetSkill(ctx, identity, name)
	if err != nil {
		return "", err
	}
	data, err := renderSkillFile(skill.Name, skill.Description, skill.Content)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", errors.New("创建技能导出目录失败")
	}
	path := filepath.Join(target, SkillFileName)
	if err := writeFileAtomic(path, data); err != nil {
		return "", fmt.Errorf("写入 %s 失败", SkillFileName)
	}
	return path, nil
}

// renderSkillFile 生成与导入解析器对称的 SKILL.md 内容。
func renderSkillFile(name, description, content string) ([]byte, error) {
	meta, err := yaml.Marshal(struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}{Name: name, Description: description})
	if err != nil {
		return nil, fmt.Errorf("生成技能元数据失败: %w", err)
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(meta)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(content))
	b.WriteString("\n")
	return b.Bytes(), nil
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

// writeFileAtomic 在同目录创建临时文件，写入完成后 rename 覆盖目标。
func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+SkillFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
	for _, raw := range dirs {
		dir := filepath.Clean(strings.TrimSpace(raw))
		if dir == "" || dir == "." || !filepath.IsAbs(dir) {
			errs = append(errs, errors.New("技能目录必须是绝对路径"))
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: 无法读取技能目录", filepath.Base(dir)))
			continue
		}
		for _, entry := range entries {
			// DirEntry.IsDir 不跟随符号链接，这里再显式跳过符号链接，避免越界读取。
			if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if validateSkillName(name) != nil {
				continue
			}
			file, err := readSkillDir(dir, name)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", filepath.ToSlash(filepath.Join(name, SkillFileName)), err))
				continue
			}
			files = append(files, skillFileSource{dir: dir, file: file})
		}
	}
	return files, errs
}

// readSkillDir 读取 `<root>/<name>/SKILL.md` 并校验符号链接、大小与名称一致性。
func readSkillDir(root, name string) (SkillFile, error) {
	rel := filepath.ToSlash(filepath.Join(name, SkillFileName))
	path := filepath.Join(root, name, SkillFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return SkillFile{}, fmt.Errorf("%s 不存在", SkillFileName)
		}
		return SkillFile{}, fmt.Errorf("无法读取 %s", SkillFileName)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return SkillFile{}, fmt.Errorf("%s 不能是符号链接", SkillFileName)
	}
	if !info.Mode().IsRegular() {
		return SkillFile{}, fmt.Errorf("%s 必须是普通文件", SkillFileName)
	}
	if info.Size() > MaxSkillFileBytes {
		return SkillFile{}, ErrSkillFileTooLarge
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillFile{}, fmt.Errorf("无法读取 %s", SkillFileName)
	}
	file, err := ParseSkillFile(data, rel)
	if err != nil {
		return SkillFile{}, err
	}
	if file.Name != name {
		return SkillFile{}, fmt.Errorf("frontmatter name %q 与目录名 %q 不一致", file.Name, name)
	}
	return file, nil
}

// importSkillFile 导入单个技能文件：不存在则新增，内容不同则替换，相同则跳过。
func (s *Service) importSkillFile(ctx context.Context, identity chat.Identity, src skillFileSource, result *SkillImportResult) {
	source := Source{Message: skillFileSourcePrefix + src.file.Path}
	current, err := s.GetSkill(ctx, identity, src.file.Name)
	switch {
	case err == nil:
		if current.Description == src.file.Description && current.Content == src.file.Content {
			result.Skipped++
			return
		}
		mut, err := s.ReplaceSkill(ctx, identity, src.file.Name, current.Version, src.file.Description, src.file.Content, source)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", src.file.Path, err))
			return
		}
		if mut.Suggested {
			result.Suggested++
			return
		}
		result.Updated++
	case errors.Is(err, ErrSkillNotFound):
		mut, err := s.AddSkill(ctx, identity, src.file.Name, src.file.Description, src.file.Content, source)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", src.file.Path, err))
			return
		}
		switch {
		case mut.Suggested:
			result.Suggested++
		case mut.Duplicate:
			result.Skipped++
		default:
			result.Imported++
		}
	default:
		result.Errors = append(result.Errors, fmt.Errorf("%s: %w", src.file.Path, err))
	}
}
