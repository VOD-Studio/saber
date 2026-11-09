package execution

import (
	"context"
	"errors"
	"slices"

	"rua.plus/saber/internal/chat"
)

// CheckArtifactAccess 复核终态产物的 read_file 精确授权与任务输出 ACL，不重开已经移除的执行目录。
func (e *Executor) CheckArtifactAccess(ctx context.Context, sourceTaskID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("artifact requires trusted identity")
	}
	grant, ok := e.grant(identity)
	if !e.cfg.Enabled || !ok || !slices.Contains(grant.Tools, "read_file") {
		return errors.New("artifact delivery requires read_file authorization")
	}
	if sourceTaskID <= 0 {
		return errors.New("artifact requires persistent source task")
	}
	authorize := e.outputAuthorizer()
	if authorize == nil {
		return errors.New("artifact output access is not configured")
	}
	return authorize(ctx, sourceTaskID)
}
