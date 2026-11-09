// Package plugins 承载明确安装和启用的进程插件、受授权扩展调用及托管连接器。
package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sashabaranov/go-openai"
	"gopkg.in/yaml.v3"
	"rua.plus/saber/internal/toolkit"
)

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)
var revisionPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

// Manifest 规定可独立审查的进程命令、工具、命令、钩子和 provider 操作。
type Manifest struct {
	ManifestVersion int                 `json:"manifest_version" yaml:"manifest_version"`
	Name            string              `json:"name" yaml:"name"`
	Version         string              `json:"version" yaml:"version"`
	Description     string              `json:"description" yaml:"description"`
	Command         []string            `json:"command" yaml:"command"`
	Tools           []ToolDefinition    `json:"tools" yaml:"tools"`
	Commands        []CommandDefinition `json:"commands" yaml:"commands"`
	Hooks           []string            `json:"hooks" yaml:"hooks"`
	Providers       map[string][]string `json:"providers" yaml:"providers"`
	Skills          map[string]string   `json:"skills" yaml:"skills"`
	Dependencies    map[string]string   `json:"dependencies" yaml:"dependencies"`
	Capabilities    []string            `json:"capabilities" yaml:"capabilities"`
	Environment     []string            `json:"environment" yaml:"environment"`
}

// ToolDefinition 将模型 schema 与管理员明确授权的插件能力绑定。
type ToolDefinition struct {
	Name        string         `json:"name" yaml:"name"`
	Description string         `json:"description" yaml:"description"`
	Parameters  map[string]any `json:"parameters" yaml:"parameters"`
	Requires    []string       `json:"requires" yaml:"requires"`
	External    bool           `json:"external" yaml:"external"`
}

// CommandDefinition 是同一运行中进程拥有的命令，不修改共享 bot 注册表。
type CommandDefinition struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Plugin      string `json:"plugin,omitempty" yaml:"-"`
}

// ScanReport 给出不能被模型绕过的安装审计结果和内容摘要。
type ScanReport struct {
	Digest   string   `json:"digest"`
	Files    int      `json:"files"`
	Bytes    int64    `json:"bytes"`
	Warnings []string `json:"warnings"`
}

func readManifest(dir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err == nil {
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		err = dec.Decode(&m)
	} else if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(dir, "plugin.json"))
		if err == nil {
			dec := json.NewDecoder(strings.NewReader(string(data)))
			dec.DisallowUnknownFields()
			err = dec.Decode(&m)
		}
	}
	if err != nil {
		return m, fmt.Errorf("read plugin manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	return m, nil
}

// Validate 拒绝未知协议版本、越界名字和无法实际运行的空进程声明。
func (m Manifest) Validate() error {
	if m.ManifestVersion != 1 && m.ManifestVersion != 2 {
		return errors.New("plugin manifest_version must be 1 or 2 (Saber JSONRPC protocol)")
	}
	if !slugPattern.MatchString(m.Name) || m.Version == "" || len(m.Command) == 0 || m.Command[0] == "" {
		return errors.New("plugin needs safe name, version and executable command")
	}
	seen := map[string]bool{}
	for _, tool := range m.Tools {
		name := namespace(m.Name, tool.Name)
		if !slugPattern.MatchString(tool.Name) || tool.Parameters == nil || tool.Description == "" || seen[name] || len(name) > 64 {
			return errors.New("plugin has invalid/duplicate/overlong tool schema")
		}
		seen[name] = true
	}
	seen = map[string]bool{}
	for _, cmd := range m.Commands {
		if !slugPattern.MatchString(cmd.Name) || seen[cmd.Name] {
			return errors.New("plugin command name invalid or duplicate")
		}
		seen[cmd.Name] = true
	}
	for _, hook := range m.Hooks {
		if !contains([]string{"pre_tool_call", "post_tool_call", "session_start", "session_end", "pre_compress", "subagent_stop"}, hook) {
			return errors.New("plugin hook is not a supported lifecycle event")
		}
	}
	for kind, ops := range m.Providers {
		if !slugPattern.MatchString(kind) || len(ops) == 0 {
			return errors.New("plugin provider must declare operations")
		}
		for _, op := range ops {
			if !slugPattern.MatchString(op) {
				return errors.New("plugin provider operation invalid")
			}
		}
	}
	for _, value := range m.Skills {
		if value == "" || filepath.IsAbs(value) || strings.Contains(filepath.ToSlash(value), "../") {
			return errors.New("plugin skill must be relative inside plugin package")
		}
	}
	for _, key := range m.Environment {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n") {
			return errors.New("plugin environment key invalid")
		}
	}
	for _, capability := range m.Capabilities {
		if !contains([]string{"tool_call", "complete", "workspace", "external"}, capability) {
			return errors.New("plugin capability is not supported")
		}
	}
	return nil
}
func namespace(plugin, name string) string {
	return "plugin__" + strings.ReplaceAll(plugin, "-", "_") + "__" + strings.ReplaceAll(name, "-", "_")
}
func pluginPermission(plugin string) string { return "plugin_" + strings.ReplaceAll(plugin, "-", "_") }
func (m Manifest) definitions() []toolkit.Definition {
	out := make([]toolkit.Definition, 0, len(m.Tools))
	for _, t := range m.Tools {
		requires := append([]string{pluginPermission(m.Name), "exec"}, t.Requires...)
		out = append(out, toolkit.Definition{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: namespace(m.Name, t.Name), Description: t.Description, Parameters: t.Parameters}}, Permission: namespace(m.Name, t.Name), Requires: requires, External: t.External})
	}
	return out
}

// Scan 不执行插件代码，拒绝软链接、设备文件和无界安装包。
func Scan(dir string) (ScanReport, error) {
	report := ScanReport{Warnings: []string{"out-of-process code still runs as the configured OS user; use plugins.launcher for a filesystem/network sandbox"}}
	files := []string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("plugin package contains symlink")
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("plugin package contains nonregular file")
		}
		report.Files++
		report.Bytes += info.Size()
		if report.Files > 20000 || report.Bytes > 256<<20 {
			return errors.New("plugin package exceeds file/size safety limit")
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return report, err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return report, err
		}
		_, _ = h.Write([]byte(rel + "\x00"))
		_, _ = h.Write(data)
	}
	report.Digest = hex.EncodeToString(h.Sum(nil))
	_, err = readManifest(dir)
	return report, err
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
