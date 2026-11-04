package ai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/config"
)

// TestBudgetEvaluationBody_IncludesGoalAndPendingCalls 验证评估器能看到任务目标与模型刚请求的下一步。
// 旧输入只有工作状态和已完成轮次的工具结果：没有记录计划的任务会在第一次软限评估时被
// 判成「无计划、无发现、仅有环境探测」而终止，即使模型已经发出下一步工具调用。
func TestBudgetEvaluationBody_IncludesGoalAndPendingCalls(t *testing.T) {
	progress := agent.BudgetProgress{
		RoundsUsed: 5,
		SoftLimit:  5,
		HardLimit:  10,
		State: agent.TaskState{Instructions: []agent.Instruction{
			{TaskID: 69, Text: "写个你自己个人主页，然后发布我看看"},
		}},
		Recent: []agent.Round{
			{
				Response: agent.Response{Content: "先看看工作目录里有什么"},
				Tools: []agent.ToolRecord{{
					Call:    openai.ToolCall{Function: openai.FunctionCall{Name: "exec", Arguments: `{"command":"ls"}`}},
					Content: "README.md",
				}},
			},
			{
				Response: agent.Response{ToolCalls: []openai.ToolCall{{
					ID:       "call-6",
					Function: openai.FunctionCall{Name: "write_file", Arguments: `{"path":"index.html"}`},
				}}},
			},
		},
	}
	body := budgetEvaluationBody(progress)
	for _, want := range []string{
		"写个你自己个人主页，然后发布我看看",
		"先看看工作目录里有什么",
		"工具 exec：README.md",
		"已请求、尚未执行：write_file",
		`{"path":"index.html"}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("评估输入缺少 %q：\n%s", want, body)
		}
	}
}

// TestBudgetEvaluationBody_TruncatesEvidence 验证超长证据不会挤占主请求预算，且截断不破坏 UTF-8。
func TestBudgetEvaluationBody_TruncatesEvidence(t *testing.T) {
	instructions := make([]agent.Instruction, 0, 8)
	for index := range 8 {
		instructions = append(instructions, agent.Instruction{TaskID: int64(index + 1), Text: strings.Repeat("指令", 1000)})
	}
	var records []agent.ToolRecord
	for range 3 {
		records = append(records, agent.ToolRecord{
			Call:    openai.ToolCall{Function: openai.FunctionCall{Name: "read_file"}},
			Content: strings.Repeat("内容", 2000),
		})
	}
	body := budgetEvaluationBody(agent.BudgetProgress{
		RoundsUsed: 5,
		State:      agent.TaskState{Instructions: instructions},
		Recent:     []agent.Round{{Response: agent.Response{Content: strings.Repeat("想法", 800)}, Tools: records}},
	})
	if !utf8.ValidString(body) {
		t.Fatal("截断后出现无效 UTF-8")
	}
	if limit := 4*budgetToolResultBytes + budgetInstructionBytes + 1024; len(body) > limit {
		t.Fatalf("评估输入长度 = %d, want <= %d", len(body), limit)
	}
	if strings.Contains(body, "…（更早的指令已省略）") == false {
		t.Fatal("超长指令链应标记省略")
	}
}

// TestBudgetEvaluator_SendsTaskContext 验证评估请求真的把任务目标与待执行调用发给模型。
func TestBudgetEvaluator_SendsTaskContext(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		body = string(raw)
		_, _ = fmt.Fprintf(w, `{"status":"completed","model":"model","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q}]}]}`, `{"continue":true,"reason":"还有下一步"}`)
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.DefaultModel = "test.model"
	cfg.AI.Providers = map[string]config.ProviderConfig{"test": {
		Type: "openai", API: "openai-responses", BaseURL: server.URL,
		Models: map[string]config.ModelConfig{"model": {Model: "model", MaxTokens: 512, Temperature: new(float64(0))}},
	}}
	s, err := NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)

	decision, err := s.budgetEvaluator("test.model")(context.Background(), agent.BudgetProgress{
		RoundsUsed: 5,
		SoftLimit:  5,
		HardLimit:  10,
		State:      agent.TaskState{Instructions: []agent.Instruction{{TaskID: 69, Text: "task69-publish-homepage"}}},
		Recent: []agent.Round{{Response: agent.Response{ToolCalls: []openai.ToolCall{{
			ID:       "call-6",
			Function: openai.FunctionCall{Name: "write_file", Arguments: `{"path":"index.html"}`},
		}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Continue {
		t.Fatalf("decision = %+v", decision)
	}
	for _, want := range []string{"task69-publish-homepage", "write_file", `{\"path\":\"index.html\"}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("评估请求缺少 %q：\n%s", want, body)
		}
	}
}
