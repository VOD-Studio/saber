package bot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"rua.plus/saber/internal/cli"
	"rua.plus/saber/internal/config"
	_ "rua.plus/saber/internal/db"
	"rua.plus/saber/internal/execution"
)

// doctorCheck 是只读诊断的一条结果。
type doctorCheck struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// runDoctor 严格执行只读检查；不监听端口、不创建目录或数据库、不清理容器。
func runDoctor(ctx context.Context, args []string, stdout io.Writer) error {
	flags, err := cli.ParseArgs(args)
	if err != nil {
		return ExitError(2, err)
	}
	checks := []doctorCheck{}
	configPath, err := resolveConfigPath(flags.ConfigPath)
	if err != nil {
		checks = append(checks, doctorCheck{Code: "config.path", Severity: "error", Message: err.Error()})
	}
	cfg := config.DefaultConfig()
	loaded := false
	if err == nil {
		loadedConfig, loadErr := config.Load(configPath)
		if loadErr != nil {
			checks = append(checks, doctorCheck{Code: "config.load", Severity: "error", Message: loadErr.Error()})
		} else {
			cfg, loaded = loadedConfig, true
			checks = append(checks, doctorCheck{Code: "config.load", Severity: "ok", Message: "configuration parsed with strict fields"})
		}
	}
	checks = append(checks, doctorChecks(ctx, cfg, configPath, loaded)...)
	ok := true
	for _, check := range checks {
		if check.Severity == "error" {
			ok = false
		}
	}
	if flags.JSON {
		if err := json.NewEncoder(stdout).Encode(struct {
			Checks []doctorCheck `json:"checks"`
			OK     bool          `json:"ok"`
		}{checks, ok}); err != nil {
			return err
		}
	} else {
		for _, check := range checks {
			if _, err := fmt.Fprintf(stdout, "%s %s %s\n", check.Severity, check.Code, check.Message); err != nil {
				return err
			}
		}
	}
	if !ok {
		return ExitError(1, nil)
	}
	return nil
}

func doctorChecks(ctx context.Context, cfg *config.Config, configPath string, loaded bool) []doctorCheck {
	var checks []doctorCheck
	add := func(code, severity, message string) {
		checks = append(checks, doctorCheck{Code: code, Severity: severity, Message: message})
	}
	if err := cfg.Server.Validate(); err != nil {
		add("server.config", "error", err.Error())
	} else {
		add("server.config", "ok", "listen address is valid")
	}
	if host, _, err := net.SplitHostPort(cfg.Server.Listen); err == nil {
		if host != "127.0.0.1" && host != "::1" && host != "localhost" && host != "" {
			add("server.listen", "warning", "listen address is not loopback; the local API should normally bind 127.0.0.1")
		}
	}
	if err := cfg.AI.Validate(); err != nil {
		add("ai.config", "error", err.Error())
	} else if cfg.AI.Enabled {
		add("ai.config", "ok", "AI providers and default model are valid")
	} else {
		add("ai.config", "ok", "ai.enabled=false; chat replies are disabled")
	}
	if err := cfg.Agent.Validate(); err != nil {
		add("agent.limits", "error", err.Error())
	} else {
		add("agent.limits", "ok", fmt.Sprintf("max_rounds=%d budget=%t compaction=%t", cfg.Agent.MaxIterations, cfg.Agent.Budget.Enabled, cfg.Agent.Context.Compaction.Enabled))
	}
	if err := cfg.Memory.Validate(); err != nil {
		add("memory.config", "error", err.Error())
	} else if cfg.Memory.Enabled {
		add("memory.config", "ok", fmt.Sprintf("memory enabled (auto_learn=%t, review_interval=%d)", cfg.Memory.AutoLearn, cfg.Memory.ReviewInterval))
	} else {
		add("memory.config", "ok", "memory.enabled=false; memory and skill commands are disabled")
	}
	if err := cfg.Commands.Validate(); err != nil {
		add("commands.config", "error", err.Error())
	} else {
		add("commands.config", "ok", "command permissions are valid")
	}
	if !cfg.Execution.Enabled {
		add("execution.disabled", "ok", "execution.enabled=false; tools are unavailable but chat still works")
	} else {
		protected, secrets := executionInputs(cfg, configPath)
		validated, err := execution.ValidateConfig(cfg.Execution, protected, secrets)
		if err != nil {
			add("execution.config", "error", err.Error())
		} else {
			add("execution.config", "ok", "execution configuration and workspace grants are valid")
			for _, diagnostic := range execution.Inspect(ctx, validated) {
				add(diagnostic.Code, diagnostic.Severity, diagnostic.Message)
			}
		}
	}
	checks = append(checks, doctorPrivateFiles(cfg, configPath, loaded)...)
	return checks
}

// doctorPrivateFiles 只检查存在性与权限，不回显文件内容。
func doctorPrivateFiles(cfg *config.Config, configPath string, loaded bool) []doctorCheck {
	var checks []doctorCheck
	add := func(code, severity, message string) {
		checks = append(checks, doctorCheck{Code: code, Severity: severity, Message: message})
	}
	tokenPath := cfg.Server.TokenPath(configPath)
	if info, err := os.Stat(tokenPath); err != nil {
		add("server.token", "warning", "server token file does not exist yet; it will be created on first serve")
	} else if info.Mode().Perm()&0o077 != 0 {
		add("server.token", "error", fmt.Sprintf("token file permissions are insecure: %o", info.Mode().Perm()))
	} else {
		add("server.token", "ok", "token file permissions are private")
	}
	configDir := filepath.Dir(configPath)
	dbs := []struct {
		name    string
		enabled bool
		state   bool
	}{
		{"tasks.db", true, true},
		{"persona.db", true, false},
		{"memory.db", cfg.Memory.Enabled, false},
	}
	for _, database := range dbs {
		if !database.enabled {
			continue
		}
		path := filepath.Join(configDir, database.name)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			add("sqlite."+database.name, "warning", "database does not exist yet; it will be created on first serve")
			continue
		}
		if err != nil {
			add("sqlite."+database.name, "error", err.Error())
			continue
		}
		if info.Mode().Perm()&0o077 != 0 {
			add("sqlite."+database.name, "error", fmt.Sprintf("database permissions are insecure: %o", info.Mode().Perm()))
		}
		checks = append(checks, inspectSQLite(context.Background(), path, database.state)...)
	}
	if loaded {
		add("config.documents", "ok", "exactly one YAML document was parsed")
	}
	return checks
}

func inspectSQLite(ctx context.Context, path string, runtimeState bool) []doctorCheck {
	var checks []doctorCheck
	add := func(code, severity, message string) {
		checks = append(checks, doctorCheck{Code: code, Severity: severity, Message: message})
	}
	dsn := "file:" + path + "?mode=ro&immutable=1&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		add("sqlite."+filepath.Base(path), "error", err.Error())
		return checks
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		add("sqlite."+filepath.Base(path), "error", err.Error())
		return checks
	}
	defer func() { _ = rows.Close() }()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			add("sqlite."+filepath.Base(path), "error", err.Error())
			return checks
		}
		tables[name] = true
	}
	if err = rows.Err(); err != nil {
		add("sqlite."+filepath.Base(path), "error", err.Error())
		return checks
	}
	if runtimeState && !tables["task_runtime_state"] {
		add("sqlite.tasks.db", "warning", "old database lacks task_runtime_state; new tasks will backfill on first write")
	} else if runtimeState {
		add("sqlite.tasks.db", "ok", "tasks schema contains task_runtime_state")
	}
	if _, err := os.Stat(path + "-wal"); err == nil {
		add("sqlite."+filepath.Base(path)+".wal", "warning", "WAL file exists; the read-only snapshot may omit recent commits")
	}
	if len(checks) == 0 {
		add("sqlite."+filepath.Base(path), "ok", "read-only schema check passed")
	}
	return checks
}
