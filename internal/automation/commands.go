package automation

import (
	"context"
	"errors"
	"strings"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// CommandRequest 只识别待执行标记；真实授权由 RunCommand 核对持久发生。
func CommandRequest(req agent.Request) (string, bool) {
	for _, m := range req.Messages {
		if m.Role == "system" && m.Name == "saber_session_command" {
			return m.Content, true
		}
	}
	return "", false
}
func recursiveCommand(raw string) bool {
	fields := strings.Fields(strings.TrimLeft(strings.TrimSpace(raw), "/!"))
	if len(fields) == 0 {
		return true
	}
	switch fields[0] {
	case "loop", "proactive", "goal", "heartbeat", "hb", "subgoal", "cron", "schedule":
		return true
	}
	return false
}

// RunCommand 在共享会话命令分发器执行已配置 loop 命令，绝不将模型标记当作授权。
func (s *Service) RunCommand(ctx context.Context, req agent.Request, emit func(agent.Event)) (agent.Result, error) {
	command, ok := CommandRequest(req)
	if !ok || task.ID(ctx) <= 0 {
		return agent.Result{}, errors.New("session command requires persistent automation task")
	}
	if recursiveCommand(command) {
		return agent.Result{}, errors.New("recurring command cannot recursively create automation")
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return agent.Result{}, errors.New("session command requires trusted identity")
	}
	s.mu.Lock()
	m := s.tasks
	dispatch := s.bindings.Command
	st, _, err := s.load(ctx, i, "loop")
	s.mu.Unlock()
	if err != nil {
		return agent.Result{}, err
	}
	if m == nil || dispatch == nil {
		return agent.Result{}, errors.New("session command dispatcher unavailable")
	}
	t, err := m.Get(ctx, i.Session, task.ID(ctx))
	if err != nil {
		return agent.Result{}, err
	}
	generation, err := m.ContextGeneration(ctx, i.Session)
	if err != nil {
		return agent.Result{}, err
	}
	if st.Generation != generation || st.Status != "active" || st.LastTask != t.ID || st.Prompt != command || !strings.HasPrefix(t.EventKey, "saber-automation:"+st.ID+":") {
		return agent.Result{}, errors.New("session command occurrence no longer authorized")
	}
	if err = s.host.Check(ctx, "automation_loop", false); err != nil {
		return agent.Result{}, err
	}
	start := time.Now()
	if emit != nil {
		emit(agent.Event{Kind: agent.RunStarted})
	}
	text, err := dispatch(ctx, command)
	status := agent.Completed
	if err != nil {
		status = agent.Failed
	}
	if errors.Is(err, context.Canceled) {
		status = agent.Cancelled
	}
	result := agent.Result{Status: status, Content: text, Duration: time.Since(start)}
	boundaryErr := s.finishLiveTurn(ctx, result, err)
	if emit != nil {
		emit(agent.Event{Kind: agent.RunFinished, Text: text, Status: status, Error: err})
	}
	return result, errors.Join(err, boundaryErr)
}
