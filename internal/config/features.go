package config

import "fmt"

// ValidateFeatures 在启动和配置修改时校验全部可选能力，避免无效配置被静默接受。
func (c *Config) ValidateFeatures() error {
	checks := []struct {
		name     string
		validate func() error
	}{
		{"browser", c.Browser.Validate},
		{"computer", c.Computer.Validate},
		{"media_tools", c.MediaTools.Validate},
		{"code_execution", c.CodeExecution.Validate},
		{"control", c.Control.Validate},
		{"memory_providers", c.MemoryProviders.Validate},
		{"memory_backends", c.MemoryBackends.Validate},
		{"model_runtime", c.ModelRuntime.Validate},
		{"plugins", c.Plugins.Validate},
		{"vault", c.Vault.Validate},
		{"agent_tools", c.AgentTools.Validate},
		{"desktop", c.Desktop.Validate},
		{"devtools", c.DevTools.Validate},
		{"automation", c.Automation.Validate},
		{"batch", c.Batch.Validate},
		{"handoff", c.Handoff.Validate},
		{"api", c.API.Validate},
		{"platforms", c.Platforms.Validate},
	}
	for _, check := range checks {
		if err := check.validate(); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
	}
	if model := c.Agent.Subagents.WorkerModel; model != "" {
		if !c.AI.ModelConfigured(model) {
			return fmt.Errorf("agent.subagents.worker_model %q is not configured", model)
		}
	}
	for name, worker := range c.Agent.Subagents.Workers {
		if worker.Model != "" {
			if !c.AI.ModelConfigured(worker.Model) {
				return fmt.Errorf("agent.subagents.workers.%s.model %q is not configured", name, worker.Model)
			}
		}
	}
	return nil
}
