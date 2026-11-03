package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestGeneratedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := GenerateExample(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Agent.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Server.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AI.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Platforms.Matrix.Enabled || cfg.MCP.Enabled || cfg.Execution.Enabled || len(cfg.AI.Providers) != 0 || cfg.AI.DefaultModel != "" {
		t.Fatal("default configuration enables unconfigured integrations")
	}
	if !reflect.DeepEqual(cfg.Agent, DefaultConfig().Agent) || cfg.AI.TimeoutSeconds != DefaultAIConfig().TimeoutSeconds {
		t.Fatal("generated and program defaults differ")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("generated permissions: %v, %v", info, err)
	}
}

func TestLoadRejectsOldAndUnknownFields(t *testing.T) {
	for _, input := range []string{
		"ai: {provider: openai}", "ai: {timeout_seconds: 30}", "ai: {context: {enabled: true}}",
		"matrix: {enabled: true}",
		"ai: {tool_calling: {max_iterations: 5}}", "ai: {direct_chat_auto_reply: true}", "meme: {enabled: false}",
		"ai: {providers: {test: {reasoning_efort: high}}}", "agent: {context: {expiry_minutes: 60}}",
		"server: {listen: '127.0.0.1:8320'}\n---\nai: {}",
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("obsolete or unknown field accepted")
			}
		})
	}
}

func TestAgentConfigValidate(t *testing.T) {
	for _, change := range []func(*AgentConfig){
		func(c *AgentConfig) { c.MaxIterations = 0 }, func(c *AgentConfig) { c.TimeoutSeconds = -1 },
		func(c *AgentConfig) { c.Context.MaxMessages = 0 }, func(c *AgentConfig) { c.Context.MaxTokens = 0 },
		func(c *AgentConfig) { c.Retry.FallbackEnabled = true }, func(c *AgentConfig) { c.Retry.MaxRetries = -1 },
	} {
		cfg := DefaultAgentConfig()
		change(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid limits accepted: %+v", cfg)
		}
	}
	unlimited := DefaultAgentConfig()
	unlimited.Budget.MaxTotalTokens = 0
	if err := unlimited.Validate(); err != nil {
		t.Fatalf("max_total_tokens: 0 must mean unlimited: %v", err)
	}
}

func TestModelZeroTemperatureInheritance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte(`ai:
  temperature: 0.7
  max_tokens: 8192
  request_timeout_seconds: 90
  providers:
    local:
      type: openai
      base_url: http://localhost/v1
      models:
        zero: {model: zero, temperature: 0, max_tokens: 1234}
        inherited: {model: inherited}
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for id, temp := range map[string]float64{"local.zero": 0, "local.inherited": 0.7} {
		model, found := cfg.AI.GetModelConfig(id)
		if !found || model.Temperature == nil || *model.Temperature != temp || model.RequestTimeoutSeconds != 90 {
			t.Fatalf("%s did not inherit: %+v", id, model)
		}
		if id == "local.zero" && model.MaxTokens != 1234 {
			t.Fatal("model budget not applied")
		}
	}
}

func TestDocumentedConfiguration(t *testing.T) {
	for _, name := range []string{"configuration.md", "responses.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", name))
		if err != nil {
			t.Fatal(err)
		}
		blocks := regexp.MustCompile("(?s)```yaml\\n(.*?)\\n```").FindAllSubmatch(data, -1)
		if len(blocks) == 0 {
			t.Fatal("missing YAML examples")
		}
		for i, block := range blocks {
			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, block[1], 0o600); err != nil {
					t.Fatal(err)
				}
				cfg, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if err = cfg.Agent.Validate(); err != nil {
					t.Fatal(err)
				}
				if cfg.AI.DefaultModel != "" {
					cfg.AI.Enabled = true
				}
				if err = cfg.AI.Validate(); err != nil {
					t.Fatal(err)
				}
				if cfg.Platforms.Matrix.Enabled {
					if err = cfg.Matrix.Validate(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

// TestAgentConfigDefaults 固定任务策略默认值：模型传输默认开启，
// 聊天平台的任务接收回执默认关闭，避免每次交办都多一条噪音消息。
func TestAgentConfigDefaults(t *testing.T) {
	cfg := DefaultAgentConfig()
	if !cfg.StreamEnabled {
		t.Error("agent.stream 默认应开启")
	}
	if cfg.TaskReceiptEnabled {
		t.Error("agent.task_receipt_enabled 默认应关闭，任务结果仍照常投递")
	}
	if cfg.TaskDeliveryMaxAttempts != 20 {
		t.Errorf("agent.task_delivery_max_attempts 默认应为 20，实际 %d", cfg.TaskDeliveryMaxAttempts)
	}
	if !strings.Contains(ExampleConfig(), "task_delivery_max_attempts: 20") {
		t.Error("示例配置应说明任务投递最大尝试次数")
	}
	if !strings.Contains(ExampleConfig(), "task_receipt_enabled: false") {
		t.Error("示例配置应说明任务回执开关及其默认值")
	}
}

func TestAgentConfigValidate_TaskDeliveryMaxAttempts(t *testing.T) {
	cfg := DefaultAgentConfig()
	cfg.TaskDeliveryMaxAttempts = 0
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.TaskDeliveryMaxAttempts != DefaultTaskDeliveryMaxAttempts {
		t.Errorf("零值应回落到默认值，实际 %d", cfg.TaskDeliveryMaxAttempts)
	}
	cfg.TaskDeliveryMaxAttempts = 5
	if err := cfg.Validate(); err != nil || cfg.TaskDeliveryMaxAttempts != 5 {
		t.Errorf("显式值应保持，err=%v value=%d", err, cfg.TaskDeliveryMaxAttempts)
	}
	cfg.TaskDeliveryMaxAttempts = -1
	if err := cfg.Validate(); err == nil {
		t.Error("负数应被拒绝")
	}
}

// TestAgentConfigSubagents 验证子代理配置零值等于默认值、可显式关闭并拒绝越界范围。
func TestAgentConfigSubagents(t *testing.T) {
	zero := AgentConfig{}
	if err := zero.Subagents.Validate(); err != nil {
		t.Fatalf("零值子代理配置应合法: %v", err)
	}
	if !zero.Subagents.IsEnabled() {
		t.Error("零值应默认开启 delegate")
	}
	if zero.Subagents.MaxRounds != DefaultSubagentMaxRounds || zero.Subagents.MaxParallel != DefaultSubagentMaxParallel {
		t.Errorf("零值未回落到默认上限: %+v", zero.Subagents)
	}
	if zero.Subagents.MaxTotalTokens != 0 {
		t.Error("max_total_tokens 零值表示只受父账本约束")
	}

	disabled := false
	cfg := DefaultAgentConfig()
	cfg.Subagents.Enabled = &disabled
	if err := cfg.Validate(); err != nil || cfg.Subagents.IsEnabled() {
		t.Errorf("显式关闭应生效: err=%v config=%+v", err, cfg.Subagents)
	}

	for _, testCase := range []struct {
		name   string
		change func(*SubagentsConfig)
	}{
		{"rounds 超上限", func(c *SubagentsConfig) { c.MaxRounds = SubagentMaxRoundsLimit + 1 }},
		{"rounds 负数", func(c *SubagentsConfig) { c.MaxRounds = -1 }},
		{"parallel 超上限", func(c *SubagentsConfig) { c.MaxParallel = SubagentMaxParallelLimit + 1 }},
		{"parallel 负数", func(c *SubagentsConfig) { c.MaxParallel = -1 }},
		{"tokens 负数", func(c *SubagentsConfig) { c.MaxTotalTokens = -1 }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := DefaultAgentConfig()
			testCase.change(&cfg.Subagents)
			if err := cfg.Validate(); err == nil {
				t.Fatal("越界子代理配置应被拒绝")
			}
		})
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("agent:\n  subagents:\n    max_rounds: 5\n    max_parallel: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Agent.Validate(); err != nil {
		t.Fatal(err)
	}
	if loaded.Agent.Subagents.MaxRounds != 5 || loaded.Agent.Subagents.MaxParallel != 2 || !loaded.Agent.Subagents.IsEnabled() {
		t.Fatalf("yaml 子代理配置未生效: %+v", loaded.Agent.Subagents)
	}
}

// TestSkillsConfigValidate 验证 memory.skills 目录约束：绝对路径、存在性、上限与导出父目录。
func TestSkillsConfigValidate(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, MaxSkillDirs+1)
	for i := range tooMany {
		tooMany[i] = root
	}
	tests := []struct {
		name    string
		config  SkillsConfig
		wantErr bool
	}{
		{name: "默认配置", config: DefaultSkillsConfig()},
		{name: "合法只读目录", config: SkillsConfig{Dirs: []string{root}}},
		{name: "相对路径", config: SkillsConfig{Dirs: []string{"skills"}}, wantErr: true},
		{name: "空目录项", config: SkillsConfig{Dirs: []string{""}}, wantErr: true},
		{name: "目录不存在", config: SkillsConfig{Dirs: []string{filepath.Join(root, "missing")}}, wantErr: true},
		{name: "路径是文件", config: SkillsConfig{Dirs: []string{file}}, wantErr: true},
		{name: "重复目录", config: SkillsConfig{Dirs: []string{root, root}}, wantErr: true},
		{name: "超过目录上限", config: SkillsConfig{Dirs: tooMany}, wantErr: true},
		{name: "导出目录合法", config: SkillsConfig{ExportDir: filepath.Join(root, "export")}},
		{name: "导出目录已存在", config: SkillsConfig{ExportDir: root}},
		{name: "导出目录相对路径", config: SkillsConfig{ExportDir: "export"}, wantErr: true},
		{name: "导出父目录不存在", config: SkillsConfig{ExportDir: filepath.Join(root, "missing", "export")}, wantErr: true},
		{name: "导出目录是文件", config: SkillsConfig{ExportDir: file}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}

	cfg := DefaultMemoryConfig()
	cfg.Enabled = false
	cfg.Skills = SkillsConfig{Dirs: []string{"relative"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("记忆关闭时也应校验技能目录配置")
	}
	cfg.Skills = DefaultSkillsConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认记忆配置应合法: %v", err)
	}
	if !strings.Contains(ExampleConfig(), "skills:") || !strings.Contains(ExampleConfig(), "export_dir:") {
		t.Fatal("示例配置应说明 memory.skills")
	}
}
