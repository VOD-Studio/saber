package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/memory"
)

func TestMemoryReview_PersonalAutoAdd(t *testing.T) {
	var toolCallsRequested bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !toolCallsRequested {
			toolCallsRequested = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-test-1",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "openai.local",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{
								{
									"id":   "call_mem_add",
									"type": "function",
									"function": map[string]any{
										"name":      "saber_memory",
										"arguments": `{"action":"add","content":"用户偏好：回答尽量精炼"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130},
			})
			return
		}
		// 第二轮：模型收到工具执行结果后正常结束
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test-2",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "openai.local",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "已为您提炼并保存偏好设置。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70},
		})
	}))
	defer modelServer.Close()

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true
	cfg.Memory.AutoLearn = true

	mem := newTestMemory(t, memory.Config{})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer svc.Stop()

	userMsg := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), userMsg)
	scope, _ := memory.Space(userMsg)

	// 插入历史对话投影
	_, err = mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        1001,
		Scope:         scope,
		Conversation:  "dm",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_1",
		UserText:      "以后的回答都尽量简短精炼一点，不要长篇大论。",
		AssistantText: "好的，我已了解您的偏好，以后会以精炼风格回答。",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	// 执行复盘
	err = svc.ReviewScope(ctx, scope)
	require.NoError(t, err)

	// 验证个人空间条目直接生效且 Explicit 为 false
	entries, err := mem.List(ctx, userMsg)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "用户偏好：回答尽量精炼", entries[0].Content)
	require.False(t, entries[0].Explicit, "自动提炼记忆 Explicit 应为 false")

	// 验证任务已完成复盘，不再返回
	remaining, err := mem.UnreviewedProjections(ctx, scope, 10)
	require.NoError(t, err)
	require.Empty(t, remaining)

	// 验证 token 统计更新
	settings, err := mem.ScopeSettings(ctx, scope)
	require.NoError(t, err)
	require.Equal(t, 1, settings.ReviewCount)
	require.Greater(t, settings.TokenCount, 0)
}

func TestMemoryReview_PersonalReplaceProposesChange(t *testing.T) {
	var toolCallsRequested bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !toolCallsRequested {
			toolCallsRequested = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-test-replace",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "openai.local",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{
								{
									"id":   "call_mem_replace",
									"type": "function",
									"function": map[string]any{
										"name":      "saber_memory",
										"arguments": `{"action":"replace","id":1,"content":"用户偏好更新：极简风格"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]any{"total_tokens": 80},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test-replace-2",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "openai.local",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "已提交修改建议。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"total_tokens": 40},
		})
	}))
	defer modelServer.Close()

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true
	cfg.Memory.AutoLearn = true

	mem := newTestMemory(t, memory.Config{})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer svc.Stop()

	userMsg := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), userMsg)
	scope, _ := memory.Space(userMsg)

	// 先显式添加原记忆
	mut, err := mem.Add(ctx, userMsg, "原偏好：简单明了", memory.Source{}, true)
	require.NoError(t, err)

	// 插入投影
	_, err = mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        2001,
		Scope:         scope,
		Conversation:  "dm",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_2",
		UserText:      "以后请更加极简一些。",
		AssistantText: "好的。",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	// 执行复盘
	err = svc.ReviewScope(ctx, scope)
	require.NoError(t, err)

	// 验证原记忆条目并未被静默覆盖！
	entries, err := mem.List(ctx, userMsg)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, mut.Entry.Content, entries[0].Content)

	// 验证形成了待确认建议
	pending, err := mem.Pending(ctx, userMsg)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, memory.ActionReplace, pending[0].Action)
	require.Equal(t, "用户偏好更新：极简风格", pending[0].Content)
}

func TestMemoryReview_GroupAddProposesChange(t *testing.T) {
	var toolCallsRequested bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !toolCallsRequested {
			toolCallsRequested = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-test-group",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "openai.local",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{
								{
									"id":   "call_mem_group",
									"type": "function",
									"function": map[string]any{
										"name":      "saber_memory",
										"arguments": `{"action":"add","content":"群共识：主分支要求必须双人 Code Review"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]any{"total_tokens": 100},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test-group-2",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "openai.local",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "已提炼群约定建议。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"total_tokens": 50},
		})
	}))
	defer modelServer.Close()

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true
	cfg.Memory.AutoLearn = true

	mem := newTestMemory(t, memory.Config{})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer svc.Stop()

	groupIdentity := groupIdentity("@bob:example.com", "!team_room:test")
	ctx := chat.WithIdentity(context.Background(), groupIdentity)
	scope, _ := memory.Space(groupIdentity)

	// 插入历史对话投影
	_, err = mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        3001,
		Scope:         scope,
		Conversation:  "!team_room:test",
		SenderID:      "@bob:example.com",
		UserMessageID: "msg_group_1",
		UserText:      "大家注意，从下周开始，所有提交到 master 的 PR 必须经过两人审查通过。",
		AssistantText: "收到，已记录该团队约定。",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	// 执行群复盘
	err = svc.ReviewScope(ctx, scope)
	require.NoError(t, err)

	// 验证群聊记忆没有直接写入
	entries, err := mem.List(ctx, groupIdentity)
	require.NoError(t, err)
	require.Empty(t, entries, "群聊自动复盘新增条目必须先形成建议，不能直接生效")

	// 验证形成了待确认建议
	pending, err := mem.Pending(ctx, groupIdentity)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, memory.ActionAdd, pending[0].Action)
	require.Equal(t, "群共识：主分支要求必须双人 Code Review", pending[0].Content)
}

func TestMemoryReview_PausedAndQuotaSkip(t *testing.T) {
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: "http://127.0.0.1:9999", APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true
	cfg.Memory.AutoLearn = true
	cfg.Memory.MaxScopeReviews = 1 // 最多 1 次

	mem := newTestMemory(t, memory.Config{})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer svc.Stop()

	userMsg := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), userMsg)
	scope, _ := memory.Space(userMsg)

	// 1. 暂停状态下直接跳过
	require.NoError(t, mem.Pause(ctx, userMsg))
	err = svc.ReviewScope(ctx, scope)
	require.NoError(t, err)

	// 恢复后模拟一次复盘使用
	require.NoError(t, mem.Resume(ctx, userMsg))
	require.NoError(t, mem.RecordReviewUsage(ctx, scope, 500))

	// 2. 超出配额（当前 1 次，上限 1 次）直接跳过
	err = svc.ReviewScope(ctx, scope)
	require.NoError(t, err)
}

func TestMemoryReview_ToolSandboxRestrictions(t *testing.T) {
	var bashCalled bool
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !bashCalled {
			bashCalled = true
			// 模型试图调用非法工具 bash
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "chatcmpl-test-illegal",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   "openai.local",
				"choices": []map[string]any{
					{
						"index": 0,
						"message": map[string]any{
							"role": "assistant",
							"tool_calls": []map[string]any{
								{
									"id":   "call_bash",
									"type": "function",
									"function": map[string]any{
										"name":      "bash",
										"arguments": `{"command":"rm -rf /"}`,
									},
								},
							},
						},
						"finish_reason": "tool_calls",
					},
				},
				"usage": map[string]any{"total_tokens": 50},
			})
			return
		}
		// 第二轮
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test-illegal-2",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "openai.local",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "工具被沙箱拒绝。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{"total_tokens": 30},
		})
	}))
	defer modelServer.Close()

	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: modelServer.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Memory.Enabled = true
	cfg.Memory.AutoLearn = true

	mem := newTestMemory(t, memory.Config{})
	svc, err := NewService(&cfg, WithMemory(mem))
	require.NoError(t, err)
	defer svc.Stop()

	userMsg := personalIdentity("@alice:example.com")
	ctx := chat.WithIdentity(context.Background(), userMsg)
	scope, _ := memory.Space(userMsg)

	_, err = mem.RecordProjection(ctx, memory.ProjectionInput{
		TaskID:        999,
		Scope:         scope,
		Conversation:  "dm",
		SenderID:      "@alice:example.com",
		UserMessageID: "msg_999",
		UserText:      "执行一个命令",
		AssistantText: "好的",
		TaskStatus:    "completed",
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	// 执行复盘，沙箱应拦截 bash 调用并继续安全结束
	err = svc.ReviewScope(ctx, scope)
	require.NoError(t, err)
	require.True(t, bashCalled)
}
