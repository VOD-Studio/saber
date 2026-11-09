package automation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"rua.plus/saber/internal/agent"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/task"
)

func (s *Service) finishLiveTurn(ctx context.Context, result agent.Result, runErr error) error {
	id := task.ID(ctx)
	if id <= 0 {
		return nil
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("automation completion requires trusted task identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tasks == nil {
		return nil
	}
	t, err := s.tasks.Get(ctx, i.Session, id)
	if err != nil {
		return err
	}
	if t.Message.SenderID != i.SenderID {
		return errors.New("automation result owner mismatch")
	}
	t.Result = result
	if result.Status == agent.Completed && runErr == nil {
		t.Status = "completed"
	} else {
		t.Status = string(result.Status)
		if t.Status == "" {
			t.Status = "failed"
		}
		if runErr != nil {
			t.Error = runErr.Error()
		}
	}
	var combined error
	for _, kind := range []string{"goal", "loop", "heartbeat"} {
		st, rev, e := s.load(ctx, i, kind)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			combined = errors.Join(combined, e)
			continue
		}
		if st.Status != "active" {
			continue
		}
		generation, e := s.tasks.ContextGeneration(ctx, i.Session)
		if e != nil {
			combined = errors.Join(combined, e)
			continue
		}
		if st.Generation != generation {
			st.Status, st.Reason = "cleared", "conversation reset or switched"
			combined = errors.Join(combined, s.save(ctx, st, rev))
			continue
		}
		manualBarrier := st.WaitingProcess != nil && st.WaitingProcess.SourceTask == id && st.WaitingProcess.TargetTask == 0
		ownTick := st.LastTask == id && st.Attempt != ""
		if !ownTick && !manualBarrier {
			if kind != "goal" || st.LastTask == id || t.CreatedAt.Before(st.Created) || st.WaitingTask > 0 || !st.WaitingUntil.IsZero() {
				continue
			}
			st.Ticks++
			st.LastTask = id
		}
		if !manualBarrier {
			s.complete(ctx, &st, t, time.Now(), true)
		}
		st.Attempt = ""
		if st.Status == "active" && st.WaitingProcess != nil && t.Status == "completed" {
			if e = s.parkProcess(ctx, &st); e != nil {
				st.Status, st.Reason = "paused", "process transfer rejected: "+e.Error()
				combined = errors.Join(combined, e)
			}
		}
		combined = errors.Join(combined, s.save(ctx, st, rev))
	}
	return combined
}
