package agent

import (
	"errors"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

type choiceConstraint struct{ mode, name string }

func toolChoiceConstraint(choice any, tools []openai.Tool) (choiceConstraint, error) {
	if choice == nil {
		return choiceConstraint{}, nil
	}
	switch value := choice.(type) {
	case string:
		switch value {
		case "auto", "none":
			return choiceConstraint{mode: value}, nil
		case "required":
			if len(tools) == 0 {
				return choiceConstraint{}, errors.New("tool_choice required 没有已授权工具")
			}
			return choiceConstraint{mode: value}, nil
		default:
			return choiceConstraint{}, errors.New("tool_choice 必须为 auto、none、required 或标准具名 function 对象")
		}
	case map[string]any:
		if value["type"] != "function" {
			return choiceConstraint{}, errors.New("tool_choice 仅支持 function")
		}
		function, ok := value["function"].(map[string]any)
		if !ok {
			return choiceConstraint{}, errors.New("tool_choice.function 必须为对象")
		}
		name, ok := function["name"].(string)
		if !ok || name == "" {
			return choiceConstraint{}, errors.New("tool_choice 缺少函数名称")
		}
		for _, tool := range tools {
			if tool.Function != nil && tool.Function.Name == name {
				return choiceConstraint{mode: "function", name: name}, nil
			}
		}
		return choiceConstraint{}, fmt.Errorf("tool_choice %q 未在当前授权工具集中", name)
	default:
		return choiceConstraint{}, errors.New("tool_choice 不是标准策略或 function 对象")
	}
}

func validateToolChoiceResponse(constraint choiceConstraint, response Response) error {
	switch constraint.mode {
	case "", "auto":
		return nil
	case "none":
		if len(response.ToolCalls) > 0 {
			return errors.New("模型违反 tool_choice none")
		}
		return nil
	case "required":
		if len(response.ToolCalls) == 0 {
			return errors.New("模型未满足 tool_choice required")
		}
		return nil
	default:
		if len(response.ToolCalls) == 0 {
			return fmt.Errorf("模型未调用指定工具 %q", constraint.name)
		}
		for _, call := range response.ToolCalls {
			if call.Function.Name != constraint.name {
				return fmt.Errorf("模型调用 %q 不符合指定工具 %q", call.Function.Name, constraint.name)
			}
		}
		return nil
	}
}
