package task

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestManager_ContinuationWaitsAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path, dir := filepath.Join(t.TempDir(), "tasks.db"), t.TempDir()
	started := make(chan struct{})
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		close(started)
		<-ctx.Done()
		return agent.Result{Status: agent.Cancelled, Rounds: []agent.Round{{Response: agent.Response{ResponsesOutput: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque","summary":[]}`)}, FinishReason: "tool_calls", ToolCalls: []openai.ToolCall{{ID: "call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "exec", Arguments: `{"command":"touch done"}`}}}}, Tools: []agent.ToolRecord{{Call: openai.ToolCall{ID: "call"}, Content: "created done"}}}}}, ctx.Err()
	}, func(context.Context, Task) (string, error) { return "", nil })
	require.NoError(t, err)
	parent, err := m.Submit(ctx, message("original", "alice"), dir, request("create file; never publish"))
	require.NoError(t, err)
	<-started
	require.NoError(t, m.RememberMessage(ctx, parent.ID, "receipt"))
	follow := message("follow", "alice")
	follow.ReplyTo = "receipt"
	child, err := m.Continue(ctx, follow, dir, request("check it"))
	require.NoError(t, err)
	require.Equal(t, "queued", child.Status)
	closeManager(t, m)
	seen := make(chan agent.Request, 1)
	m, err = Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		seen <- req
		return agent.Result{Status: agent.Completed}, nil
	}, func(context.Context, Task) (string, error) { return "", nil })
	require.NoError(t, err)
	defer closeManager(t, m)
	select {
	case req := <-seen:
		require.Equal(t, "create file; never publish", req.Messages[0].Content)
		require.Equal(t, "call", req.Messages[1].ToolCalls[0].ID)
		require.JSONEq(t, `{"type":"reasoning","encrypted_content":"opaque","summary":[]}`, string(req.ResponsesHistory["call"][0]))
		require.Equal(t, "created done", req.Messages[2].Content)
		require.Equal(t, "check it", req.Messages[len(req.Messages)-1].Content)
	case <-time.After(3 * time.Second):
		t.Fatal("continuation did not resume")
	}
	stored := waitTask(t, m, child, func(t Task) bool { return t.Status == "completed" })
	require.Greater(t, len(stored.Request.Messages), 1)
	other := follow
	other.ID = "other"
	other.Session.Conversation = "other-room"
	_, err = m.Continue(ctx, other, dir, request("steal"))
	require.Error(t, err)
	wrong := follow
	wrong.ID = "wrong-dir"
	_, err = m.Continue(ctx, wrong, t.TempDir(), request("steal"))
	require.Error(t, err)
}

func TestManager_ContinuationThroughCancelledQueuedRound(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := openStore(filepath.Join(t.TempDir(), "tasks.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	m := &Manager{store: s, ctx: ctx}
	parent, err := m.Submit(ctx, message("source", "alice"), dir, request("original goal and constraints"))
	require.NoError(t, err)
	follow := message("follow", "alice")
	follow.ReplyTo = "source"
	child, err := m.Continue(ctx, follow, dir, request("additional constraint"))
	require.NoError(t, err)
	_, err = m.Cancel(ctx, chat.Identity{Session: follow.Session, SenderID: "alice"}, child.ID)
	require.NoError(t, err)
	latest := message("latest", "alice")
	latest.ReplyTo = "source"
	grandchild, err := m.Continue(ctx, latest, dir, request("continue"))
	require.NoError(t, err)
	req, err := m.continuationRequest(ctx, grandchild)
	require.NoError(t, err)
	require.Equal(t, "original goal and constraints", req.Messages[0].Content)
	require.Contains(t, fmt.Sprint(req.Messages), "additional constraint")
	stored, err := m.Get(ctx, latest.Session, grandchild.ID)
	require.NoError(t, err)
	again, err := m.continuationRequest(ctx, stored)
	require.NoError(t, err)
	require.Equal(t, req, again)
	duplicate, err := m.Continue(ctx, follow, dir, request("changed"))
	require.NoError(t, err)
	require.Equal(t, child.ID, duplicate.ID)
	var parentID int64
	require.NoError(t, s.db.QueryRow(`SELECT parent_id FROM task_links WHERE task_id=?`, child.ID).Scan(&parentID))
	require.Equal(t, parent.ID, parentID)
}

func TestManager_ContinuationRejectsMalformedToolHistory(t *testing.T) {
	valid := openai.ToolCall{ID: "call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "exec", Arguments: `{"command":"inspect"}`}}
	missingID, wrongType, missingName := valid, valid, valid
	missingID.ID = ""
	wrongType.Type = "unsupported"
	missingName.Function.Name = ""
	for _, tc := range []struct {
		name   string
		calls  []openai.ToolCall
		finish string
	}{
		{"missing_id", []openai.ToolCall{missingID}, "tool_calls"},
		{"duplicate_id", []openai.ToolCall{valid, valid}, "tool_calls"},
		{"wrong_type", []openai.ToolCall{wrongType}, "tool_calls"},
		{"missing_name", []openai.ToolCall{missingName}, "tool_calls"},
		{"truncated", []openai.ToolCall{valid}, "length"},
	} {
		for _, journal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/journal=%t", tc.name, journal), func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				store, err := openStore(filepath.Join(t.TempDir(), "tasks.db"))
				require.NoError(t, err)
				defer func() { require.NoError(t, store.db.Close()) }()
				m := &Manager{store: store, ctx: ctx}
				source, err := m.Submit(ctx, message("source", "alice"), dir, request("original goal"))
				require.NoError(t, err)
				_, err = store.claim(ctx)
				require.NoError(t, err)
				runtime := agent.Runtime{Model: func(context.Context, agent.Request, func(agent.Event)) (agent.Response, error) {
					return agent.Response{Content: "partial response", ToolCalls: tc.calls, FinishReason: tc.finish}, nil
				}, Execute: func(context.Context, string, map[string]any) (agent.ToolOutput, error) {
					t.Error("malformed call executed")
					return agent.ToolOutput{}, nil
				}}
				result, runErr := runtime.Run(ctx, source.Request, func(e agent.Event) { require.NoError(t, store.record(ctx, source.ID, e)) })
				require.Error(t, runErr)
				if journal {
					require.NoError(t, store.recover(ctx))
				} else {
					require.NoError(t, store.finish(ctx, source, result, runErr, false))
				}
				follow := message("follow", "alice")
				follow.ReplyTo = "source"
				child, err := m.Continue(ctx, follow, dir, request("continue"))
				require.NoError(t, err)
				req, err := m.continuationRequest(ctx, child)
				require.NoError(t, err)
				for _, msg := range req.Messages {
					require.Empty(t, msg.ToolCalls)
					require.NotEqual(t, openai.ChatMessageRoleTool, msg.Role)
					require.Empty(t, msg.ToolCallID)
				}
				require.Contains(t, fmt.Sprint(req.Messages), "partial response")
				require.Contains(t, fmt.Sprint(req.Messages), "历史响应校验失败")
				require.Contains(t, fmt.Sprint(req.Messages), "inspect")
				saved, err := m.Get(ctx, follow.Session, child.ID)
				require.NoError(t, err)
				require.Equal(t, req, saved.Request)
				again, err := m.continuationRequest(ctx, saved)
				require.NoError(t, err)
				require.Equal(t, req, again)
			})
		}
	}
}

func TestManager_ContinuationRepairsPreviouslySavedInvalidHistory(t *testing.T) {
	call := openai.ToolCall{ID: "call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "exec", Arguments: `{}`}}
	invalid := call
	invalid.ID = ""
	assistant := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{call}}
	result := openai.ChatCompletionMessage{Role: openai.ChatMessageRoleTool, ToolCallID: "call", Content: `{"error":"tool_failed"}`}
	wrong := result
	wrong.ToolCallID = "other"
	for _, tc := range []struct {
		name    string
		history []openai.ChatCompletionMessage
		valid   bool
	}{
		{"empty_id", []openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleAssistant, ToolCalls: []openai.ToolCall{invalid}}, {Role: openai.ChatMessageRoleTool, Content: "unknown effect"}}, false},
		{"orphan_result", []openai.ChatCompletionMessage{result}, false},
		{"missing_result", []openai.ChatCompletionMessage{assistant}, false},
		{"wrong_result", []openai.ChatCompletionMessage{assistant, wrong}, false},
		{"duplicate_result", []openai.ChatCompletionMessage{assistant, result, result}, false},
		{"valid_tool_failure", []openai.ChatCompletionMessage{assistant, result}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store, err := openStore(filepath.Join(t.TempDir(), "tasks.db"))
			require.NoError(t, err)
			defer func() { require.NoError(t, store.db.Close()) }()
			m := &Manager{store: store, ctx: ctx}
			parent, err := m.Submit(ctx, message("source", "alice"), dir, request("original goal"))
			require.NoError(t, err)
			follow := message("follow", "alice")
			follow.ReplyTo = parent.Message.ID
			child, err := m.Continue(ctx, follow, dir, request("continue"))
			require.NoError(t, err)
			dirty := child.Request
			dirty.Messages = append([]openai.ChatCompletionMessage{{Role: openai.ChatMessageRoleUser, Content: "original goal"}}, tc.history...)
			dirty.Messages = append(dirty.Messages, openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: "continue"})
			data, err := json.Marshal(dirty)
			require.NoError(t, err)
			_, err = store.db.Exec(`UPDATE tasks SET request=? WHERE id=?`, data, child.ID)
			require.NoError(t, err)
			_, err = store.db.Exec(`INSERT INTO task_contexts(task_id) VALUES(?)`, child.ID)
			require.NoError(t, err)
			child, err = m.Get(ctx, follow.Session, child.ID)
			require.NoError(t, err)
			req, err := m.continuationRequest(ctx, child)
			require.NoError(t, err)
			if tc.valid {
				require.Equal(t, dirty, req)
			} else {
				for _, msg := range req.Messages {
					require.Empty(t, msg.ToolCalls)
					require.NotEqual(t, openai.ChatMessageRoleTool, msg.Role)
				}
				require.Contains(t, fmt.Sprint(req.Messages), "历史响应校验失败")
				require.Contains(t, req.Messages[1].Content, fmt.Sprintf("%+v", tc.history[0]))
			}
			saved, err := m.Get(ctx, follow.Session, child.ID)
			require.NoError(t, err)
			require.Equal(t, req, saved.Request)
		})
	}
}

func TestManager_ContinuationContextBudget(t *testing.T) {
	ctx := context.Background()
	path, dir := filepath.Join(t.TempDir(), "tasks.db"), t.TempDir()
	s, err := openStore(path)
	require.NoError(t, err)
	m := &Manager{store: s, ctx: ctx, contextPolicy: agent.ContextPolicy{Enabled: true, MaxMessages: 3, MaxInputTokens: 10000}}
	parent, err := m.Submit(ctx, message("first", "alice"), dir, request("first question"))
	require.NoError(t, err)
	for i := 0; i < 6; i++ {
		follow := message(fmt.Sprintf("next-%d", i), "alice")
		follow.ReplyTo = parent.Message.ID
		child, err := m.Continue(ctx, follow, dir, request(fmt.Sprintf("question-%d", i)))
		require.NoError(t, err)
		req, err := m.continuationRequest(ctx, child)
		require.NoError(t, err)
		require.LessOrEqual(t, len(req.Messages), 3)
		require.Equal(t, fmt.Sprintf("question-%d", i), req.Messages[len(req.Messages)-1].Content)
		parent, err = m.Get(ctx, follow.Session, child.ID)
		require.NoError(t, err)
	}
	// 历史仍在数据库，重启并收紧限制后重新裁剪已保存的请求。
	require.NoError(t, s.db.Close())
	s, err = openStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.db.Close()) }()
	m = &Manager{store: s, ctx: ctx, contextPolicy: agent.ContextPolicy{MaxMessages: 2, MaxInputTokens: 10000}}
	req, err := m.continuationRequest(ctx, parent)
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)
	require.Equal(t, "question-5", req.Messages[0].Content)
	var count int
	require.NoError(t, s.db.QueryRow("SELECT count(*) FROM tasks").Scan(&count))
	require.Equal(t, 7, count)
}
