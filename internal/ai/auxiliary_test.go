package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
)

// TestAuxiliaryRequests_UseModelOutputBudget 验证评估与摘要请求沿用模型的生成预算与重试策略。
//
// 推理模型的思考 token 计入 max_output_tokens：旧的固定 512/1024 上限会让上游
// 在 JSON 产出前返回 incomplete，评估失败随后被当成预算耗尽终止任务。
// 旧的辅助请求还硬编码零重试，一次瞬时 5xx 同样会终止整次任务。
func TestAuxiliaryRequests_UseModelOutputBudget(t *testing.T) {
	const modelBudget = 4096
	var limits []int
	var requests int
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
		if requests++; requests == 1 {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, `{"continue":true,"reason":"还有未完成的检查"}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.DefaultModel = "test.model"
	cfg.Agent.Retry.Enabled = true
	cfg.Agent.Retry.InitialDelayMs = 1
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
	for index, limit := range limits {
		if limit != modelBudget {
			t.Fatalf("第 %d 次辅助请求输出上限 = %d, want %d", index+1, limit, modelBudget)
		}
	}
	// 评估首次 503 后按配置重试一次，摘要再发一次：辅助请求共 3 次。
	if len(limits) != 3 {
		t.Fatalf("辅助请求次数 = %d, want 3", len(limits))
	}
}

// TestService_UnlimitedMaxTokensOmitsWireLimit 验证全局 max_tokens: 0 时不发送输出上限。
//
// 任务请求经过预算账本后仍保持 0；评估与摘要请求同样省略字段，交由模型默认。
func TestService_UnlimitedMaxTokensOmitsWireLimit(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		for _, key := range []string{"max_output_tokens", "max_tokens"} {
			if _, found := body[key]; found {
				t.Errorf("不设上限时仍发送了 %s: %v", key, body[key])
			}
		}
		requests.Add(1)
		fmt.Fprintf(w, `{"status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, `{"continue":true,"reason":"继续"}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.AI.Enabled, cfg.AI.DefaultModel, cfg.AI.MaxTokens = true, "test.model", 0
	cfg.Agent.StreamEnabled = false
	cfg.Agent.Retry.Enabled = false
	cfg.AI.Providers = map[string]config.ProviderConfig{"test": {
		Type: "openai", API: "openai-responses", BaseURL: server.URL,
		Models: map[string]config.ModelConfig{"model": {Model: "model", Temperature: new(float64(0))}},
	}}
	s, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	if err = s.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")); err != nil {
		t.Fatal(err)
	}

	message := chat.Message{Session: chat.Session{Platform: "tui", Account: "local", Conversation: "unlimited"}, ID: "one", SenderID: "local", Text: "hello"}
	created, err := s.SubmitChatTask(context.Background(), message, "", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	status := ""
	for time.Now().Before(deadline) {
		current, getErr := s.tasks.Get(context.Background(), message.Session, created.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		status = current.Status
		if status == "failed" {
			t.Fatalf("任务失败: %s", current.Error)
		}
		if status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status != "completed" {
		t.Fatalf("任务未完成，状态 = %q", status)
	}

	if _, err = s.budgetEvaluator("test.model")(context.Background(), agent.BudgetProgress{RoundsUsed: 1, HardLimit: 2}); err != nil {
		t.Fatalf("评估失败: %v", err)
	}
	if _, err = s.summarizer("test.model")(context.Background(), agent.SummaryInput{
		Messages: []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "历史"}},
	}); err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("请求次数 = %d, want 3（任务 + 评估 + 摘要）", requests.Load())
	}
}
