// Package discovery 实现按当前任务授权的渐进式工具披露，不创建第二条执行通道。
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/toolkit"
)

// Service 在每次搜索和执行时获取当前工具集合，禁用或撤权立即生效。
type Service struct {
	cfg  config.AgentToolsConfig
	host toolkit.Host
	list func(context.Context) []toolkit.Definition
}

// New 创建真实工具发现入口；列表和调用缺失时直接报错。
func New(cfg config.AgentToolsConfig, host toolkit.Host, list func(context.Context) []toolkit.Definition) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if list == nil || host.Call == nil {
		return nil, errors.New("tool discovery requires live catalog and authorized dispatcher")
	}
	return &Service{cfg: cfg, host: host, list: list}, nil
}

func bridge(name string) bool {
	return name == "tool_search" || name == "tool_describe" || name == "tool_call"
}
func has(values []string, name string) bool {
	for _, v := range values {
		if v == name {
			return true
		}
	}
	return false
}
func definition(name, description string, properties map[string]any, required ...string) toolkit.Definition {
	return toolkit.Definition{Tool: openai.Tool{Type: openai.ToolTypeFunction, Function: &openai.FunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}}
}

// Definitions 返回桥接工具本身；目标工具的精确权限不会被桥接权限替代。
func (s *Service) Definitions() []toolkit.Definition {
	if s.cfg.ToolSearch == "off" {
		return nil
	}
	return []toolkit.Definition{
		definition("tool_search", "Search tools available in this task. Search does not execute or connect an account.", map[string]any{"query": map[string]any{"type": "string"}, "queries": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 7}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}),
		definition("tool_describe", "Get complete schemas for currently permitted tools.", map[string]any{"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 10}}, "names"),
		definition("tool_call", "Call one currently permitted tool using its exact arguments and original execution authorization.", map[string]any{"name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}}, "name", "arguments"),
	}
}

func (s *Service) authorize(ctx context.Context, d toolkit.Definition) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return errors.New("trusted identity required")
	}
	if d.Tool.Function == nil || !agent.ToolAllowed(ctx, d.Tool.Function.Name) {
		return errors.New("tool outside child task scope")
	}
	if d.Permission != "" || d.External || len(d.Requires) > 0 {
		if s.host.Check == nil {
			return errors.New("authorization unavailable")
		}
		name := d.Permission
		if name == "" && d.External {
			name = d.Tool.Function.Name
		}
		if name != "" {
			if err := s.host.Check(ctx, name, d.External); err != nil {
				return err
			}
		}
		for _, name := range d.Requires {
			if err := s.host.Check(ctx, name, false); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) catalog(ctx context.Context) []toolkit.Definition {
	var out []toolkit.Definition
	for _, d := range s.list(ctx) {
		if d.Tool.Function != nil && !bridge(d.Tool.Function.Name) && s.authorize(ctx, d) == nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tool.Function.Name < out[j].Tool.Function.Name })
	return out
}

// Assemble 缩减模型的初始 schema，仅隐藏指定工具；所有桥接调用仍取实时授权目录。
func (s *Service) Assemble(ctx context.Context, tools []openai.Tool, contextTokens int) []openai.Tool {
	if s.cfg.ToolSearch == "off" {
		return tools
	}
	deferTool := func(name string) bool {
		if bridge(name) || has(s.cfg.AlwaysVisible, name) {
			return false
		}
		return len(s.cfg.Deferred) == 0 || has(s.cfg.Deferred, name)
	}
	total := 0
	for _, t := range tools {
		if t.Function != nil && deferTool(t.Function.Name) {
			b, _ := json.Marshal(t)
			total += (len(b) + 3) / 4
		}
	}
	if s.cfg.ToolSearch == "auto" {
		threshold := s.cfg.ListingMaxTokens
		if contextTokens > 0 {
			threshold = contextTokens * s.cfg.ThresholdPercent / 100
		}
		if total <= threshold {
			return tools
		}
	}
	out := make([]openai.Tool, 0, len(tools))
	for _, t := range tools {
		if t.Function != nil && !deferTool(t.Function.Name) && !bridge(t.Function.Name) {
			out = append(out, t)
		}
	}
	for _, d := range s.Definitions() {
		out = append(out, d.Tool)
	}
	return out
}

// Execute 搜索、描述及真实派发共享相同实时目录，不能递归调用桥接工具。
func (s *Service) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if s.cfg.ToolSearch == "off" {
		return nil, errors.New("tool discovery disabled")
	}
	if _, ok := chat.IdentityFromContext(ctx); !ok {
		return nil, errors.New("trusted identity required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog := s.catalog(ctx)
	switch name {
	case "tool_call":
		target, _ := args["name"].(string)
		values, ok := args["arguments"].(map[string]any)
		if !ok || target == "" || bridge(target) {
			return nil, errors.New("tool_call needs one non-bridge name and object arguments")
		}
		for _, d := range catalog {
			if d.Tool.Function.Name == target {
				if err := s.authorize(ctx, d); err != nil {
					return nil, err
				}
				return s.host.Call(ctx, target, values)
			}
		}
		return nil, errors.New("tool is not available in this task")
	case "tool_describe":
		names, err := stringList(args["names"], 10)
		if err != nil {
			return nil, err
		}
		schemas := map[string]openai.Tool{}
		var missing []string
		for _, target := range names {
			found := false
			for _, d := range catalog {
				if d.Tool.Function.Name == target {
					schemas[target] = d.Tool
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, target)
			}
		}
		return map[string]any{"schemas": schemas, "not_found": missing}, nil
	case "tool_search":
		queries := []string{}
		if q, ok := args["query"].(string); ok && strings.TrimSpace(q) != "" {
			queries = append(queries, q)
		}
		if raw, ok := args["queries"]; ok {
			v, err := stringList(raw, 7)
			if err != nil {
				return nil, err
			}
			queries = append(queries, v...)
		}
		if len(queries) == 0 || len(queries) > 7 {
			return nil, errors.New("provide 1..7 nonempty search queries")
		}
		limit := s.cfg.SearchLimit
		if v, ok := args["limit"].(float64); ok {
			limit = int(v)
		}
		if limit < 1 || limit > 100 {
			return nil, errors.New("limit must be 1..100")
		}
		type hit struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Score       int    `json:"-"`
		}
		groups := make([]any, 0, len(queries))
		for _, q := range queries {
			var hits []hit
			words := strings.Fields(strings.ToLower(q))
			for _, d := range catalog {
				f := d.Tool.Function
				text := strings.ToLower(f.Name + " " + f.Description)
				score := 0
				for _, word := range words {
					if strings.Contains(text, word) {
						score++
					}
				}
				if score > 0 {
					hits = append(hits, hit{f.Name, f.Description, score})
				}
			}
			sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
			if len(hits) > limit {
				hits = hits[:limit]
			}
			groups = append(groups, map[string]any{"query": q, "tools": hits})
		}
		return map[string]any{"results": groups, "available_tools": len(catalog)}, nil
	default:
		return nil, errors.New("unknown discovery tool")
	}
}
func stringList(raw any, max int) ([]string, error) {
	var values []string
	switch v := raw.(type) {
	case []string:
		values = v
	case []any:
		for _, x := range v {
			str, ok := x.(string)
			if !ok {
				return nil, errors.New("expected string array")
			}
			values = append(values, str)
		}
	default:
		return nil, errors.New("expected string array")
	}
	if len(values) < 1 || len(values) > max {
		return nil, errors.New("string array length out of range")
	}
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return nil, errors.New("empty search/name")
		}
	}
	return values, nil
}

// Close 不持有额外执行资源。
func (s *Service) Close() error { return nil }
