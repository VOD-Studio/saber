package batch

import (
	"strings"

	"github.com/sashabaranov/go-openai"
)

func withoutMemoryTools(tools []openai.Tool) []openai.Tool {
	out := make([]openai.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Function == nil {
			continue
		}
		name := tool.Function.Name
		if strings.HasPrefix(name, "saber_memory") || strings.HasPrefix(name, "saber_history") || strings.HasPrefix(name, "memory_") || strings.HasPrefix(name, "history_") || name == "saber_skill" || name == "skill_manage" || name == "skill_view" || name == "memory_profile" {
			continue
		}
		out = append(out, tool)
	}
	return out
}
func normalizeTrajectory(t *Trajectory, names []string) {
	if t.ToolStats == nil {
		t.ToolStats = map[string]ToolStats{}
	}
	if t.ToolErrors == nil {
		t.ToolErrors = map[string]int{}
	}
	for _, name := range names {
		if _, ok := t.ToolStats[name]; !ok {
			t.ToolStats[name] = ToolStats{}
		}
		if _, ok := t.ToolErrors[name]; !ok {
			t.ToolErrors[name] = 0
		}
	}
}
