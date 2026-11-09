package ai

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

type moaProvider struct {
	cfg  config.MoAConfig
	host toolkit.Host
}

// NewMoA 把协作预设接入真实模型运行路径；参考模型与聚合器都由 Core 白名单、预算与路由管理。
func NewMoA(cfg config.MoAConfig, host toolkit.Host) (toolkit.Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled && host.Complete == nil {
		return nil, errors.New("MoA requires the host model completion port")
	}
	return &moaProvider{cfg: cfg, host: host}, nil
}

func (p *moaProvider) Definitions() []toolkit.Definition {
	if !p.cfg.Enabled {
		return nil
	}
	presets := []string{}
	for name, preset := range p.cfg.Presets {
		if preset.Enabled {
			presets = append(presets, name)
		}
	}
	sort.Strings(presets)
	return []toolkit.Definition{{Permission: "mixture_of_agents", External: true, Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: "mixture_of_agents", Description: "Ask a configured Mixture of Agents preset for multiple private advisor analyses and an aggregator answer. Every slot bills its configured provider. No tools execute in this one-shot completion; use a selectable moa model for a full tool loop.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"prompt": map[string]any{"type": "string"}, "preset": map[string]any{"type": "string", "enum": presets}}, "required": []string{"prompt"}, "additionalProperties": false}}}}}
}

func (p *moaProvider) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if !p.cfg.Enabled || name != "mixture_of_agents" {
		return nil, errors.New("MoA tool is disabled or unknown")
	}
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		return nil, errors.New("MoA requires prompt")
	}
	preset, _ := args["preset"].(string)
	if preset == "" {
		preset = p.cfg.DefaultPreset
	}
	if configured, ok := p.cfg.Presets[preset]; !ok || !configured.Enabled {
		return nil, fmt.Errorf("MoA preset %q is not enabled", preset)
	}
	if p.host.Check == nil {
		return nil, errors.New("MoA permission check is not attached")
	}
	if err := p.host.Check(ctx, "mixture_of_agents", true); err != nil {
		return nil, err
	}
	answer, err := p.host.Complete(ctx, "moa."+preset, []openai.ChatCompletionMessage{{Role: "user", Content: prompt}})
	if err != nil {
		return nil, err
	}
	return map[string]any{"preset": preset, "acting_model": p.cfg.Presets[preset].Aggregator.Model, "answer": answer}, nil
}

func (p *moaProvider) Close() error { return nil }

// ModelAuthCommand 由已认证的管理员 CLI/API 消费；认证操作不登记为模型工具。
func (s *Service) ModelAuthCommand(ctx context.Context, args []string) (any, error) {
	if s.core == nil {
		return nil, errors.New("AI model core is unavailable")
	}
	return s.core.AuthCommand(ctx, args)
}
