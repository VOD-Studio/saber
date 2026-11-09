package task

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/require"
	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
)

func TestManagerExternalTaskCancellationAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	var modelRuns atomic.Int32
	run := func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		modelRuns.Add(1)
		return agent.Result{Status: agent.Completed}, nil
	}
	m, err := Open(path, run, nil, controlTestOptions())
	require.NoError(t, err)
	msg := message("external-audio", "alice")
	identity, _ := chat.IdentityFromContext(controlTestContext(msg))
	dir := t.TempDir()
	voice, err := m.StartExternal(controlTestContext(msg), msg, dir, "voice")
	require.NoError(t, err)
	require.Equal(t, "running", voice.Status)
	bound, err := m.BindToolContext(context.Background(), identity, voice.ID)
	require.NoError(t, err)
	require.Equal(t, voice.ID, ID(bound))
	canonical, err := canonicalDir(dir)
	require.NoError(t, err)
	require.Equal(t, canonical, WorkDir(bound))
	cancelled, err := m.Cancel(context.Background(), identity, voice.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.Status)
	require.True(t, cancelled.CancelRequested)
	select {
	case <-bound.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("provider context survived authoritative cancellation")
	}
	require.Zero(t, modelRuns.Load(), "audio lifecycle must never dispatch a fake model request")
	msg.ID = "external-restart"
	interrupted, err := m.StartExternal(controlTestContext(msg), msg, dir, "voice")
	require.NoError(t, err)
	require.NoError(t, m.Close())
	m, err = Open(path, run, nil, controlTestOptions())
	require.NoError(t, err)
	defer closeManager(t, m)
	got, err := m.Get(context.Background(), msg.Session, interrupted.ID)
	require.NoError(t, err)
	require.Equal(t, "interrupted", got.Status)
	require.Zero(t, modelRuns.Load(), "restart cannot replay external side effects")
}

func TestManagerContinuationProcessOwnershipOutlivesSource(t *testing.T) {
	var m *Manager
	var err error
	transferred := make(chan context.Context, 1)
	sourceContext := make(chan context.Context, 1)
	targetStarted := make(chan context.Context, 1)
	targetTask := make(chan Task, 1)
	msg := message("process-source", "alice")
	dir := t.TempDir()
	options := controlTestOptions()
	options.TransferProcesses = func(source, destination context.Context) error {
		if err := m.AuthorizeProcessTransfer(source, destination); err != nil {
			return err
		}
		transferred <- destination
		return nil
	}
	m, err = Open(filepath.Join(t.TempDir(), "tasks.db"), func(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
		if req.Messages[len(req.Messages)-1].Content == "followup" {
			targetStarted <- ctx
			<-ctx.Done()
			return agent.Result{Status: agent.Cancelled}, ctx.Err()
		}
		emit(agent.Event{Kind: agent.ModelStarted})
		sourceContext <- ctx
		next := msg
		next.ID, next.Text = "saber-automation:process-followup", "followup"
		target, err := m.PrepareContinuation(ctx, next, dir, request("followup"))
		if err != nil {
			return agent.Result{}, err
		}
		targetTask <- target
		return agent.Result{Status: agent.Completed, Content: "source handed over"}, nil
	}, nil, options)
	require.NoError(t, err)
	defer closeManager(t, m)
	source, err := m.Submit(context.Background(), msg, dir, request("source"))
	require.NoError(t, err)
	var original, destination context.Context
	var target Task
	select {
	case original = <-sourceContext:
	case <-time.After(5 * time.Second):
		t.Fatal("source did not run")
	}
	select {
	case target = <-targetTask:
	case <-time.After(5 * time.Second):
		t.Fatal("continuation did not persist")
	}
	select {
	case destination = <-transferred:
	case <-time.After(5 * time.Second):
		t.Fatal("ownership was not transferred before finalization")
	}
	waitTask(t, m, source, func(task Task) bool { return task.Status == "completed" })
	select {
	case <-original.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("source lifecycle did not finish")
	}
	require.NoError(t, destination.Err(), "destination ownership cannot inherit finished source cancellation")
	var running context.Context
	select {
	case running = <-targetStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("target did not claim saved lifecycle")
	}
	require.Equal(t, destination.Done(), running.Done())
	identity, _ := chat.IdentityFromContext(controlTestContext(msg))
	_, err = m.Cancel(context.Background(), identity, target.ID)
	require.NoError(t, err)
	select {
	case <-destination.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("target cancellation did not stop transferred lifecycle")
	}
	waitTask(t, m, target, func(task Task) bool { return task.Status == "cancelled" })
}

func TestManagerScheduledPreparationKeepsEphemeralSkills(t *testing.T) {
	prepared := make(chan agent.Request, 1)
	options := controlTestOptions()
	options.Schedule = func(chat.Identity, string) error { return nil }
	options.PrepareSchedule = func(_ context.Context, s Schedule) (agent.Request, error) {
		req := s.Request
		req.RuntimeContext = []openai.ChatCompletionMessage{{Role: "system", Content: "current authorized skill resource"}}
		return req, nil
	}
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), func(_ context.Context, req agent.Request, _ func(agent.Event)) (agent.Result, error) {
		prepared <- req
		return agent.Result{Status: agent.Completed}, nil
	}, nil, options)
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("prepared-schedule", "alice")
	identity, _ := chat.IdentityFromContext(controlTestContext(msg))
	plan, err := m.CreateSchedule(context.Background(), msg, t.TempDir(), request("scheduled goal"), ScheduleSpec{Kind: "once", Timezone: "UTC", At: time.Now().Add(time.Hour).Format(time.RFC3339)})
	require.NoError(t, err)
	occurrence, err := m.RunSchedule(context.Background(), identity, plan.ID)
	require.NoError(t, err)
	select {
	case req := <-prepared:
		require.Len(t, req.RuntimeContext, 1)
		require.Equal(t, "current authorized skill resource", req.RuntimeContext[0].Content)
	case <-time.After(5 * time.Second):
		t.Fatal("prepared schedule did not reach the real runner")
	}
	stored, err := m.Get(context.Background(), msg.Session, occurrence.TaskID)
	require.NoError(t, err)
	require.Empty(t, stored.Request.RuntimeContext, "temporary authorized skill context must not leak into persistent request JSON")
}

func TestManagerAutomationGenerationAndInternalEventKeys(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "tasks.db"), func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{Status: agent.Completed}, nil
	}, nil, controlTestOptions())
	require.NoError(t, err)
	defer closeManager(t, m)
	msg := message("saber-automation:stable", "alice")
	generation, err := m.ContextGeneration(context.Background(), msg.Session)
	require.NoError(t, err)
	task, err := m.SubmitAutomationAtGeneration(context.Background(), msg, t.TempDir(), request("work"), false, generation)
	require.NoError(t, err)
	require.Equal(t, msg.ID, task.EventKey)
	require.Empty(t, task.Message.ID, "internal automation key is not a platform message reference")
	_, err = m.ClearContext(context.Background(), msg.Session)
	require.NoError(t, err)
	msg.ID = "saber-automation:stale"
	_, err = m.SubmitAutomationAtGeneration(context.Background(), msg, t.TempDir(), request("stale work"), false, generation)
	require.Error(t, err)
	_, err = m.TaskForMessage(context.Background(), msg.Session, msg.ID)
	require.True(t, errors.Is(err, sql.ErrNoRows))
}
