package automation

import (
	"context"
	"errors"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

// RecordSkillMutation 记录已提交的前台、命令或审批修改，不伪装成可回滚的事务失败。
func (s *Service) RecordSkillMutation(ctx context.Context, i chat.Identity, action string, before, after *memory.SkillPackage) error {
	if ctx.Value(curatorInternalKey{}) != nil {
		return nil
	}
	if _, ok := memory.Space(i); !ok {
		return errors.New("skill audit requires trusted memory scope")
	}
	name := ""
	if after != nil {
		name = after.Name
	} else if before != nil {
		name = before.Name
	}
	if name == "" {
		return errors.New("skill audit missing package name")
	}
	_, err := s.recordLedger(ctx, i, name, action, before, after, 0)
	return err
}
