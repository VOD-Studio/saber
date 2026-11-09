package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"rua.plus/saber/internal/chat"
)

// SetSkillAudit 绑定提交后审计，回调不在 memory 事务内运行；失败不能伪称已回滚。
func (s *Service) SetSkillAudit(audit func(context.Context, chat.Identity, string, *SkillPackage, *SkillPackage) error) {
	s.skillAudit = audit
}

func currentSkillPackageTx(ctx context.Context, tx *sql.Tx, scope Scope, name string) (*SkillPackage, error) {
	entry, err := querySkillByName(ctx, tx, scope, name)
	if errors.Is(err, ErrSkillNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pkg, err := loadSkillPackageTx(ctx, tx, scope, name, false, 0)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		pkg = &SkillPackage{}
	}
	pkg.Name = entry.Name
	pkg.Description = entry.Description
	pkg.Content = entry.Content
	pkg.Version = entry.Version
	return pkg, nil
}

func (s *Service) commitSkillMutation(ctx context.Context, tx *sql.Tx, scope Scope, in skillMutationInput, entry SkillEntry, before *SkillPackage) error {
	if s.skillAudit == nil {
		return tx.Commit()
	}
	var after *SkillPackage
	if in.Action != ActionRemove {
		var err error
		after, err = currentSkillPackageTx(ctx, tx, scope, entry.Name)
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.skillAudit == nil {
		return nil
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return fmt.Errorf("技能已提交但审计失败: %w", ErrScope)
	}
	if err := s.skillAudit(ctx, identity, in.Action, before, after); err != nil {
		return fmt.Errorf("技能已提交但审计失败: %w", err)
	}
	return nil
}
