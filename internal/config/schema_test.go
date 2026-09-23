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
	if cfg.Matrix.Enabled || cfg.MCP.Enabled || cfg.Execution.Enabled || len(cfg.AI.Providers) != 0 || cfg.AI.DefaultModel != "" {
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
				if cfg.Matrix.Enabled {
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
	if !strings.Contains(ExampleConfig(), "task_receipt_enabled: false") {
		t.Error("示例配置应说明任务回执开关及其默认值")
	}
}
