package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/config"
	"rua.plus/saber/internal/task"
)

func TestNaturalTaskRedirect(t *testing.T) {
	for _, tc := range []struct {
		text, action, payload string
		id                    int64
		ok                    bool
	}{
		{"重定向任务 #12 优先检查配置", "redirect", "优先检查配置", 12, true},
		{"补充任务 12 再检查日志", "redirect", "再检查日志", 12, true},
		{"补充任务#12：先跑测试\n再提交", "redirect", "先跑测试\n再提交", 12, true},
		{"重定向任务 #12", "", "", 0, false},
		{"重定向任务 #0 内容", "", "", 0, false},
		{"查询任务 42 的状态", "status", "", 42, true},
		{"取消任务 #42", "cancel", "", 42, true},
		{"帮我重定向任务", "", "", 0, false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			action, id, ok := naturalTaskCommand(tc.text)
			require.Equal(t, tc.action, action)
			require.Equal(t, tc.id, id)
			require.Equal(t, tc.ok, ok)
			payload, payloadOK := naturalRedirectText(tc.text)
			require.Equal(t, tc.payload, payload)
			require.Equal(t, tc.payload != "", payloadOK)
		})
	}
}

func TestParseTaskRedirect(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		id     int64
		text   string
		wantOK bool
	}{
		{"12 优先检查配置", 12, "优先检查配置", true},
		{"#12  保留  内部空格 ", 12, "保留  内部空格", true},
		{"12", 0, "", false},
		{"abc 文本", 0, "", false},
		{"0 文本", 0, "", false},
		{"", 0, "", false},
	} {
		id, text, ok := parseTaskRedirect(tc.raw)
		require.Equal(t, tc.wantOK, ok, tc.raw)
		require.Equal(t, tc.id, id, tc.raw)
		require.Equal(t, tc.text, text, tc.raw)
	}
}

// captureAdapter 记录发送的正文，命令回执与自然语言回执都通过它检查。
type captureAdapter struct {
	mu   sync.Mutex
	text []string
}

func (a *captureAdapter) Capabilities() chat.Capabilities { return chat.Capabilities{} }

func (a *captureAdapter) Send(_ context.Context, reply chat.Reply) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.text = append(a.text, reply.Text)
	return "$sent", nil
}

func (a *captureAdapter) Edit(context.Context, string, chat.Reply) error { return nil }

func (a *captureAdapter) SetTyping(context.Context, chat.Session, bool) error { return nil }

func (a *captureAdapter) last() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.text) == 0 {
		return ""
	}
	return a.text[len(a.text)-1]
}

// TestTaskRedirectCommandNaturalAndSteering 验证命令与自然语言排队补充要求，
// 排队任务开始时由 Runtime 消费，模型请求与事件都带上补充文本。
func TestTaskRedirectCommandNaturalAndSteering(t *testing.T) {
	var mu sync.Mutex
	var requests [][]openai.ChatCompletionMessage
	firstArrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []openai.ChatCompletionMessage `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		mu.Lock()
		index := len(requests)
		requests = append(requests, body.Messages)
		mu.Unlock()
		if index == 0 {
			once.Do(func() { close(firstArrived) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": fmt.Sprintf("回答 %d", index)}, "finish_reason": "stop",
		}}}); err != nil {
			t.Error(err)
		}
	}))
	defer model.Close()
	cfg := *config.DefaultConfig()
	cfg.AI.Enabled = true
	cfg.AI.Providers = map[string]config.ProviderConfig{"openai": {Type: "openai", BaseURL: model.URL, APIKey: "test"}}
	cfg.AI.DefaultModel = "openai.local"
	cfg.Agent.StreamEnabled = false
	service, err := NewService(&cfg)
	require.NoError(t, err)
	defer service.Stop()
	require.NoError(t, service.EnableTasks(filepath.Join(t.TempDir(), "tasks.db")))
	session := chat.Session{Platform: "terminal", Account: "saber", Conversation: "demo"}
	sender := chat.Identity{Session: session, SenderID: "42"}
	message := func(id, text string) chat.Message {
		return chat.Message{ID: id, SenderID: sender.SenderID, Text: text, Session: session}
	}
	started, err := service.SubmitChatTask(context.Background(), message("$started", "先跑长任务"), "", "")
	require.NoError(t, err)
	select {
	case <-firstArrived:
	case <-time.After(5 * time.Second):
		t.Fatal("首个任务未开始")
	}
	queuedMsg := message("$queued", "排队任务")
	// 同会话已有运行中的轮次时不接受新的聊天轮次，直接入库模拟已排队的后台任务。
	queued, err := service.tasks.Submit(context.Background(), queuedMsg, service.taskDir, agent.Request{
		Model: cfg.AI.DefaultModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: queuedMsg.Text},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "queued", queued.Status)

	adapter := &captureAdapter{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, service.TaskCommand(ctx, message("$cmd", "redirect"), adapter, "redirect", fmt.Sprintf("%d 优先检查配置", queued.ID)))
	require.Contains(t, adapter.last(), "开始时生效")
	// 自然语言解析后走同一条排队逻辑。
	require.NoError(t, service.replyTaskCommand(ctx, message("$natural", fmt.Sprintf("补充任务 #%d 再检查日志", queued.ID)), adapter, "redirect", queued.ID))
	require.Contains(t, adapter.last(), "开始时生效")
	// 状态回执显示待生效数量。
	require.NoError(t, service.TaskCommand(ctx, message("$status", "status"), adapter, "status", fmt.Sprint(queued.ID)))
	require.Contains(t, adapter.last(), "补充要求待生效：2 条")
	// 同一任务重复补充超过上限时明确拒绝。
	for i := range task.MaxPendingSteering - 2 {
		_, err = service.tasks.Redirect(ctx, sender, queued.ID, fmt.Sprintf("额外 %d", i))
		require.NoError(t, err)
	}
	_, err = service.tasks.Redirect(ctx, sender, queued.ID, "超出上限")
	require.ErrorContains(t, err, "待生效补充要求已达")

	close(release)
	require.Eventually(t, func() bool {
		got, getErr := service.tasks.Get(ctx, session, queued.ID)
		return getErr == nil && got.Status == "completed"
	}, 10*time.Second, 20*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(requests), 2)
	var joined strings.Builder
	for _, request := range requests[1:] {
		for _, item := range request {
			joined.WriteString(item.Content)
			joined.WriteString("\n")
		}
	}
	require.Contains(t, joined.String(), "[运行中补充要求] 优先检查配置")
	require.Contains(t, joined.String(), "[运行中补充要求] 再检查日志")
	records, err := service.tasks.ReadEvents(ctx, session, queued.ID, 0)
	require.NoError(t, err)
	var steered int
	for _, record := range records {
		if record.Kind == agent.Steered {
			steered++
		}
	}
	require.Equal(t, task.MaxPendingSteering, steered)
	// 全部生效后待生效数量归零。
	pending, err := service.tasks.PendingSteering(ctx, session, queued.ID)
	require.NoError(t, err)
	require.Zero(t, pending)
	finished, err := service.tasks.Get(ctx, session, started.ID)
	require.NoError(t, err)
	require.Equal(t, "completed", finished.Status)
}
