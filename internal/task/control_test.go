package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func controlTestOptions() Options {
	return Options{Authorize: func(chat.Identity, string) error { return nil }}
}

func controlTestContext(msg chat.Message) context.Context {
	return chat.WithIdentity(context.Background(), chat.Identity{Session: msg.Session, SenderID: msg.SenderID, Direct: msg.Direct})
}

func TestManagerGraphDependencyCycleAndFailedGate(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var dependentRuns atomic.Int32
	options := controlTestOptions()
	options.MaxConcurrent = 1
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), func(ctx context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		switch req.Messages[0].Content {
		case "occupy":
			started <- struct{}{}
			select {
			case <-release:
				return agent.Result{Status: agent.Completed}, nil
			case <-ctx.Done():
				return agent.Result{Status: agent.Cancelled}, ctx.Err()
			}
		case "fail":
			return agent.Result{Status: agent.Failed}, errors.New("dependency execution failed")
		default:
			dependentRuns.Add(1)
			return agent.Result{Status: agent.Completed}, nil
		}
	}, nil, options)
	require.NoError(t, err)
	defer closeManager(t, m)
	_, err = m.Submit(ctx, message("occupy", "alice"), t.TempDir(), request("occupy"))
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	first, err := m.Submit(ctx, message("dependency", "alice"), t.TempDir(), request("fail"))
	require.NoError(t, err)
	second, err := m.Submit(ctx, message("dependent", "alice"), t.TempDir(), request("dependent"))
	require.NoError(t, err)
	identity, _ := chat.IdentityFromContext(controlTestContext(first.Message))
	require.NoError(t, m.LinkDependency(ctx, identity, second.ID, first.ID))
	require.Error(t, m.LinkDependency(ctx, identity, first.ID, second.ID))
	stranger := identity
	stranger.SenderID = "mallory"
	require.Error(t, m.LinkDependency(ctx, stranger, second.ID, first.ID))
	close(release)
	waitTask(t, m, first, func(task Task) bool { return task.Status == "failed" })
	waitTask(t, m, second, func(task Task) bool { return task.Status == "blocked" })
	require.Zero(t, dependentRuns.Load(), "failed dependency must not dispatch downstream work")
	_, err = m.GraphTool(controlTestContext(second.Message), map[string]any{"action": "unlink", "task_id": second.ID, "dependency_id": first.ID})
	require.NoError(t, err)
	_, err = m.GraphTool(controlTestContext(second.Message), map[string]any{"action": "unblock", "task_id": second.ID})
	require.NoError(t, err)
	waitTask(t, m, second, func(task Task) bool { return task.Status == "completed" })
	require.EqualValues(t, 1, dependentRuns.Load())
}

func TestManagerQuestionAnswerLockAndRestartResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	questionsReady := make(chan []Question, 1)
	var m *Manager
	var err error
	m, err = Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		call := openai.ToolCall{ID: "clarify-call", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "clarify", Arguments: `{"questions":[{"question":"Choose target","choices":["A","B"]}]}`}}
		emit(agent.Event{Kind: agent.ModelCompleted, Response: agent.Response{ToolCalls: []openai.ToolCall{call}}})
		emit(agent.Event{Kind: agent.ToolStarted, Tool: agent.ToolRecord{Call: call}})
		current, err := m.Get(ctx, message("", "alice").Session, ID(ctx))
		if err != nil {
			return agent.Result{}, err
		}
		value, err := m.QuestionTool(ctx, map[string]any{"action": "ask", "questions": []any{map[string]any{"question": "Choose target", "choices": []string{"A", "B"}}}})
		if err != nil {
			return agent.Result{}, err
		}
		questionsReady <- value.([]Question)
		_, err = m.waitQuestions(ctx, current.ID, value.([]Question))
		return agent.Result{Status: agent.Cancelled}, err
	}, nil, controlTestOptions())
	require.NoError(t, err)
	msg := message("question-source", "alice")
	original, err := m.Submit(context.Background(), msg, t.TempDir(), request("original constrained goal"))
	require.NoError(t, err)
	var questions []Question
	select {
	case questions = <-questionsReady:
	case <-time.After(5 * time.Second):
		t.Fatal("questions were not persisted")
	}
	require.NoError(t, m.Close())
	resumedRequest := make(chan agent.Request, 1)
	m, err = Open(path, func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		resumedRequest <- req
		return agent.Result{Status: agent.Completed, Content: "finished after answer"}, nil
	}, nil, controlTestOptions())
	require.NoError(t, err)
	defer closeManager(t, m)
	persisted, err := m.Get(context.Background(), msg.Session, original.ID)
	require.NoError(t, err)
	require.Equal(t, "waiting_input", persisted.Status)
	trusted := controlTestContext(msg)
	stranger := chat.WithIdentity(context.Background(), chat.Identity{Session: msg.Session, SenderID: "mallory"})
	_, err = m.QuestionTool(stranger, map[string]any{"action": "answer", "question_id": questions[0].ID, "answer": "B"})
	require.Error(t, err)
	answer := map[string]any{"action": "answer", "question_id": questions[0].ID, "answer": "B"}
	_, err = m.QuestionTool(trusted, answer)
	require.NoError(t, err)
	_, err = m.QuestionTool(trusted, answer)
	require.NoError(t, err, "identical answer retry is idempotent")
	_, err = m.QuestionTool(trusted, map[string]any{"action": "answer", "question_id": questions[0].ID, "answer": "A"})
	require.Error(t, err, "locked answer cannot be overwritten")
	value, err := m.QuestionTool(trusted, map[string]any{"action": "resume", "task_id": original.ID})
	require.NoError(t, err)
	resumed := value.(Task)
	require.NotEqual(t, original.ID, resumed.ID, "restart resume must not blindly replay the original run")
	var req agent.Request
	select {
	case req = <-resumedRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("answered task did not resume")
	}
	var content strings.Builder
	for _, msg := range req.Messages {
		content.WriteString(msg.Content)
	}
	require.Contains(t, content.String(), "original constrained goal")
	require.Contains(t, content.String(), "用户澄清结果")
	require.Contains(t, content.String(), "B")
	require.Contains(t, content.String(), "不要自动重放")
	waitTask(t, m, resumed, func(task Task) bool { return task.Status == "completed" })
}

func TestManagerDetachedSurvivesParentCancelAndPreservesReceivedResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	parentStarted := make(chan context.Context, 1)
	childStarted := make(chan struct{}, 1)
	releaseChild := make(chan struct{})
	var prepared, cleaned atomic.Bool
	childDir := t.TempDir()
	artifact := filepath.Join(t.TempDir(), "result.txt")
	m, err := Open(path, func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		if req.Messages[0].Content == "parent" {
			parentStarted <- ctx
			<-ctx.Done()
			return agent.Result{Status: agent.Cancelled}, ctx.Err()
		}
		if !prepared.Load() {
			return agent.Result{}, errors.New("child started before workspace binding")
		}
		childStarted <- struct{}{}
		select {
		case <-releaseChild:
		case <-ctx.Done():
			return agent.Result{Status: agent.Cancelled}, ctx.Err()
		}
		if err := os.WriteFile(artifact, []byte("independent result"), 0600); err != nil {
			return agent.Result{}, err
		}
		tool := agent.ToolRecord{Call: openai.ToolCall{ID: "artifact"}, Content: "artifact generated", Files: []string{artifact}}
		emit(agent.Event{Kind: agent.ToolFinished, Tool: tool})
		return agent.Result{Status: agent.Completed, Content: "independent child completed", Rounds: []agent.Round{{Tools: []agent.ToolRecord{tool}}}}, nil
	}, nil, controlTestOptions())
	require.NoError(t, err)
	msg := message("detached-parent", "alice")
	parent, err := m.Submit(context.Background(), msg, t.TempDir(), request("parent"))
	require.NoError(t, err)
	var parentCtx context.Context
	select {
	case parentCtx = <-parentStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not start")
	}
	identity, _ := chat.IdentityFromContext(parentCtx)
	childMsg := msg
	childMsg.ID, childMsg.Text = "detached-child", "child"
	childReq := request("child")
	childReq.SubagentDepth, childReq.ChildMaxParallel = 1, 1
	childReq.PrepareChild = func(_ context.Context, id int64) error {
		if id <= parent.ID {
			return errors.New("missing real child ID")
		}
		prepared.Store(true)
		return nil
	}
	childReq.ChildCleanup = func(context.Context) error { cleaned.Store(true); return os.RemoveAll(childDir) }
	child, err := m.SpawnDetached(parentCtx, identity, parent.ID, childMsg, childDir, childReq)
	require.NoError(t, err)
	select {
	case <-childStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not run independently")
	}
	another := childMsg
	another.ID = "excess-child"
	_, err = m.SpawnDetached(parentCtx, identity, parent.ID, another, t.TempDir(), childReq)
	require.Error(t, err, "capacity covers the entire running parent tree")
	_, err = m.Cancel(context.Background(), identity, parent.ID)
	require.NoError(t, err)
	waitTask(t, m, parent, func(task Task) bool { return task.Status == "cancelled" })
	stillRunning, err := m.Get(context.Background(), msg.Session, child.ID)
	require.NoError(t, err)
	require.Equal(t, "running", stillRunning.Status, "turn cancellation must not implicitly stop background children")
	close(releaseChild)
	waitTask(t, m, child, func(task Task) bool { return task.Status == "completed" })
	require.True(t, cleaned.Load())
	_, err = os.Stat(childDir)
	require.True(t, os.IsNotExist(err), "completed child workspace is actually cleaned")
	completions, err := m.DrainChildCompletions(context.Background(), identity, parent.ID)
	require.NoError(t, err)
	require.Len(t, completions, 1)
	require.Equal(t, child.ID, completions[0].TaskID)
	require.Equal(t, []string{artifact}, completions[0].Files)
	empty, err := m.DrainChildCompletions(context.Background(), identity, parent.ID)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.NoError(t, m.Close())
	m, err = Open(path, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{}, errors.New("must not replay")
	}, nil, controlTestOptions())
	require.NoError(t, err)
	defer closeManager(t, m)
	received, err := m.receivedChildCompletions(context.Background(), parent.ID)
	require.NoError(t, err)
	require.Equal(t, completions, received, "claim-before-parent-cancel leaves a durable receive copy")
}

func TestManagerReviewDeliversNoticeAndFinalWithoutRerunning(t *testing.T) {
	var m *Manager
	var runs, parts atomic.Int32
	var err error
	m, err = Open(filepath.Join(t.TempDir(), "tasks.db"), func(ctx context.Context, _ agent.Request, emit func(agent.Event)) (agent.Result, error) {
		runs.Add(1)
		emit(agent.Event{Kind: agent.ModelStarted})
		_, err := m.GraphTool(ctx, map[string]any{"action": "request_review", "summary": "implemented and verified requested change"})
		if err != nil {
			return agent.Result{}, err
		}
		identity, _ := chat.IdentityFromContext(ctx)
		content, done, err := m.RequestedCompletion(ctx, identity, ID(ctx))
		if !done {
			return agent.Result{}, errors.New("review did not park the run")
		}
		return agent.Result{Status: agent.Completed, Content: content}, err
	}, func(ctx context.Context, task Task) (string, error) {
		return m.DeliverPart(ctx, task.ID, "body", func(context.Context) ([]byte, error) { return json.Marshal(task.Result.Content) }, func(context.Context, []byte) (string, error) { n := parts.Add(1); return string(rune('a' + n)), nil })
	}, controlTestOptions())
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("review", "alice")
	task, err := m.Submit(context.Background(), msg, t.TempDir(), request("implementation"))
	require.NoError(t, err)
	waitTask(t, m, task, func(task Task) bool { return task.Status == "review" && task.Delivery == "sent" })
	_, err = m.GraphTool(controlTestContext(msg), map[string]any{"action": "approve", "task_id": task.ID})
	require.NoError(t, err)
	final := waitTask(t, m, task, func(task Task) bool { return task.Status == "completed" && task.Delivery == "sent" })
	require.Equal(t, "implemented and verified requested change", final.Result.Content)
	require.EqualValues(t, 1, runs.Load())
	require.EqualValues(t, 2, parts.Load(), "control notice and final result use different durable delivery parts")
}

func TestManagerHandoffRechecksTrustedMapping(t *testing.T) {
	var revoked atomic.Bool
	options := controlTestOptions()
	target := chat.Identity{Session: chat.Session{Platform: "slack", Account: "second", Conversation: "dm-alice"}, SenderID: "alice", Direct: true}
	options.Handoff = func(_ context.Context, source chat.Identity, route string) (chat.Identity, error) {
		if revoked.Load() || route != "same-person" || source.SenderID != "alice" {
			return chat.Identity{}, errors.New("route revoked or unmatched")
		}
		return target, nil
	}
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed, Content: "completed work"}, nil
	}, nil, options)
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("handoff", "alice")
	task, err := m.Submit(context.Background(), msg, t.TempDir(), request("result goal"))
	require.NoError(t, err)
	waitTask(t, m, task, func(task Task) bool { return task.Status == "completed" })
	identity, _ := chat.IdentityFromContext(controlTestContext(msg))
	_, err = m.Handoff(context.Background(), identity, task.ID, "same-person")
	require.NoError(t, err)
	mapped, err := m.DeliveryTask(context.Background(), task)
	require.NoError(t, err)
	require.Equal(t, target.Session, mapped.Message.Session)
	original, err := m.Get(context.Background(), msg.Session, task.ID)
	require.NoError(t, err)
	require.Equal(t, msg.Session, original.Message.Session, "handoff never migrates history or memory ownership")
	revoked.Store(true)
	_, err = m.DeliveryTask(context.Background(), task)
	require.Error(t, err, "revoking mapping blocks previously queued delivery")
}

func TestManagerSubmitTurnRejectsConflictingIdempotencyPayload(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil, controlTestOptions())
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("stable-api-key", "alice")
	dir := t.TempDir()
	req := request("payload")
	first, err := m.SubmitTurn(context.Background(), msg, dir, req)
	require.NoError(t, err)
	waitTask(t, m, first, func(task Task) bool { return task.Status == "completed" })
	duplicate, err := m.SubmitTurn(context.Background(), msg, dir, req)
	require.NoError(t, err)
	require.Equal(t, first.ID, duplicate.ID)
	changed := msg
	changed.Text = "different instructions"
	_, err = m.SubmitTurn(context.Background(), changed, dir, req)
	require.Error(t, err)
	req.Model = "different-model"
	_, err = m.SubmitTurn(context.Background(), msg, dir, req)
	require.Error(t, err)
}
