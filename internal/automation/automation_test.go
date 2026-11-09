package automation

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/memory"
	"rua.plus/saber/internal/task"
	"rua.plus/saber/internal/toolkit"
)

func automationFixture(t *testing.T, run task.RunFunc) (*Service, *task.Manager, context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	state := t.TempDir()
	m, err := task.Open(filepath.Join(state, "tasks.db"), run, nil, task.Options{Schedule: func(chat.Identity, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg := config.DefaultAutomationConfig()
	cfg.Enabled = true
	cfg.PollSeconds = 60
	cfg.Curator.Enabled = false
	host := toolkit.Host{StateDir: state, Check: func(context.Context, string, bool) error { return nil }, Workspace: func(context.Context) (string, error) { return dir, nil }, IsAdmin: func(context.Context) bool { return true }, Call: func(context.Context, string, map[string]any) (any, error) {
		return nil, errors.New("unexpected tool call")
	}}
	s, err := New(cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	s.AttachTaskManager(m)
	s.Configure(Bindings{Prepare: func(_ context.Context, msg chat.Message, _ []string, _ string) (agent.Request, error) {
		return agent.Request{Messages: []openai.ChatCompletionMessage{{Role: "user", Content: msg.Text}}}, nil
	}})
	i := chat.Identity{Session: chat.Session{Platform: "test", Account: "bot", Conversation: "chat", Thread: "thread"}, SenderID: "user", Direct: true}
	return s, m, chat.WithIdentity(context.Background(), i), dir
}
func waitTerminal(t *testing.T, m *task.Manager, ctx context.Context, id int64) task.Task {
	t.Helper()
	i, _ := chat.IdentityFromContext(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		item, err := m.Get(ctx, i.Session, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Status != "queued" && item.Status != "running" {
			return item
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task did not terminate")
	return task.Task{}
}

func TestLoopRecoveryDoesNotReplayDispatchedTask(t *testing.T) {
	var calls atomic.Int32
	s, m, ctx, _ := automationFixture(t, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		calls.Add(1)
		return agent.Result{Status: agent.Completed, Content: "LOOP_COMPLETE"}, nil
	})
	value, err := s.Execute(ctx, "automation_loop", map[string]any{"action": "set", "prompt": "watch deployment", "interval": "30s"})
	if err != nil {
		t.Fatal(err)
	}
	state := value.(State)
	if err = s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	i, _ := chat.IdentityFromContext(ctx)
	state, _, err = s.load(ctx, i, "loop")
	if err != nil || state.LastTask == 0 {
		t.Fatalf("missing admission: %+v %v", state, err)
	}
	waitTerminal(t, m, ctx, state.LastTask)
	if err = s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.cfg, s.host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	reopened.Configure(s.bindings)
	reopened.AttachTaskManager(m)
	if err = reopened.Tick(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	restored, _, err := reopened.load(ctx, i, "loop")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "done" || calls.Load() != 1 {
		t.Fatalf("state=%+v actual runs=%d", restored, calls.Load())
	}
}

func TestUnknownPreparedOccurrencePausesWithoutExecution(t *testing.T) {
	var calls atomic.Int32
	s, _, ctx, dir := automationFixture(t, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		calls.Add(1)
		return agent.Result{Status: agent.Completed}, nil
	})
	i, _ := chat.IdentityFromContext(ctx)
	generation, err := s.tasks.ContextGeneration(ctx, i.Session)
	if err != nil {
		t.Fatal(err)
	}
	st := State{ID: "prepared", Kind: "loop", Owner: i, WorkDir: dir, Message: identityMessage(i, "watch"), Generation: generation, Status: "active", Attempt: "unknown", Next: time.Now()}
	if err = s.save(ctx, st, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	st, _, err = s.load(ctx, i, "loop")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "paused" || calls.Load() != 0 {
		t.Fatalf("unknown admission replayed: %+v calls=%d", st, calls.Load())
	}
}

func TestHeartbeatCoalescesMissedTicksAndReanchorsResume(t *testing.T) {
	var calls atomic.Int32
	s, m, ctx, _ := automationFixture(t, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		calls.Add(1)
		return agent.Result{Status: agent.Completed, Content: "NO_REPLY"}, nil
	})
	if _, err := s.Execute(ctx, "automation_heartbeat", map[string]any{"action": "set", "prompt": "watch CI", "interval": "1m"}); err != nil {
		t.Fatal(err)
	}
	i, _ := chat.IdentityFromContext(ctx)
	future := time.Now().Add(24 * time.Hour)
	if err := s.Tick(ctx, future); err != nil {
		t.Fatal(err)
	}
	st, _, err := s.load(ctx, i, "heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, m, ctx, st.LastTask)
	if err = s.Tick(ctx, future); err != nil {
		t.Fatal(err)
	}
	st, _, err = s.load(ctx, i, "heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	if st.Ticks != 1 || calls.Load() != 1 {
		t.Fatalf("missed ticks were replayed %+v", st)
	}
	if _, err = s.Execute(ctx, "automation_heartbeat", map[string]any{"action": "pause"}); err != nil {
		t.Fatal(err)
	}
	value, err := s.Execute(ctx, "automation_heartbeat", map[string]any{"action": "resume"})
	if err != nil {
		t.Fatal(err)
	}
	if !value.(State).Next.After(time.Now()) {
		t.Fatal("resume did not reanchor")
	}
}

func TestHookExitTwoAndFailClosedRejectBeforeTool(t *testing.T) {
	s, _, ctx, _ := automationFixture(t, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{}, nil
	})
	s.cfg.Hooks = []config.AutomationHook{{Name: "block", Events: []string{"pre_tool_call"}, Tool: "exec", Args: map[string]any{"command": "trusted-hook"}, FailClosed: true}}
	s.host.Call = func(context.Context, string, map[string]any) (any, error) {
		return execution.Result{ExitCode: 2, Summary: "{}"}, nil
	}
	if _, err := s.BeforeTool(ctx, "write_file", map[string]any{"path": "a"}); err == nil {
		t.Fatal("exit 2 allowed a mutation")
	}
	s.host.Call = func(context.Context, string, map[string]any) (any, error) { return nil, context.DeadlineExceeded }
	if _, err := s.BeforeTool(ctx, "write_file", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fail-closed did not preserve failure: %v", err)
	}
}

func TestSignedOutboundHookRefusesRedirectReplay(t *testing.T) {
	var deliveries atomic.Int32
	secret := "test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			deliveries.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		if r.Header.Get("X-Hermes-Signature-256") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("signature mismatch")
		}
		var payload map[string]any
		if err = json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
		}
		if payload["delivery_id"] != r.Header.Get("X-Hermes-Delivery") {
			t.Error("delivery identifier not covered by signature")
		}
		http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	if err := sendHook(context.Background(), server.URL, secret, "agent:end", map[string]any{"response": "done"}); err == nil {
		t.Fatal("redirect considered successful")
	}
	if deliveries.Load() != 0 {
		t.Fatal("signed event replayed to redirect target")
	}
}

func TestSkillLockUsesMemorySpaceAcrossThreads(t *testing.T) {
	s, _, ctx, _ := automationFixture(t, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{}, nil
	})
	i, _ := chat.IdentityFromContext(ctx)
	i.Direct = false
	if err := s.setFlags(chat.WithIdentity(ctx, i), i, "locked-skill", skillFlags{Locked: true}); err != nil {
		t.Fatal(err)
	}
	other := i
	other.Session.Thread = "other-thread"
	if err := s.SkillWritable(chat.WithIdentity(ctx, other), "locked-skill"); err == nil {
		t.Fatal("sibling thread bypassed shared skill lock")
	}
	other.Session.Conversation = "other-room"
	if err := s.SkillWritable(chat.WithIdentity(ctx, other), "locked-skill"); err != nil {
		t.Fatalf("lock leaked to another room: %v", err)
	}
}

func TestCuratorArchiveRestorePreservesPackageResources(t *testing.T) {
	s, _, ctx, _ := automationFixture(t, func(context.Context, agent.Request, func(agent.Event)) (agent.Result, error) {
		return agent.Result{}, nil
	})
	mem, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"), memory.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := mem.Close(); err != nil {
			t.Error(err)
		}
	}()
	i, _ := chat.IdentityFromContext(ctx)
	pkg := memory.SkillPackage{Name: "restore-skill", Description: "test", Content: "a real procedure", Files: map[string][]byte{"scripts/check.txt": []byte("preserved")}, Metadata: map[string]any{"tags": []any{"testing"}}, Provenance: memory.SkillProvenance{Source: "agent"}}
	if _, err = mem.PutSkillPackage(ctx, i, pkg, 0, memory.Source{Creator: i.SenderID}); err != nil {
		t.Fatal(err)
	}
	s.bindings.Skills = SkillStore{Get: mem.GetSkillPackage, Put: func(ctx context.Context, i chat.Identity, p memory.SkillPackage, v int64) error {
		_, err := mem.PutSkillPackage(ctx, i, p, v, memory.Source{Creator: i.SenderID})
		return err
	}, Remove: func(ctx context.Context, i chat.Identity, n string, v int64) error {
		_, err := mem.RemoveSkill(ctx, i, n, v, memory.Source{Creator: i.SenderID})
		return err
	}}
	if _, err = s.archive(ctx, i, pkg.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = mem.GetSkillPackage(ctx, i, pkg.Name); !errors.Is(err, memory.ErrSkillNotFound) {
		t.Fatalf("archive did not remove active skill: %v", err)
	}
	if _, err = s.restore(ctx, i, pkg.Name, false); err != nil {
		t.Fatal(err)
	}
	restored, err := mem.GetSkillPackage(ctx, i, pkg.Name)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored.Files["scripts/check.txt"]) != "preserved" || restored.Content != pkg.Content {
		t.Fatalf("package lost during restore %+v", restored)
	}
	ledger, err := s.ledger(ctx, i, pkg.Name)
	if err != nil || len(ledger) != 2 {
		t.Fatalf("recoverable mutations not ledgered %+v %v", ledger, err)
	}
}
