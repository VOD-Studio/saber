package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DevToolsConfig 配置编辑诊断、文档提取及有界上下文引用；不授予任何执行权限。
type DevToolsConfig struct {
	Enabled                  bool                            `yaml:"enabled"`
	LSPEnabled               bool                            `yaml:"lsp_enabled"`
	TrustedWorkspaces        []string                        `yaml:"trusted_workspaces"`
	ExcludeRoots             []string                        `yaml:"exclude_roots"`
	WaitMode                 string                          `yaml:"wait_mode"`
	WaitTimeoutSeconds       int                             `yaml:"wait_timeout_seconds"`
	WarmupTimeoutSeconds     int                             `yaml:"warmup_timeout_seconds"`
	BrokenRetrySeconds       int                             `yaml:"broken_retry_seconds"`
	InlineWaitMilliseconds   int                             `yaml:"inline_wait_milliseconds"`
	DeferredWaitSeconds      int                             `yaml:"deferred_wait_seconds"`
	MaxServers               int                             `yaml:"max_servers"`
	Servers                  map[string]LanguageServerConfig `yaml:"servers"`
	MaxDocumentBytes         int64                           `yaml:"max_document_bytes"`
	MaxExtractedBytes        int64                           `yaml:"max_extracted_bytes"`
	NotebookOutputCharacters int                             `yaml:"notebook_output_characters"`
	PDFCommand               []string                        `yaml:"pdf_command"`
	DocumentConverters       map[string][]string             `yaml:"document_converters"`
	MaxReferences            int                             `yaml:"max_references"`
	FolderEntries            int                             `yaml:"folder_entries"`
	ReferenceBytes           int                             `yaml:"reference_bytes"`
}

// LanguageServerConfig 的命令与初始化配置只能来自管理员；自定义服务也受工作区信任限制。
type LanguageServerConfig struct {
	Disabled              bool           `yaml:"disabled"`
	Command               []string       `yaml:"command"`
	Extensions            []string       `yaml:"extensions"`
	LanguageID            string         `yaml:"language_id"`
	RootMarkers           []string       `yaml:"root_markers"`
	InitializationOptions map[string]any `yaml:"initialization_options"`
	Settings              map[string]any `yaml:"settings"`
}

// DefaultDevToolsConfig 默认启用纯解析及冲突保护，语言服务只在显式信任的工作区启动。
func DefaultDevToolsConfig() DevToolsConfig {
	return DevToolsConfig{Enabled: true, LSPEnabled: true, WaitMode: "document", WaitTimeoutSeconds: 5,
		InlineWaitMilliseconds: 80, DeferredWaitSeconds: 5, MaxServers: 8,
		MaxDocumentBytes: 50 << 20, MaxExtractedBytes: 8 << 20, NotebookOutputCharacters: 20000,
		PDFCommand:    []string{"pdftotext", "-layout", "-", "-"},
		MaxReferences: 16, FolderEntries: 200, ReferenceBytes: 128 << 10}
}

// Validate 拒绝无界工作、非法路径模式及含空参数的可执行配置。
func (c DevToolsConfig) Validate() error {
	if c.WaitMode != "" && c.WaitMode != "document" && c.WaitMode != "full" {
		return fmt.Errorf("devtools.wait_mode must be document or full")
	}
	if c.WaitTimeoutSeconds < 0 || c.WaitTimeoutSeconds > 120 || c.WarmupTimeoutSeconds < 0 || c.WarmupTimeoutSeconds > 300 || c.BrokenRetrySeconds < 0 || c.InlineWaitMilliseconds < 0 || c.InlineWaitMilliseconds > 1000 || c.DeferredWaitSeconds < 0 || c.DeferredWaitSeconds > 120 {
		return fmt.Errorf("devtools diagnostic timeout out of range")
	}
	if c.MaxServers < 0 || c.MaxServers > 64 || c.MaxDocumentBytes < 0 || c.MaxDocumentBytes > 50<<20 || c.MaxExtractedBytes < 0 || c.MaxExtractedBytes > 32<<20 || c.NotebookOutputCharacters < 0 || c.NotebookOutputCharacters > 100000 || c.MaxReferences < 0 || c.MaxReferences > 64 || c.FolderEntries < 0 || c.FolderEntries > 1000 || c.ReferenceBytes < 0 || c.ReferenceBytes > 4<<20 {
		return fmt.Errorf("devtools resource limit out of range")
	}
	for _, p := range c.TrustedWorkspaces {
		if !filepath.IsAbs(p) || filepath.Clean(p) == string(filepath.Separator) {
			return fmt.Errorf("devtools.trusted_workspaces requires non-root absolute paths")
		}
	}
	for _, p := range c.ExcludeRoots {
		if _, err := filepath.Match(p, ""); err != nil {
			return fmt.Errorf("devtools.exclude_roots: %w", err)
		}
	}
	for id, s := range c.Servers {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("devtools server ID is empty")
		}
		if err := validateDevCommand(s.Command); err != nil {
			return fmt.Errorf("devtools.servers.%s: %w", id, err)
		}
		for _, ext := range s.Extensions {
			if ext == "" || strings.ContainsAny(ext, "/\\\x00") {
				return fmt.Errorf("devtools.servers.%s has invalid extension", id)
			}
		}
		for _, marker := range s.RootMarkers {
			clean := filepath.Clean(marker)
			if marker == "" || strings.ContainsRune(marker, 0) || filepath.IsAbs(marker) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("devtools.servers.%s has invalid root marker", id)
			}
		}
	}
	if err := validateDevCommand(c.PDFCommand); err != nil {
		return fmt.Errorf("devtools.pdf_command: %w", err)
	}
	for ext, cmd := range c.DocumentConverters {
		if !strings.HasPrefix(ext, ".") || strings.ContainsAny(ext, "/\\\x00") || len(cmd) == 0 {
			return fmt.Errorf("devtools.document_converters has invalid extension or empty command")
		}
		if err := validateDevCommand(cmd); err != nil {
			return fmt.Errorf("devtools.document_converters.%s: %w", ext, err)
		}
	}
	return nil
}

func validateDevCommand(cmd []string) error {
	for _, arg := range cmd {
		if strings.TrimSpace(arg) == "" || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("command contains empty or NUL argument")
		}
	}
	return nil
}
