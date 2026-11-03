package ai

import (
	"context"
	"errors"
	"strings"

	"rua.plus/saber/internal/chat"
)

// ApproveCommand 批准或拒绝一次危险命令的一次性审批码，返回给审批者的中文回执。
//
// 身份必须来自消息入口，不能由模型参数提供；能够操作的范围由执行器校验：
// 同一机器人账号下该工作区的有效授权成员，或配置中的精确任务管理员。
func (s *Service) ApproveCommand(ctx context.Context, identity chat.Identity, code string, approve bool) (string, error) {
	if s.executor == nil {
		return "", errors.New("未配置执行权限（execution.enabled 未启用），无法审批命令")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("用法：!approve <审批码> 或 !deny <审批码>")
	}
	if identity.SenderID == "" || identity.Session.Validate() != nil {
		return "", errors.New("审批需要可信的接入身份")
	}
	return s.executor.ResolveApproval(identity, code, approve)
}
