package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

// TestAuxiliaryRequests_UseModelOutputBudget 验证评估与摘要请求沿用模型的生成预算。
//
// 推理模型的思考 token 计入 max_output_tokens：旧的固定 512/1024 上限会让上游
// 在 JSON 产出前返回 incomplete，评估失败随后被当成预算耗尽终止任务。
func TestAuxiliaryRequests_UseModelOutputBudget(t *testing.T) {
	const modelBudget = 4096
	var limits []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		limit, _ := body["max_output_tokens"].(float64)
		limits = append(limits, int(limit))
		if int(limit) < modelBudget {
			fmt.Fprint(w, `{"status":"incomplete","model":"model","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`)
			return
		}
		fmt.Fprintf(w, `{"status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, `{"continue":true,"reason":"还有未完成的检查"}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.DefaultModel = "test.model"
	cfg.AI.Providers = map[string]config.ProviderConfig{"test": {
		Type: "openai", API: "openai-responses", BaseURL: server.URL,
		Models: map[string]config.ModelConfig{"model": {Model: "model", MaxTokens: modelBudget, Temperature: new(float64(0))}},
	}}
	s, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	decision, err := s.budgetEvaluator("test.model")(context.Background(), agent.BudgetProgress{RoundsUsed: 5, HardLimit: 10})
	if err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if !decision.Continue {
		t.Fatalf("decision = %+v", decision)
	}
	summary, err := s.summarizer("test.model")(context.Background(), agent.SummaryInput{
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "历史"}},
	})
	if err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	if summary.Text == "" {
		t.Fatal("摘要为空")
	}
	if len(limits) != 2 || limits[0] != modelBudget || limits[1] != modelBudget {
		t.Fatalf("辅助请求输出上限 = %v, want %d", limits, modelBudget)
	}
}
