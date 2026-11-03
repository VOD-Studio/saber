package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"rua.plus/saber/internal/config"
)

// Diagnostic 是只读环境检查结果；doctor 与运行时共用同一探测逻辑。
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// Inspect 只读检查活动执行后端的依赖与残留，不创建目录、不清理容器。
func Inspect(ctx context.Context, cfg config.ExecutionConfig) []Diagnostic {
	if !cfg.Enabled {
		return nil
	}
	var diagnostics []Diagnostic
	active := make(map[string]bool)
	needsCommands := make(map[string]bool)
	for _, grant := range cfg.Grants {
		active[grant.Workspace] = true
		for _, tool := range grant.Tools {
			if tool == "exec" || tool == "process" {
				needsCommands[grant.Workspace] = true
			}
		}
	}
	names := make([]string, 0, len(active))
	for name := range active {
		names = append(names, name)
	}
	slices.Sort(names)
	dockerChecked := false
	for _, name := range names {
		if _, ok := cfg.Workspaces[name]; !ok {
			continue
		}
		backend, err := cfg.BackendFor(name)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Code: "execution.backend", Severity: "error", Message: err.Error()})
			continue
		}
		switch backend {
		case "local":
			if !needsCommands[name] {
				continue
			}
			if !slices.Contains([]string{"darwin", "linux", "freebsd", "openbsd"}, runtime.GOOS) {
				diagnostics = append(diagnostics, Diagnostic{Code: "execution.local.platform", Severity: "error", Message: "local execution is unsupported on this platform; configure backend: docker"})
				continue
			}
			if _, err := os.Stat("/bin/sh"); err != nil {
				diagnostics = append(diagnostics, Diagnostic{Code: "execution.local.shell", Severity: "error", Message: "local execution requires /bin/sh: " + err.Error()})
			} else {
				diagnostics = append(diagnostics, Diagnostic{Code: "execution.local.platform", Severity: "ok", Message: "local execution is supported on " + runtime.GOOS})
			}
			diagnostics = append(diagnostics, Diagnostic{Code: "execution.local.host_access", Severity: "warning", Message: "local backend runs as the Saber OS user and can read accessible configuration secrets; do not grant untrusted members"})
		case "docker":
			if dockerChecked {
				continue
			}
			dockerChecked = true
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := probeDocker(probeCtx, cfg)
			cancel()
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{Code: "execution.docker", Severity: "error", Message: err.Error()})
				continue
			}
			diagnostics = append(diagnostics, Diagnostic{Code: "execution.docker", Severity: "ok", Message: "docker CLI, daemon and image are available"})
			diagnostics = append(diagnostics, dockerResidue(ctx, cfg)...)
		}
	}
	diagnostics = append(diagnostics, environmentRecords(cfg.LogDir)...)
	return diagnostics
}

func dockerResidue(ctx context.Context, cfg config.ExecutionConfig) []Diagnostic {
	absolute, err := filepath.Abs(cfg.LogDir)
	if err != nil {
		return []Diagnostic{{Code: "execution.docker.residue", Severity: "warning", Message: "cannot resolve execution log directory: " + err.Error()}}
	}
	owner := fmt.Sprintf("%x", sha256.Sum256([]byte(absolute)))
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := docker(probeCtx, "ps", "-aq", "--filter", "label=saber.executor="+owner).Output()
	if err != nil {
		return []Diagnostic{{Code: "execution.docker.residue", Severity: "warning", Message: "cannot list leftover containers: " + err.Error()}}
	}
	count := len(strings.Fields(string(output)))
	if count == 0 {
		return []Diagnostic{{Code: "execution.docker.residue", Severity: "ok", Message: "no leftover task containers"}}
	}
	return []Diagnostic{{Code: "execution.docker.residue", Severity: "warning", Message: fmt.Sprintf("%d leftover task containers will be removed by serve startup Recover", count)}}
}

// environmentRecords 报告未关闭的旧环境与清理隔离标记，不修改任何记录。
func environmentRecords(logDir string) []Diagnostic {
	absolute, err := filepath.Abs(logDir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil
	}
	var diagnostics []Diagnostic
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "task-") {
			continue
		}
		dir := filepath.Join(absolute, entry.Name())
		if data, err := os.ReadFile(filepath.Join(dir, "environment.json")); err == nil {
			var record environmentRecord
			if json.Unmarshal(data, &record) == nil && record.State == envActive {
				severity := "warning"
				if record.Backend == "local" || record.Backend == "" {
					severity = "error"
				}
				diagnostics = append(diagnostics, Diagnostic{Code: "execution.environment.active", Severity: severity, Message: fmt.Sprintf("%s has an unclosed %s environment; inspect before reusing the workspace", dir, record.Backend)})
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "quarantine")); err == nil {
			diagnostics = append(diagnostics, Diagnostic{Code: "execution.environment.quarantine", Severity: "error", Message: dir + " is quarantined after cleanup failure; check the workspace and mark the record closed manually"})
		}
	}
	return diagnostics
}
