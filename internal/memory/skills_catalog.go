package memory

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

const skillScannerVersion = "saber-skill-guard-v1"

// SkillFinding 标记静态扫描的实际文件、行号、风险类型与严重程度。
type SkillFinding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
}

var skillThreats = []struct {
	rule, severity string
	pattern        *regexp.Regexp
}{
	{"instruction_override", "critical", regexp.MustCompile(`(?i)(ignore|disregard)\s+.*(previous|prior|above|all|your)\s+.*(instructions|rules)|system\s+prompt\s+override`)},
	{"secret_exfiltration", "critical", regexp.MustCompile(`(?i)(curl|wget|fetch|requests\.(get|post)|httpx\.(get|post)).*(\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD)|process\.env|os\.environ)`)},
	{"secret_store_read", "critical", regexp.MustCompile(`(?i)(cat\s+[^>]|readFile|read_text|open\s*\().*(\.env|id_rsa|id_ed25519|\.netrc|\.pgpass|credentials)`)},
	{"download_execute", "high", regexp.MustCompile(`(?i)(curl|wget).*\|\s*(sudo\s+)?(ba)?sh\b`)},
	{"destructive_command", "high", regexp.MustCompile(`(?i)rm\s+(-[^\s]*[rf][^\s]*\s+)+(/|\$HOME)|mkfs\b|dd\s+.*of=/dev/`)},
	{"agent_config_persistence", "high", regexp.MustCompile(`(?i)(write|modify|append|replace|tee|sed\s+-i|>).*([.]hermes/(config|SOUL)|AGENTS[.]md|CLAUDE[.]md|[.]claude/settings|[.]codex/config)`)},
	{"concealed_execution", "high", regexp.MustCompile(`(?i)do\s+not\s+tell\s+the\s+user|base64.*(decode|exec|eval)|eval\s*\(`)},
	{"private_key", "critical", regexp.MustCompile(`-----BEGIN ([A-Z ]+ )?PRIVATE KEY-----`)},
}

// ScanSkillPackage 扫描完整包而非仅入口；安装 never 执行其中脚本。
func ScanSkillPackage(pkg SkillPackage) ([]SkillFinding, error) {
	if err := validateSkillPackage(pkg); err != nil {
		return nil, err
	}
	entry, err := renderSkillPackage(pkg)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(pkg.Files)+1)
	files[SkillFileName] = entry
	for n, d := range pkg.Files {
		files[n] = d
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var findings []SkillFinding
	for _, n := range names {
		switch strings.ToLower(path.Ext(n)) {
		case ".exe", ".dll", ".so", ".dylib", ".bin", ".com", ".msi", ".dmg", ".deb", ".rpm":
			findings = append(findings, SkillFinding{File: n, Rule: "binary_payload", Severity: "critical"})
		}
		for i, line := range strings.Split(string(files[n]), "\n") {
			for _, r := range skillThreats {
				if r.pattern.MatchString(line) {
					findings = append(findings, SkillFinding{File: n, Line: i + 1, Rule: r.rule, Severity: r.severity})
				}
			}
			for _, r := range line {
				if r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\u202a' || r == '\u202b' || r == '\u202d' || r == '\u202e' || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
					findings = append(findings, SkillFinding{File: n, Line: i + 1, Rule: "unicode_smuggling", Severity: "high"})
					break
				}
				if unicode.IsControl(r) && r != '\t' && r != '\r' {
					findings = append(findings, SkillFinding{File: n, Line: i + 1, Rule: "control_bytes", Severity: "high"})
					break
				}
			}
		}
	}
	return findings, nil
}

// SetSkillGuards 在初始化期绑定 curator 锁定和删除保护，所有模型/CLI/审批共用。
func (s *Service) SetSkillGuards(writable, deletable func(context.Context, string) error) {
	s.skillWritable = writable
	s.skillDeletable = deletable
}
func (s *Service) checkSkillGuard(ctx context.Context, in skillMutationInput) error {
	if in.Action == ActionRemove && s.skillDeletable != nil {
		return s.skillDeletable(ctx, in.Name)
	}
	if s.skillWritable != nil {
		return s.skillWritable(ctx, in.Name)
	}
	return nil
}

// SkillCatalog 返回管理员实际配置的可安装来源；不伪造远端 registry 查询结果。
func (s *Service) SkillCatalog(ctx context.Context, identity chat.Identity, query string) ([]map[string]any, error) {
	if _, err := space(identity); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(s.skillCatalog))
	for n := range s.skillCatalog {
		if strings.Contains(strings.ToLower(n), strings.ToLower(query)) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		src := s.skillCatalog[n]
		out = append(out, map[string]any{"identifier": n, "ref": src.Ref, "sha256": src.SHA256, "builtin": src.Builtin, "network": src.URL != ""})
	}
	return out, nil
}

// InspectCatalogSkill 读取配置 pin 对应的完整包并执行扫描，不改变已安装版本。
func (s *Service) InspectCatalogSkill(ctx context.Context, identity chat.Identity, identifier string) (SkillPackage, error) {
	if _, err := space(identity); err != nil {
		return SkillPackage{}, err
	}
	src, ok := s.skillCatalog[identifier]
	if !ok {
		return SkillPackage{}, errors.New("技能来源不在管理员 catalog 中")
	}
	var pkg SkillPackage
	var err error
	if src.Directory != "" {
		pkg, err = ReadSkillPackageDirectory(ctx, src.Directory)
	} else {
		if !s.skillAllowNetwork {
			return SkillPackage{}, errors.New("网络技能安装未授权配置")
		}
		pkg, err = downloadPinnedSkill(ctx, src)
	}
	if err != nil {
		return SkillPackage{}, err
	}
	digest, err := skillPackageDigest(pkg)
	if err != nil {
		return SkillPackage{}, err
	}
	if src.Directory != "" && src.SHA256 != "" && digest != strings.TrimPrefix(src.SHA256, "sha256:") {
		return SkillPackage{}, errors.New("本地技能内容与配置 SHA256 pin 不一致")
	}
	findings, err := ScanSkillPackage(pkg)
	if err != nil {
		return SkillPackage{}, err
	}
	pkg.Provenance = SkillProvenance{Source: identifier, Ref: src.Ref, Digest: digest, Builtin: src.Builtin, InstalledAt: time.Now(), Scanner: skillScannerVersion, Findings: findings}
	return pkg, nil
}

// InstallCatalogSkill 安装或更新配置来源；强制只允许覆盖用户修改，无法绕过 critical 扫描和 CAS。
func (s *Service) InstallCatalogSkill(ctx context.Context, identity chat.Identity, identifier string, update, force bool) (SkillMutation, error) {
	pkg, err := s.InspectCatalogSkill(ctx, identity, identifier)
	if err != nil {
		return SkillMutation{}, err
	}
	for _, f := range pkg.Provenance.Findings {
		if f.Severity == "critical" || f.Severity == "high" && !force {
			return SkillMutation{}, fmt.Errorf("技能扫描拒绝 %s:%d %s (%s)", f.File, f.Line, f.Rule, f.Severity)
		}
	}
	current, err := s.GetSkillPackage(ctx, identity, pkg.Name)
	var version int64
	if err == nil {
		if !update {
			return SkillMutation{}, ErrSkillDuplicate
		}
		if current.Provenance.Source != identifier {
			return SkillMutation{}, errors.New("更新来源与已安装技能不一致")
		}
		digest, err := skillPackageDigest(current)
		if err != nil {
			return SkillMutation{}, err
		}
		if digest != current.Provenance.Digest && !force {
			return SkillMutation{}, errors.New("技能已被用户修改；更新拒绝覆盖")
		}
		version = current.Version
	} else if !errors.Is(err, ErrSkillNotFound) {
		return SkillMutation{}, err
	} else if update {
		return SkillMutation{}, ErrSkillNotFound
	}
	return s.PutSkillPackage(ctx, identity, pkg, version, Source{Message: "catalog:" + identifier})
}

// CheckSkillUpdates 对比完整包 hash，清晰区分远端变化与用户编辑，不自动覆盖。
func (s *Service) CheckSkillUpdates(ctx context.Context, identity chat.Identity) ([]map[string]any, error) {
	skills, err := s.ListSkills(ctx, identity)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, sk := range skills {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := s.GetSkillPackage(ctx, identity, sk.Name)
		if err != nil {
			return nil, err
		}
		if _, ok := s.skillCatalog[current.Provenance.Source]; !ok {
			continue
		}
		item := map[string]any{"name": sk.Name, "identifier": current.Provenance.Source, "version": sk.Version}
		digest, err := skillPackageDigest(current)
		if err != nil {
			return nil, err
		}
		item["modified"] = digest != current.Provenance.Digest
		upstream, err := s.InspectCatalogSkill(ctx, identity, current.Provenance.Source)
		if err != nil {
			item["error"] = err.Error()
		} else {
			item["update_available"] = upstream.Provenance.Digest != current.Provenance.Digest
			item["digest"] = upstream.Provenance.Digest
		}
		out = append(out, item)
	}
	return out, nil
}

func downloadPinnedSkill(ctx context.Context, src config.SkillCatalogSource) (SkillPackage, error) {
	u, err := url.Parse(src.URL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return SkillPackage{}, errors.New("技能来源必须无凭据 HTTPS URL")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || req.URL.Scheme != "https" || req.URL.Host != u.Host {
			return errors.New("技能下载拒绝跨域或非 HTTPS 重定向")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return SkillPackage{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return SkillPackage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return SkillPackage{}, fmt.Errorf("技能下载 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxSkillPackageBytes+1))
	if err != nil {
		return SkillPackage{}, err
	}
	if len(data) > MaxSkillPackageBytes {
		return SkillPackage{}, errors.New("技能下载超过 5 MiB")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != strings.TrimPrefix(src.SHA256, "sha256:") {
		return SkillPackage{}, errors.New("技能下载 SHA256 pin 不匹配")
	}
	if strings.HasSuffix(u.Path, "SKILL.md") {
		file, err := ParseSkillFile(data, SkillFileName)
		if err != nil {
			return SkillPackage{}, err
		}
		return SkillPackage{Name: file.Name, Description: file.Description, Content: file.Content, Metadata: file.Metadata, Files: make(map[string][]byte)}, nil
	}
	return parseSkillArchive(ctx, data, src.Subdir)
}

func parseSkillArchive(ctx context.Context, data []byte, subdir string) (SkillPackage, error) {
	files := make(map[string][]byte)
	total := 0
	add := func(name string, data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
			return errors.New("技能归档路径越界")
		}
		if subdir != "" {
			prefix := strings.TrimSuffix(subdir, "/") + "/"
			if !strings.HasPrefix(name, prefix) {
				return nil
			}
			name = strings.TrimPrefix(name, prefix)
		}
		if name != SkillFileName {
			if err := safeSkillResource(name); err != nil {
				return err
			}
		}
		if _, ok := files[name]; ok {
			return errors.New("技能归档包含重复文件")
		}
		if len(data) > MaxSkillFileBytes {
			return ErrSkillFileTooLarge
		}
		total += len(data)
		if total > MaxSkillPackageBytes || len(files) >= MaxSkillPackageFiles {
			return errors.New("技能归档资源超限")
		}
		files[name] = data
		return nil
	}
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return SkillPackage{}, err
		}
		for _, f := range reader.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if !f.Mode().IsRegular() {
				return SkillPackage{}, errors.New("技能归档不允许符号链接或特殊文件")
			}
			if f.UncompressedSize64 > MaxSkillFileBytes {
				return SkillPackage{}, ErrSkillFileTooLarge
			}
			r, err := f.Open()
			if err != nil {
				return SkillPackage{}, err
			}
			d, err := io.ReadAll(io.LimitReader(r, MaxSkillFileBytes+1))
			err = errors.Join(err, r.Close())
			if err != nil {
				return SkillPackage{}, err
			}
			if err = add(f.Name, d); err != nil {
				return SkillPackage{}, err
			}
		}
	} else {
		var reader io.Reader = bytes.NewReader(data)
		if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
			gz, err := gzip.NewReader(reader)
			if err != nil {
				return SkillPackage{}, err
			}
			defer func() { _ = gz.Close() }()
			reader = gz
		}
		tr := tar.NewReader(reader)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return SkillPackage{}, err
			}
			if h.Typeflag == tar.TypeDir {
				continue
			}
			if h.Typeflag != tar.TypeReg {
				return SkillPackage{}, errors.New("技能归档禁止链接和特殊文件")
			}
			if h.Size > MaxSkillFileBytes {
				return SkillPackage{}, ErrSkillFileTooLarge
			}
			d, err := io.ReadAll(io.LimitReader(tr, MaxSkillFileBytes+1))
			if err != nil {
				return SkillPackage{}, err
			}
			if err = add(h.Name, d); err != nil {
				return SkillPackage{}, err
			}
		}
	}
	entry, ok := files[SkillFileName]
	if !ok {
		return SkillPackage{}, errors.New("技能归档入口不存在，请配置 subdir")
	}
	file, err := ParseSkillFile(entry, SkillFileName)
	if err != nil {
		return SkillPackage{}, err
	}
	delete(files, SkillFileName)
	p := SkillPackage{Name: file.Name, Description: file.Description, Content: file.Content, Metadata: file.Metadata, Files: files}
	return p, validateSkillPackage(p)
}
