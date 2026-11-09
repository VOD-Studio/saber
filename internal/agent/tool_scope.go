package agent

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

type toolScopeKey struct{}

// WithToolScope 将子任务工具快照与已有父范围求交集；空切片表示不允许任何工具。
func WithToolScope(ctx context.Context, names []string) context.Context {
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" && ToolAllowed(ctx, name) {
			allowed[name] = true
		}
	}
	return context.WithValue(ctx, toolScopeKey{}, allowed)
}

// ToolAllowed 检查程序设置的子任务范围；未设置范围不替代宿主权限检查。
func ToolAllowed(ctx context.Context, name string) bool {
	scope, ok := ctx.Value(toolScopeKey{}).(map[string]bool)
	return !ok || scope[name]
}

func scopedTools(ctx context.Context, tools []openai.Tool) []openai.Tool {
	if _, scoped := ctx.Value(toolScopeKey{}).(map[string]bool); !scoped {
		return tools
	}
	first := -1
	for index, tool := range tools {
		if tool.Function == nil || !ToolAllowed(ctx, tool.Function.Name) {
			first = index
			break
		}
	}
	if first < 0 {
		return tools
	}
	out := make([]openai.Tool, 0, len(tools))
	out = append(out, tools[:first]...)
	for _, tool := range tools[first+1:] {
		if tool.Function != nil && ToolAllowed(ctx, tool.Function.Name) {
			out = append(out, tool)
		}
	}
	return out
}
