package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// MemoryProvidersConfig 为内置记忆增加一个显式启用的外部提供者，不替换本地权威存储。
type MemoryProvidersConfig struct {
	Active           string                          `yaml:"active"`
	Providers        map[string]MemoryProviderConfig `yaml:"providers"`
	TimeoutSeconds   int                             `yaml:"timeout_seconds"`
	ContextBytes     int                             `yaml:"context_bytes"`
	StrictCheckpoint bool                            `yaml:"strict_checkpoint"`
}

// MemoryProviderConfig 选择已安装插件及其允许的生命周期；凭据只由插件私有配置读取。
type MemoryProviderConfig struct {
	Plugin     string   `yaml:"plugin"`
	Operations []string `yaml:"operations"`
}

// DefaultMemoryProvidersConfig 默认不向第三方传送对话。
func DefaultMemoryProvidersConfig() MemoryProvidersConfig {
	return MemoryProvidersConfig{TimeoutSeconds: 30, ContextBytes: 4096}
}

// Validate 校验启用的提供者真实存在于配置，拒绝未知生命周期和不受限资源参数。
func (c MemoryProvidersConfig) Validate() error {
	if c.TimeoutSeconds < 0 || c.TimeoutSeconds > 300 || c.ContextBytes < 0 || c.ContextBytes > 65536 {
		return errors.New("memory_providers timeout_seconds 必须为 0..300，context_bytes 为 0..65536")
	}
	if c.Active != "" {
		if _, ok := c.Providers[c.Active]; !ok {
			return errors.New("memory_providers.active 未配置")
		}
	}
	for name, p := range c.Providers {
		if name == "" || p.Plugin == "" {
			return errors.New("memory provider 名称及 plugin 不能为空")
		}
		seen := make(map[string]bool)
		for _, op := range p.Operations {
			switch op {
			case "initialize", "context", "prefetch", "turn_start", "sync_turn", "session_end", "session_switch", "pre_compress", "delegation", "memory_write", "shutdown", "tools", "tool":
			default:
				return fmt.Errorf("memory provider %s 未知 operation %q", name, op)
			}
			if seen[op] {
				return fmt.Errorf("memory provider %s 重复 operation %q", name, op)
			}
			seen[op] = true
		}
		if len(p.Operations) == 0 {
			return fmt.Errorf("memory provider %s 必须显式声明 operations", name)
		}
		if c.StrictCheckpoint && name == c.Active && !seen["pre_compress"] {
			return errors.New("strict_checkpoint 需要 provider pre_compress operation")
		}
	}
	return nil
}

// SkillCatalogSource 将可安装技能绑定到管理员配置的目录或内容校验过的 HTTPS 来源。
type SkillCatalogSource struct {
	Directory string `yaml:"directory"`
	URL       string `yaml:"url"`
	SHA256    string `yaml:"sha256"`
	Ref       string `yaml:"ref"`
	Subdir    string `yaml:"subdir"`
	Builtin   bool   `yaml:"builtin"`
}

// ValidateSkillsExtensions 校验 SkillsConfig 的目录包来源，模型不能指定 URL 或取消 pin。
func ValidateSkillsExtensions(c SkillsConfig) error {
	for name, src := range c.Catalog {
		if name == "" || strings.ContainsAny(name, "\\\x00") {
			return errors.New("技能 catalog 标识无效")
		}
		if (src.Directory == "") == (src.URL == "") {
			return fmt.Errorf("技能 %s 必须且只能配置 directory 或 url", name)
		}
		if src.Directory != "" && !filepath.IsAbs(src.Directory) {
			return fmt.Errorf("技能 %s directory 必须绝对路径", name)
		}
		if src.Subdir != "" && (filepath.IsAbs(src.Subdir) || strings.Contains(src.Subdir, "\\") || hasDotDot(src.Subdir)) {
			return fmt.Errorf("技能 %s subdir 越界", name)
		}
		if src.URL != "" {
			u, err := url.Parse(src.URL)
			if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("技能 %s URL 必须无凭据 HTTPS 地址", name)
			}
			if !c.AllowNetwork {
				return fmt.Errorf("技能 %s 网络来源需要 allow_network", name)
			}
		}
		if src.SHA256 != "" {
			b, err := hex.DecodeString(strings.TrimPrefix(src.SHA256, "sha256:"))
			if err != nil || len(b) != 32 {
				return fmt.Errorf("技能 %s sha256 必须完整 SHA256", name)
			}
		}
		if src.URL != "" && src.SHA256 == "" {
			return fmt.Errorf("技能 %s 网络安装必须 pin sha256", name)
		}
	}
	return nil
}

func hasDotDot(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
