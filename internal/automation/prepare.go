package automation

import (
	"context"
	"errors"
	"strings"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

// PrepareTask 在序列化恢复后从当前授权空间重载技能；临时上下文不写入持久请求。
func (s *Service) PrepareTask(ctx context.Context, req agent.Request) (agent.Request, error) {
	id := task.ID(ctx)
	if id <= 0 {
		return req, nil
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return req, errors.New("automation preparation requires trusted identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks == nil {
		return req, errors.New("automation task manager unavailable")
	}
	t, err := s.tasks.Get(ctx, i.Session, id)
	if err != nil {
		return req, err
	}
	if !strings.HasPrefix(t.EventKey, "saber-automation:") {
		return req, nil
	}
	for _, kind := range []string{"goal", "loop", "heartbeat"} {
		st, _, e := s.load(ctx, i, kind)
		if e != nil {
			continue
		}
		if !strings.HasPrefix(t.EventKey, "saber-automation:"+st.ID+":") {
			continue
		}
		generation, e := s.tasks.ContextGeneration(ctx, i.Session)
		if e != nil {
			return req, e
		}
		if st.Generation != generation || st.Status != "active" || st.LastTask != id {
			return req, errors.New("automation occurrence paused, replaced or reset before dispatch")
		}
		if e = s.host.Check(ctx, "automation_"+kind, false); e != nil {
			return req, e
		}
		if len(st.Skills) == 0 {
			return req, nil
		}
		if s.bindings.LoadSkills == nil {
			return req, errors.New("automation skill loader unavailable")
		}
		return s.bindings.LoadSkills(ctx, req, st.Skills)
	}
	return req, errors.New("automation occurrence has no matching current state")
}
